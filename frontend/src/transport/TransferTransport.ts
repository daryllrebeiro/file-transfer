import { Metadata } from '../types';

export type TransportStatus = 'new' | 'connecting' | 'connected' | 'transferring' | 'completed' | 'failed' | 'closed';
export type TransportMode = 'auto' | 'webrtc' | 'relay';

export interface ReceivedChunk {
  index: number;
  bytes: ArrayBuffer;
}

export interface TransferProgress {
  bytesSent: number;
  totalBytes: number;
}

export interface TransferError {
  message: string;
}

export interface TransferTransport {
  connect(): Promise<void>;
  sendMetadata(metadata: Metadata): Promise<void>;
  sendChunk(chunk: ArrayBuffer, index: number): Promise<void>;
  onChunk(callback: (chunk: ReceivedChunk) => void): void;
  onProgress(callback: (progress: TransferProgress) => void): void;
  onError(callback: (error: TransferError) => void): void;
  onStatusChange(callback: (status: TransportStatus, activeTransport: TransportMode) => void): void;
  onMetadata?(callback: (metadata: Metadata) => void): void;
  close(): void;
  getStatus(): TransportStatus;
  getActiveTransport(): TransportMode;
  getStartChunk?(): number;
  getChunkSize?(): number | undefined;
  accept?(): void;
  complete?(): Promise<void>;
  pause?(): void;
  rewind?(fromChunk: number): void;
}
