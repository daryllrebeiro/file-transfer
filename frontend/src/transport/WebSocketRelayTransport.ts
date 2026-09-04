import { TransferTransport, ReceivedChunk, TransferProgress, TransferError, TransportStatus, TransportMode } from './TransferTransport';
import { Metadata, Message } from '../types';
import { TransferClient, parseChunk, frameChunk } from '../services/transferClient';
import { logger } from '../services/logger';

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
  private windowSize = 4;
  private maxAttempts = 3;
  private frames = new Map<number, ArrayBuffer>();
  private timeouts = new Map<number, number>();

  constructor(role: 'sender' | 'receiver', url: string, id: string, token: string, file?: File, metadata?: Metadata, client?: TransferClient) {
    this.role = role;
    this.url = url;
    this.id = id;
    this.token = token;
    this.file = file;
    this.metadata = metadata;
    this.client = client;
  }

  async connect(): Promise<void> {
    this.updateStatus('connecting');
    if (!this.client) {
      this.client = new TransferClient(this.url, this.role, this.id, this.token);
      this.ownClient = true;
    }

    this.removeMessageListener = this.client.onMessage(msg => this.handleMessage(msg));
    
    if (this.role === 'receiver') {
      this.removeBinaryListener = this.client.onBinary(buf => {
        try {
          const chunk = parseChunk(buf);
          if (this.chunkCallback) {
            this.chunkCallback({ index: chunk.index, bytes: chunk.bytes });
          }
        } catch (e) {
          this.handleError('Invalid chunk received.');
        }
      });
    }
  }

  private startChunk = 0;

  private handleMessage(msg: Message) {
    if (msg.type === 'error') {
      this.handleError(msg.message || 'Connection error.');
    } else if (msg.type === 'transfer_offer') {
      this.startChunk = msg.nextChunk || 0;
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
        pending.resolve();
        this.pendingAcks.delete(idx);
        this.frames.delete(idx);
        if (idx === this.acknowledged) {
          this.acknowledged = idx + 1;
        }
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
    this.client.socket.send(frame);

    const ackPromise = new Promise<void>((resolve, reject) => {
      this.pendingAcks.set(index, { resolve, reject });
    });

    this.setupTimeout(index);

    if (this.progressCallback && this.file) {
      this.progressCallback({ bytesSent: (index + 1) * chunk.byteLength, totalBytes: this.file.size });
    }

    // If this is the last chunk, wait for all pending ACKs to clear, then send transfer_complete
    const totalChunks = this.file ? Math.ceil(this.file.size / this.metadata!.chunkSize) : 1;
    if (index === totalChunks - 1) {
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
      this.client.send({ type: 'transfer_complete' });
      this.updateStatus('completed');
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

  accept(): void {
    this.client?.send({ type: 'accept_transfer' });
  }

  async complete(): Promise<void> {
    this.client?.send({ type: 'transfer_complete' });
    this.updateStatus('completed');
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
