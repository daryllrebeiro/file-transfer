import { TransferTransport, ReceivedChunk, TransferProgress, TransferError, TransportStatus, TransportMode } from './TransferTransport';
import { Metadata, Message } from '../types';
import { TransferClient, parseChunk, frameChunk } from '../services/transferClient';
import { logger } from '../services/logger';

const defaultChunkSize = 2 * 1024 * 1024;
const ADAPTIVE_MIN_CHUNK = 256 * 1024;
const ADAPTIVE_GROWTH_AFTER_ACKS = 4;
const ADAPTIVE_GROW = 1.25;
const ADAPTIVE_SHRINK = 0.5;
const WINDOW_MIN = 2;
const WINDOW_MAX = 16;
const WINDOW_START = 4;

export class WebSocketRelayTransport implements TransferTransport {
  private role: 'sender' | 'receiver';
  private url: string;
  private id: string;
  private token: string;
  private file?: File;
  private metadata?: Metadata;
  private client?: TransferClient;
  private status: TransportStatus = 'new';
  private ownClient = false;
  
  private chunkCallback?: (chunk: ReceivedChunk) => void;
  private progressCallback?: (progress: TransferProgress) => void;
  private errorCallback?: (error: TransferError) => void;
  private statusCallback?: (status: TransportStatus, activeTransport: TransportMode) => void;
  private metadataCallback?: (metadata: Metadata) => void;

  private removeMessageListener?: () => void;
  private removeBinaryListener?: () => void;

  private pendingAcks = new Map<number, { resolve: () => void; reject: (err: Error) => void }>();
  private acknowledged = 0;
  private windowSize = WINDOW_START;
  private windowAckCount = 0;
  private maxAttempts = 3;
  private frames = new Map<number, ArrayBuffer>();
  private timeouts = new Map<number, number>();
  private sentTimes = new Map<number, number>();
  private bytesSent = 0;
  private currentChunkSize: number;
  private maxChunkSize: number;
  private acksSinceResize = 0;

  constructor(role: 'sender' | 'receiver', url: string, id: string, token: string, file?: File, metadata?: Metadata, client?: TransferClient) {
    this.role = role;
    this.url = url;
    this.id = id;
    this.token = token;
    this.file = file;
    this.metadata = metadata;
    this.client = client;
    this.currentChunkSize = metadata?.chunkSize || defaultChunkSize;
    this.maxChunkSize = this.currentChunkSize;
  }

  private announceSize(target: number) {
    this.currentChunkSize = target;
    this.acksSinceResize = 0;
    if (this.client && this.client.socket.readyState === WebSocket.OPEN) {
      this.client.socket.send(JSON.stringify({ type: 'chunk_size_change', chunkSize: this.currentChunkSize }));
      logger.debug(`[WebSocketRelayTransport] Adaptive chunk size -> ${this.currentChunkSize}`);
    }
  }

  private adjustSize(next: number) {
    const clamped = Math.max(ADAPTIVE_MIN_CHUNK, Math.min(Math.round(next), this.maxChunkSize));
    if (clamped === this.currentChunkSize) {
      this.acksSinceResize = 0;
      return;
    }
    this.announceSize(clamped);
  }

  private onChunkAcknowledged(index: number) {
    this.acksSinceResize++;
    this.windowAckCount++;
    if (this.acksSinceResize >= ADAPTIVE_GROWTH_AFTER_ACKS && this.currentChunkSize < this.maxChunkSize) {
      this.adjustSize(this.currentChunkSize * ADAPTIVE_GROW);
    }
    // Additive window increase: after a full clean window has been acknowledged,
    // widen the window by one (capped at the server's parallel admission window).
    if (this.windowAckCount >= this.windowSize && this.windowSize < WINDOW_MAX) {
      this.windowSize = Math.min(WINDOW_MAX, this.windowSize + 1);
      this.windowAckCount = 0;
      logger.debug(`[WebSocketRelayTransport] Adaptive window -> ${this.windowSize}`);
    }
  }

  private onChunkRetry() {
    if (this.currentChunkSize > ADAPTIVE_MIN_CHUNK) {
      this.adjustSize(this.currentChunkSize * ADAPTIVE_SHRINK);
    }
    // Multiplicative window decrease on loss: halve the in-flight window.
    if (this.windowSize > WINDOW_MIN) {
      this.windowSize = Math.max(WINDOW_MIN, Math.floor(this.windowSize / 2));
    }
    this.windowAckCount = 0;
  }

  async connect(): Promise<void> {
    this.updateStatus('connecting');
    if (!this.client) {
      this.client = new TransferClient(this.url, this.role, this.id, this.token);
      this.ownClient = true;
    }

    this.removeMessageListener = this.client.onMessage(msg => this.handleMessage(msg));
    
    if (this.role === 'receiver') {
      this.removeBinaryListener = this.client.onBinary(buf => this.handleBinary(buf));
    }
  }

  private startChunk = 0;
  private receiveBuffer = new Map<number, ReceivedChunk>();
  private receiveFloor = 0;
  private readonly reorderWindow = 32;

  private handleBinary(buf: ArrayBuffer) {
    let chunk: { index: number; bytes: ArrayBuffer };
    try {
      chunk = parseChunk(buf);
    } catch {
      this.handleError('Invalid chunk received.');
      return;
    }
    if (chunk.index < this.receiveFloor) {
      return; // duplicate already emitted
    }
    if (chunk.index === this.receiveFloor) {
      this.emitChunk(chunk);
      return;
    }
    if (chunk.index < this.receiveFloor + this.reorderWindow && !this.receiveBuffer.has(chunk.index)) {
      this.receiveBuffer.set(chunk.index, chunk);
    }
  }

  private emitChunk(chunk: ReceivedChunk) {
    if (!this.chunkCallback) return;
    this.chunkCallback(chunk);
    this.receiveFloor = chunk.index + 1;
    let next = this.receiveBuffer.get(this.receiveFloor);
    while (next) {
      this.receiveBuffer.delete(this.receiveFloor);
      this.chunkCallback(next);
      this.receiveFloor = next.index + 1;
      next = this.receiveBuffer.get(this.receiveFloor);
    }
  }

  private handleMessage(msg: Message) {
    if (msg.type === 'error') {
      this.handleError(msg.message || 'Connection error.');
    } else if (msg.type === 'transfer_offer') {
      this.startChunk = msg.nextChunk || 0;
      if (this.role === 'receiver') {
        this.receiveFloor = msg.nextChunk || 0;
        this.receiveBuffer.clear();
      }
      if (msg.metadata) {
        this.metadataCallback?.(msg.metadata);
      }
      this.updateStatus('connected');
    } else if (msg.type === 'transfer_accepted') {
      this.updateStatus('transferring');
    } else if (msg.type === 'chunk_ack') {
      const idx = msg.chunkIndex ?? 0;
      const pending = this.pendingAcks.get(idx);
      if (pending) {
        window.clearTimeout(this.timeouts.get(idx));
        this.timeouts.delete(idx);
        this.sentTimes.delete(idx);
        pending.resolve();
        this.pendingAcks.delete(idx);
        this.frames.delete(idx);
        if (idx === this.acknowledged) {
          this.acknowledged = idx + 1;
        }
        this.onChunkAcknowledged(idx);
      }
    } else if (msg.type === 'sender_disconnected' || msg.type === 'receiver_disconnected') {
      this.handleError('The other device disconnected.');
    } else if (msg.type === 'transfer_complete') {
      this.updateStatus('completed');
    } else if (msg.type === 'transfer_cancelled') {
      this.handleError('Transfer cancelled.');
    }
  }

  async sendMetadata(metadata: Metadata): Promise<void> {
    this.metadata = metadata;
  }

  async sendChunk(chunk: ArrayBuffer, index: number): Promise<void> {
    if (this.status === 'failed' || this.status === 'closed') {
      throw new Error('Transport not connected');
    }

    if (!this.client) {
      throw new Error('Client not initialized');
    }

    // Sliding window backpressure: block if we are too far ahead of acknowledged
    while (index - this.acknowledged >= this.windowSize) {
      await new Promise<void>(resolve => {
        const check = () => {
          if (index - this.acknowledged < this.windowSize || (this.status as string) === 'failed' || (this.status as string) === 'closed') {
            resolve();
          } else {
            window.setTimeout(check, 50);
          }
        };
        check();
      });
      if ((this.status as string) === 'failed' || (this.status as string) === 'closed') {
        throw new Error('Transport failed or closed during send');
      }
    }

    const frame = frameChunk(index, chunk);
    this.frames.set(index, frame);
    this.sentTimes.set(index, performance.now());
    this.client.socket.send(frame);

    const ackPromise = new Promise<void>((resolve, reject) => {
      this.pendingAcks.set(index, { resolve, reject });
    });

    this.setupTimeout(index);

    this.bytesSent += chunk.byteLength;
    if (this.progressCallback && this.file) {
      this.progressCallback({ bytesSent: Math.min(this.bytesSent, this.file.size), totalBytes: this.file.size });
    }
  }

  private setupTimeout(index: number) {
    let attempt = 1;
    const runTimeout = () => {
      const timer = window.setTimeout(() => {
        const pending = this.pendingAcks.get(index);
        if (pending) {
          if (attempt >= this.maxAttempts) {
            pending.reject(new Error(`Timed out waiting for chunk ${index} after 3 attempts`));
            this.handleError(`Timed out waiting for chunk ${index}`);
          } else {
            attempt++;
            logger.warn(`[WebSocketRelayTransport] Resending chunk ${index}, attempt ${attempt}`);
            this.onChunkRetry();
            const frame = this.frames.get(index);
            if (frame && this.client && this.client.socket.readyState === WebSocket.OPEN) {
              this.client.socket.send(frame);
              runTimeout();
            }
          }
        }
      }, 15000);
      this.timeouts.set(index, timer);
    };
    runTimeout();
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

  onMetadata(callback: (metadata: Metadata) => void): void {
    this.metadataCallback = callback;
  }

  close(): void {
    this.updateStatus('closed');
    for (const timer of this.timeouts.values()) {
      window.clearTimeout(timer);
    }
    this.timeouts.clear();
    if (this.removeMessageListener) this.removeMessageListener();
    if (this.removeBinaryListener) this.removeBinaryListener();
    if (this.ownClient && this.client) {
      this.client.close();
    }
    for (const pending of this.pendingAcks.values()) {
      pending.reject(new Error('Transport closed'));
    }
    this.pendingAcks.clear();
    this.frames.clear();
    this.sentTimes.clear();
  }

  getStatus(): TransportStatus {
    return this.status;
  }

  getActiveTransport(): TransportMode {
    return 'relay';
  }

  getStartChunk(): number {
    return this.startChunk;
  }

  getChunkSize(): number {
    return this.currentChunkSize;
  }

  accept(): void {
    this.client?.send({ type: 'accept_transfer' });
  }

  async complete(): Promise<void> {
    await new Promise<void>((resolve, reject) => {
      const checkDone = () => {
        if (this.pendingAcks.size === 0) {
          resolve();
        } else if (this.status === 'failed' || this.status === 'closed') {
          reject(new Error('Transport failed before completion'));
        } else {
          window.setTimeout(checkDone, 50);
        }
      };
      checkDone();
    });
    this.client?.send({ type: 'transfer_complete' });
    this.updateStatus('completed');
  }

  pause(): void {
    this.client?.send({ type: 'pause' });
  }

  rewind(fromChunk: number): void {
    this.client?.send({ type: 'rewind', nextChunk: fromChunk });
  }

  private updateStatus(status: TransportStatus) {
    this.status = status;
    if (this.statusCallback) {
      this.statusCallback(status, 'relay');
    }
  }

  private handleError(msg: string) {
    this.updateStatus('failed');
    if (this.errorCallback) {
      this.errorCallback({ message: msg });
    }
  }
}
