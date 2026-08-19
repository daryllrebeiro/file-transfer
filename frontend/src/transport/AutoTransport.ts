import { TransferTransport, ReceivedChunk, TransferProgress, TransferError, TransportStatus, TransportMode } from './TransferTransport';
import { WebSocketRelayTransport } from './WebSocketRelayTransport';
import { WebRTCTransport } from './WebRTCTransport';
import { Metadata, Message } from '../types';
import { TransferClient } from '../services/transferClient';

export class AutoTransport implements TransferTransport {
  private role: 'sender' | 'receiver';
  private url: string;
  private id: string;
  private token: string;
  private file?: File;
  private metadata?: Metadata;

  private client?: TransferClient;
  private activeTransport?: TransferTransport;
  private fallbackTimer?: number;
  private status: TransportStatus = 'new';
  private currentActiveTransportMode: TransportMode = 'auto';

  private chunkCallback?: (chunk: ReceivedChunk) => void;
  private progressCallback?: (progress: TransferProgress) => void;
  private errorCallback?: (error: TransferError) => void;
  private statusCallback?: (status: TransportStatus, activeTransport: TransportMode) => void;
  private metadataCallback?: (metadata: Metadata) => void;

  private removeMessageListener?: () => void;

  constructor(role: 'sender' | 'receiver', url: string, id: string, token: string, file?: File, metadata?: Metadata) {
    this.role = role;
    this.url = url;
    this.id = id;
    this.token = token;
    this.file = file;
    this.metadata = metadata;
  }

  async connect(): Promise<void> {
    this.updateStatus('connecting', 'auto');
    
    this.client = new TransferClient(this.url, this.role, this.id, this.token);
    
    // We listen to signaling fallback messages on the client
    this.removeMessageListener = this.client.onMessage(msg => {
      console.log(`[AutoTransport] [${this.role}] Received signaling:`, msg.type);
      if (msg.type === 'webrtc_fallback') {
        console.log('[AutoTransport] Fallback signal received from peer. Triggering fallback.');
        this.triggerFallback();
      } else if (msg.type === 'transfer_accepted' && this.role === 'sender') {
        console.log('[AutoTransport] Receiver accepted. Starting handshake timeout timer.');
        const timeout = Number(import.meta.env.VITE_WEBRTC_CONNECTION_TIMEOUT || 10000);
        window.clearTimeout(this.fallbackTimer);
        this.fallbackTimer = window.setTimeout(() => {
          console.log('[AutoTransport] WebRTC connection timeout. Falling back to relay.');
          this.triggerFallback();
        }, timeout);
      }
    });

    const webrtc = new WebRTCTransport(this.role, this.client, this.id, this.file, this.metadata);
    this.activeTransport = webrtc;

    webrtc.onChunk(chunk => this.chunkCallback?.(chunk));
    webrtc.onProgress(prog => this.progressCallback?.(prog));
    webrtc.onMetadata?.(meta => this.metadataCallback?.(meta));
    
    webrtc.onStatusChange((status, mode) => {
      if (status === 'connected') {
        window.clearTimeout(this.fallbackTimer);
        this.currentActiveTransportMode = 'webrtc';
        this.updateStatus('connected', 'webrtc');
      } else if (status === 'transferring') {
        this.updateStatus('transferring', 'webrtc');
      } else if (status === 'completed') {
        this.updateStatus('completed', 'webrtc');
      } else if (status === 'failed') {
        this.triggerFallback();
      }
    });

    webrtc.onError(err => {
      console.log('[AutoTransport] WebRTC error encountered:', err.message);
      this.triggerFallback();
    });



    webrtc.connect().catch(err => {
      console.error('[AutoTransport] Failed to start WebRTC connection:', err);
      this.triggerFallback();
    });
  }

  private triggerFallback() {
    window.clearTimeout(this.fallbackTimer);
    if (this.currentActiveTransportMode === 'relay') return;
    
    console.log('[AutoTransport] Performing active switch to Server Relay');
    this.currentActiveTransportMode = 'relay';
    
    // Inform peer if we are sender
    if (this.role === 'sender' && this.client) {
      this.client.send({ type: 'webrtc_fallback' });
    }

    if (this.activeTransport) {
      this.activeTransport.close();
    }

    const relay = new WebSocketRelayTransport(this.role, this.url, this.id, this.token, this.file, this.metadata, this.client);
    this.activeTransport = relay;

    relay.onChunk(chunk => this.chunkCallback?.(chunk));
    relay.onProgress(prog => this.progressCallback?.(prog));
    relay.onMetadata?.(meta => this.metadataCallback?.(meta));
    relay.onStatusChange((status, mode) => {
      this.updateStatus(status, 'relay');
    });
    relay.onError(err => this.errorCallback?.(err));

    relay.connect().catch(err => {
      this.handleError(err.message || 'Relay connection failed');
    });
  }

  async sendMetadata(metadata: Metadata): Promise<void> {
    await this.activeTransport?.sendMetadata(metadata);
  }

  async sendChunk(chunk: ArrayBuffer, index: number): Promise<void> {
    await this.activeTransport?.sendChunk(chunk, index);
  }

  onChunk(callback: (chunk: ReceivedChunk) => void): void {
    this.chunkCallback = callback;
    this.activeTransport?.onChunk(callback);
  }

  onProgress(callback: (progress: TransferProgress) => void): void {
    this.progressCallback = callback;
    this.activeTransport?.onProgress(callback);
  }

  onError(callback: (error: TransferError) => void): void {
    this.errorCallback = callback;
    this.activeTransport?.onError(callback);
  }

  onStatusChange(callback: (status: TransportStatus, activeTransport: TransportMode) => void): void {
    this.statusCallback = callback;
  }

  close(): void {
    window.clearTimeout(this.fallbackTimer);
    if (this.removeMessageListener) this.removeMessageListener();
    if (this.activeTransport) {
      this.activeTransport.close();
    }
    if (this.client) {
      this.client.close();
    }
  }

  getStatus(): TransportStatus {
    return this.status;
  }

  getActiveTransport(): TransportMode {
    return this.currentActiveTransportMode;
  }

  getStartChunk(): number {
    return this.activeTransport?.getStartChunk?.() || 0;
  }

  onMetadata(callback: (metadata: Metadata) => void): void {
    this.metadataCallback = callback;
    this.activeTransport?.onMetadata?.(callback);
  }

  accept(): void {
    this.activeTransport?.accept?.();
  }

  async complete(): Promise<void> {
    console.log('[AutoTransport] complete() called');
    await this.activeTransport?.complete?.();
    this.updateStatus('completed', this.currentActiveTransportMode);
  }

  private updateStatus(status: TransportStatus, mode: TransportMode) {
    this.status = status;
    if (this.statusCallback) {
      this.statusCallback(status, mode);
    }
  }

  private handleError(msg: string) {
    this.updateStatus('failed', this.currentActiveTransportMode);
    if (this.errorCallback) {
      this.errorCallback({ message: msg });
    }
  }
}
