import { TransferTransport, ReceivedChunk, TransferProgress, TransferError, TransportStatus, TransportMode } from './TransferTransport';
import { Metadata, Message } from '../types';
import { TransferClient } from '../services/transferClient';
import { logger } from '../services/logger';

const SUB_CHUNK_SIZE = 60 * 1024; // 60KB sub-chunks to stay well within browser SCTP limits

function encodeSubChunk(chunkIndex: number, subIndex: number, totalSubChunks: number, payload: ArrayBuffer): ArrayBuffer {
  const header = new ArrayBuffer(20 + payload.byteLength);
  const view = new DataView(header);
  view.setBigUint64(0, BigInt(chunkIndex));
  view.setUint32(8, subIndex);
  view.setUint32(12, totalSubChunks);
  view.setUint32(16, payload.byteLength);
  new Uint8Array(header, 20).set(new Uint8Array(payload));
  return header;
}

function parseSubChunk(buffer: ArrayBuffer) {
  if (buffer.byteLength < 20) throw new Error('Invalid sub-chunk');
  const view = new DataView(buffer);
  const chunkIndex = Number(view.getBigUint64(0));
  const subIndex = view.getUint32(8);
  const totalSubChunks = view.getUint32(12);
  const length = view.getUint32(16);
  if (length !== buffer.byteLength - 20) throw new Error('Payload size mismatch');
  return { chunkIndex, subIndex, totalSubChunks, payloadStart: 20, payloadLength: length };
}

export class WebRTCTransport implements TransferTransport {
  private role: 'sender' | 'receiver';
  private signaling: TransferClient;
  private id: string;
  private file?: File;
  private metadata?: Metadata;
  private status: TransportStatus = 'new';
  private startChunk = 0;

  private pc?: RTCPeerConnection;
  private dataChannel?: RTCDataChannel;
  private isConnected = false;

  private chunkCallback?: (chunk: ReceivedChunk) => void;
  private progressCallback?: (progress: TransferProgress) => void;
  private errorCallback?: (error: TransferError) => void;
  private statusCallback?: (status: TransportStatus, activeTransport: TransportMode) => void;
  private metadataCallback?: (metadata: Metadata) => void;

  private unsubscribeSignaling?: () => void;

  // Assembly map for receiving sub-chunks
  private receiveAssembly = new Map<number, { receivedCount: number, subChunks: Array<{ data: ArrayBuffer; length: number } | undefined> }>();

  constructor(role: 'sender' | 'receiver', signaling: TransferClient, id: string, file?: File, metadata?: Metadata) {
    this.role = role;
    this.signaling = signaling;
    this.id = id;
    this.file = file;
    this.metadata = metadata;
  }

  async connect(): Promise<void> {
    this.updateStatus('connecting');

    // Parse ICE configurations
    let iceServers = [{ urls: 'stun:stun.l.google.com:19302' }, { urls: 'stun:stun1.l.google.com:19302' }];
    const configuredServers = import.meta.env.VITE_WEBRTC_ICE_SERVERS;
    if (configuredServers) {
      try {
        iceServers = JSON.parse(configuredServers);
      } catch (e) {
        logger.warn('Failed to parse VITE_WEBRTC_ICE_SERVERS, using fallback:', e);
      }
    }

    this.pc = new RTCPeerConnection({ iceServers });

    // Setup ice state diagnostics
    this.pc.oniceconnectionstatechange = () => {
      logger.debug(`[WebRTCTransport] ICE connection state: ${this.pc?.iceConnectionState}`);
      if (this.pc?.iceConnectionState === 'failed' || this.pc?.iceConnectionState === 'closed') {
        this.handleError(`ICE connection ${this.pc.iceConnectionState}`);
      }
    };

    this.pc.onconnectionstatechange = () => {
      logger.debug(`[WebRTCTransport] Connection state: ${this.pc?.connectionState}`);
      if (this.pc?.connectionState === 'connected') {
        this.isConnected = true;
        // Inform backend for active transport tracking
        this.signaling.send({ type: 'webrtc_connected' });
      } else if (this.pc?.connectionState === 'failed' || this.pc?.connectionState === 'closed') {
        this.handleError(`Connection ${this.pc.connectionState}`);
      }
    };

    // Trickle ICE candidates
    this.pc.onicecandidate = event => {
      if (event.candidate && this.pc && this.pc.signalingState !== 'closed') {
        this.signaling.send({
          type: 'webrtc_ice_candidate',
          candidate: event.candidate.candidate,
          sdpMid: event.candidate.sdpMid || '',
          sdpMLineIndex: event.candidate.sdpMLineIndex ?? 0
        });
      }
    };

    // Listen to signaling messages
    this.unsubscribeSignaling = this.signaling.onMessage(msg => {
      logger.debug(`[WebRTCTransport] [${this.role}] Received signaling:`, msg.type);
      return this.handleSignaling(msg);
    });

    if (this.role === 'sender') {
      this.dataChannel = this.pc.createDataChannel('file-transfer', { ordered: true });
      this.setupDataChannel();
    } else {
      // Receiver listens for Data Channel
      this.pc.ondatachannel = event => {
        this.dataChannel = event.channel;
        this.setupDataChannel();
      };
    }
  }

  private async createOffer() {
    if (!this.pc) return;
    try {
      logger.debug('[WebRTCTransport] Creating WebRTC offer...');
      const offer = await this.pc.createOffer();
      await this.pc.setLocalDescription(offer);
      this.signaling.send({ type: 'webrtc_offer', sdp: offer.sdp });
    } catch (e) {
      this.handleError(e instanceof Error ? e.message : 'Failed to create WebRTC offer');
    }
  }

  private setupDataChannel() {
    if (!this.dataChannel) return;
    this.dataChannel.binaryType = 'arraybuffer';

    this.dataChannel.onopen = () => {
      logger.debug('[WebRTCTransport] DataChannel open');
      this.updateStatus('connected');
    };

    this.dataChannel.onclose = () => {
      logger.debug('[WebRTCTransport] DataChannel closed');
      this.updateStatus('closed');
    };

    this.dataChannel.onerror = error => {
      logger.error('[WebRTCTransport] DataChannel error:', error);
      this.handleError('Data channel error');
    };

    if (this.role === 'receiver') {
      this.dataChannel.onmessage = event => {
        try {
          const frame = event.data as ArrayBuffer;
          const sub = parseSubChunk(frame);
          if (sub.totalSubChunks === 0) {
            if (sub.subIndex === 1) {
              logger.debug('[WebRTCTransport] [receiver] Received transfer_complete control frame over DataChannel');
              this.updateStatus('completed');
            }
            return;
          }
          let assembly = this.receiveAssembly.get(sub.chunkIndex);
          if (!assembly) {
            assembly = { receivedCount: 0, subChunks: new Array(sub.totalSubChunks) };
            this.receiveAssembly.set(sub.chunkIndex, assembly);
          }
          assembly.subChunks[sub.subIndex] = { data: frame, length: sub.payloadLength };
          assembly.receivedCount++;

          if (assembly.receivedCount === sub.totalSubChunks) {
            // Reconstitute the full chunk with a single copy straight from the sub-frames.
            const totalBytes = assembly.subChunks.reduce((acc, slot) => acc + (slot ? slot.length : 0), 0);
            const fullBuffer = new Uint8Array(totalBytes);
            let offset = 0;
            for (const slot of assembly.subChunks) {
              if (!slot) continue;
              fullBuffer.set(new Uint8Array(slot.data, sub.payloadStart, slot.length), offset);
              offset += slot.length;
            }
            this.receiveAssembly.delete(sub.chunkIndex);

            if (this.chunkCallback) {
              this.chunkCallback({ index: sub.chunkIndex, bytes: fullBuffer.buffer });
            }
          }
        } catch (e) {
          logger.error('[WebRTCTransport] Error decoding WebRTC subchunk:', e);
          this.handleError('Failed to parse WebRTC binary frame');
        }
      };
    }
  }

  private async handleSignaling(msg: Message) {
    if (msg.type === 'webrtc_offer' && this.role === 'receiver' && this.pc) {
      await this.pc.setRemoteDescription(new RTCSessionDescription({ type: 'offer', sdp: msg.sdp }));
      const answer = await this.pc.createAnswer();
      await this.pc.setLocalDescription(answer);
      this.signaling.send({ type: 'webrtc_answer', sdp: answer.sdp });
    } else if (msg.type === 'webrtc_answer' && this.role === 'sender' && this.pc) {
      await this.pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: msg.sdp }));
    } else if (msg.type === 'webrtc_ice_candidate' && this.pc && this.pc.remoteDescription) {
      await this.pc.addIceCandidate(new RTCIceCandidate({
        candidate: msg.candidate,
        sdpMid: msg.sdpMid,
        sdpMLineIndex: msg.sdpMLineIndex
      }));
    } else if (msg.type === 'webrtc_fallback') {
      this.handleError('Fallback requested by remote peer.');
    } else if (msg.type === 'transfer_complete') {
      logger.debug('[WebRTCTransport] Ignoring signaling transfer_complete; awaiting DataChannel control frame');
    } else if (msg.type === 'transfer_cancelled' || msg.type === 'error') {
      this.handleError(msg.message || 'Remote side cancelled/errored.');
    } else if (msg.type === 'transfer_offer') {
      this.startChunk = msg.nextChunk || 0;
      if (msg.metadata) {
        this.metadataCallback?.(msg.metadata);
      }
    } else if (msg.type === 'transfer_accepted' && this.role === 'sender') {
      void this.createOffer();
    }
  }

  async sendMetadata(metadata: Metadata): Promise<void> {
    this.metadata = metadata;
  }

  async sendChunk(chunk: ArrayBuffer, index: number): Promise<void> {
    if (this.status === 'failed' || this.status === 'closed') {
      throw new Error('Transport not connected');
    }

    if (!this.dataChannel || this.dataChannel.readyState !== 'open') {
      throw new Error('Data channel not open');
    }

    // Split the 2MB chunk into smaller SCTP MTU compliant sub-chunks (60KB)
    const totalSubChunks = Math.ceil(chunk.byteLength / SUB_CHUNK_SIZE);
    
    for (let subIndex = 0; subIndex < totalSubChunks; subIndex++) {
      const offset = subIndex * SUB_CHUNK_SIZE;
      const subPayload = chunk.slice(offset, Math.min(chunk.byteLength, offset + SUB_CHUNK_SIZE));
      
      const frame = encodeSubChunk(index, subIndex, totalSubChunks, subPayload);

      // Backpressure Check: Low/High watermarks
      const BUFFER_THRESHOLD = 4 * 1024 * 1024; // 4MB low water
      const MAX_BUFFER = 8 * 1024 * 1024;       // 8MB high water
      this.dataChannel.bufferedAmountLowThreshold = BUFFER_THRESHOLD;

      if (this.dataChannel.bufferedAmount > MAX_BUFFER) {
        await new Promise<void>(resolve => {
          this.dataChannel!.onbufferedamountlow = () => {
            this.dataChannel!.onbufferedamountlow = null;
            resolve();
          };
        });
      }

      this.dataChannel.send(frame);
    }

    if (this.progressCallback && this.file) {
      this.progressCallback({ bytesSent: (index + 1) * chunk.byteLength, totalBytes: this.file.size });
    }

    // If this is the last chunk, wait for all data to drain out of the channel, then signal transfer_complete
    const totalChunks = this.file ? Math.ceil(this.file.size / this.metadata!.chunkSize) : 1;
    if (index === totalChunks - 1) {
      if (this.dataChannel.bufferedAmount > 0) {
        this.dataChannel.bufferedAmountLowThreshold = 0;
        await new Promise<void>(resolve => {
          if (this.dataChannel!.bufferedAmount === 0) {
            resolve();
            return;
          }
          this.dataChannel!.onbufferedamountlow = () => {
            this.dataChannel!.onbufferedamountlow = null;
            resolve();
          };
        });
      }
      this.signaling.send({ type: 'transfer_complete' });
      this.updateStatus('completed');
    }
  }

  onChunk(callback: (chunk: ReceivedChunk) => void): void {
    this.chunkCallback = callback;
  }

  onProgress(callback: (progress: TransferProgress) => void): void {
    this.progressCallback = callback;
  }

  onError(callback: (error: TransferError) => void): void {
    this.errorCallback = callback;
  }

  onStatusChange(callback: (status: TransportStatus, activeTransport: TransportMode) => void): void {
    this.statusCallback = callback;
  }

  close(): void {
    this.updateStatus('closed');
    if (this.unsubscribeSignaling) this.unsubscribeSignaling();
    if (this.dataChannel) {
      this.dataChannel.close();
    }
    if (this.pc) {
      this.pc.close();
    }
    this.receiveAssembly.clear();
  }

  getStatus(): TransportStatus {
    return this.status;
  }

  getActiveTransport(): TransportMode {
    return 'webrtc';
  }

  getStartChunk(): number {
    return this.startChunk;
  }

  onMetadata(callback: (metadata: Metadata) => void): void {
    this.metadataCallback = callback;
  }

  accept(): void {
    this.signaling.send({ type: 'accept_transfer' });
  }

  async complete(): Promise<void> {
    logger.debug('[WebRTCTransport] complete() called');
    if (this.dataChannel && this.dataChannel.readyState === 'open') {
      logger.debug('[WebRTCTransport] Sending transfer_complete over DataChannel...');
      const controlFrame = encodeSubChunk(0, 1, 0, new ArrayBuffer(0));
      this.dataChannel.send(controlFrame);
    }
    this.signaling.send({ type: 'transfer_complete' });
    this.updateStatus('completed');
  }

  private updateStatus(status: TransportStatus) {
    this.status = status;
    if (this.statusCallback) {
      this.statusCallback(status, 'webrtc');
    }
  }

  private handleError(msg: string) {
    if (this.status === 'failed' || this.status === 'closed') return;
    this.updateStatus('failed');
    if (this.errorCallback) {
      this.errorCallback({ message: msg });
    }
  }
}
