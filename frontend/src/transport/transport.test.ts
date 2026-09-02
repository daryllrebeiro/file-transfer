import { describe, expect, it, vi, beforeEach } from 'vitest';
import { WebSocketRelayTransport } from './WebSocketRelayTransport';
import { AutoTransport } from './AutoTransport';
import type { TransferTransport } from './TransferTransport';

const sendCalls: any[] = [];
let messageHandler: ((msg: any) => void) | null = null;
let binaryHandler: ((data: ArrayBuffer) => void) | null = null;

const createMockClient = () => ({
  socket: {
    send: vi.fn((data: any) => {
      sendCalls.push(data);
      if (typeof data === 'string') {
        const msg = JSON.parse(data);
        if (messageHandler) messageHandler(msg);
      }
    }),
    close: vi.fn(),
    readyState: 1,
  },
  onMessage: (handler: (msg: any) => void) => {
    messageHandler = handler;
    return () => { messageHandler = null; };
  },
  onBinary: (handler: (data: ArrayBuffer) => void) => {
    binaryHandler = handler;
    return () => { binaryHandler = null; };
  },
  send: vi.fn((msg: any) => {
    sendCalls.push(msg);
    if (messageHandler) messageHandler(msg);
  }),
  close: vi.fn(),
});

beforeEach(() => {
  sendCalls.length = 0;
  messageHandler = null;
  binaryHandler = null;
});

describe('WebSocketRelayTransport', () => {
  it('processes transfer_offer and updates status to connected', async () => {
    const client: any = createMockClient();
    const transport = new WebSocketRelayTransport('receiver', 'ws://localhost:8080/ws/test', 'test-id', 'token', undefined, undefined, client);
    const statuses: string[] = [];
    transport.onStatusChange((status) => statuses.push(status));
    await transport.connect();
    if (messageHandler) messageHandler({ type: 'transfer_offer', nextChunk: 0, metadata: { fileName: 'a.txt', fileSize: 10, mimeType: 'text/plain', chunkSize: 5 } });
    expect(statuses).toContain('connected');
    transport.close();
  });

  it('processes binary chunk on receiver', async () => {
    const client: any = createMockClient();
    const transport = new WebSocketRelayTransport('receiver', 'ws://localhost:8080/ws/test', 'test-id', 'token', undefined, undefined, client);
    const chunks: any[] = [];
    transport.onChunk((chunk) => chunks.push(chunk));
    await transport.connect();
    const payload = new Uint8Array([1, 2, 3, 4, 5]);
    const frame = new ArrayBuffer(16 + payload.byteLength);
    const view = new DataView(frame);
    view.setBigUint64(0, BigInt(0));
    view.setBigUint64(8, BigInt(payload.byteLength));
    new Uint8Array(frame, 16).set(payload);
    if (binaryHandler) binaryHandler(frame);
    expect(chunks).toHaveLength(1);
    expect(chunks[0].index).toBe(0);
    expect(new Uint8Array(chunks[0].bytes)).toEqual(payload);
    transport.close();
  });

  it('sends accept_transfer', async () => {
    const client: any = createMockClient();
    const transport = new WebSocketRelayTransport('receiver', 'ws://localhost:8080/ws/test', 'test-id', 'token', undefined, undefined, client);
    await transport.connect();
    transport.accept();
    expect(client.send).toHaveBeenCalledWith({ type: 'accept_transfer' });
    transport.close();
  });

  it('completes transfer', async () => {
    const client: any = createMockClient();
    const transport = new WebSocketRelayTransport('sender', 'ws://localhost:8080/ws/test', 'test-id', 'token', new File(['hello'], 'a.txt', { type: 'text/plain' }), { fileName: 'a.txt', fileSize: 5, mimeType: 'text/plain', chunkSize: 5 }, client);
    const statuses: string[] = [];
    transport.onStatusChange((status) => statuses.push(status));
    await transport.connect();
    await transport.complete();
    expect(statuses).toContain('completed');
    expect(client.send).toHaveBeenCalledWith({ type: 'transfer_complete' });
    transport.close();
  });

  it('handles transfer_cancelled error', async () => {
    const client: any = createMockClient();
    const transport = new WebSocketRelayTransport('sender', 'ws://localhost:8080/ws/test', 'test-id', 'token', undefined, undefined, client);
    const errors: any[] = [];
    transport.onError((err) => errors.push(err));
    await transport.connect();
    if (messageHandler) messageHandler({ type: 'transfer_cancelled' });
    expect(errors).toHaveLength(1);
    expect(errors[0].message).toBe('Transfer cancelled.');
    transport.close();
  });
});

describe('AutoTransport', () => {
  it('falls back to relay when webrtc_fallback received', async () => {
    const client: any = createMockClient();
    const transport = new AutoTransport('sender', 'ws://localhost:8080/ws/test', 'test-id', 'token', undefined, undefined);
    const modes: string[] = [];
    transport.onStatusChange((_, mode) => modes.push(mode));
    transport.onError(() => {});
    await transport.connect();
    expect(modes).toContain('auto');
    if (messageHandler) messageHandler({ type: 'webrtc_fallback' });
    if (messageHandler) messageHandler({ type: 'transfer_accepted' });
    expect(modes).toContain('relay');
    transport.close();
  });
});
