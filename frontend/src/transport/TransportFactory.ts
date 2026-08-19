import { TransferTransport, TransportMode } from './TransferTransport';
import { WebSocketRelayTransport } from './WebSocketRelayTransport';
import { WebRTCTransport } from './WebRTCTransport';
import { AutoTransport } from './AutoTransport';
import { Metadata } from '../types';
import { TransferClient } from '../services/transferClient';

export interface TransportOptions {
  role: 'sender' | 'receiver';
  url: string;
  id: string;
  token: string;
  file?: File;
  metadata?: Metadata;
}

export function createTransferTransport(
  mode: TransportMode,
  options: TransportOptions
): TransferTransport {
  switch (mode) {
    case 'webrtc': {
      const client = new TransferClient(options.url, options.role, options.id, options.token);
      return new WebRTCTransport(options.role, client, options.id, options.file, options.metadata);
    }
    case 'relay':
      return new WebSocketRelayTransport(options.role, options.url, options.id, options.token, options.file, options.metadata);
    case 'auto':
    default:
      return new AutoTransport(options.role, options.url, options.id, options.token, options.file, options.metadata);
  }
}
export { type TransportMode, type TransportStatus } from './TransferTransport';
