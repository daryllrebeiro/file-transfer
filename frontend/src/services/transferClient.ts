import type { Message, Metadata } from '../types';
const headerSize = 16;
export const frameChunk = (index: number, bytes: ArrayBuffer) => { const frame = new ArrayBuffer(headerSize + bytes.byteLength); const view = new DataView(frame); view.setBigUint64(0, BigInt(index)); view.setBigUint64(8, BigInt(bytes.byteLength)); new Uint8Array(frame, headerSize).set(new Uint8Array(bytes)); return frame; };
export const parseChunk = (data: ArrayBuffer) => { if (data.byteLength < headerSize) throw new Error('Invalid chunk received'); const view = new DataView(data); const length = Number(view.getBigUint64(8)); if (length !== data.byteLength - headerSize) throw new Error('Invalid chunk received'); return { index: Number(view.getBigUint64(0)), bytes: data.slice(headerSize) }; };
export class TransferClient { socket: WebSocket; private handlers = new Set<(message: Message) => void>(); private binaryHandlers = new Set<(data: ArrayBuffer) => void>(); constructor(url: string, role: 'sender' | 'receiver', id: string, token: string) { this.socket = new WebSocket(url); this.socket.binaryType = 'arraybuffer';     this.socket.onmessage = event => { if (typeof event.data === 'string') this.handlers.forEach(handler => handler(JSON.parse(event.data))); else this.binaryHandlers.forEach(handler => handler(event.data as ArrayBuffer)); }; this.socket.onopen = () => this.send({ type: `${role}_join`, transferId: id, token }); } onMessage(handler: (message: Message) => void) { this.handlers.add(handler); return () => this.handlers.delete(handler); } onBinary(handler: (data: ArrayBuffer) => void) { this.binaryHandlers.add(handler); return () => this.binaryHandlers.delete(handler); } send(message: Message) { if (this.socket.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(message)); else this.socket.addEventListener('open', () => this.socket.send(JSON.stringify(message)), { once: true }); } close() { this.socket.close(); } }
const ackTimeout = 15_000;
const maxAttempts = 3;

function waitForAck(client: TransferClient, index: number, signal: AbortSignal): Promise<void> {
	return new Promise((resolve, reject) => {
		let timer: number | undefined;
		const cleanup = () => { if (timer !== undefined) window.clearTimeout(timer); unsubscribe(); signal.removeEventListener('abort', abort); };
		const unsubscribe = client.onMessage(message => { if (message.type === 'chunk_ack' && message.chunkIndex === index) { cleanup(); resolve(); } if (message.type === 'transfer_cancelled' || message.type === 'error') { cleanup(); reject(new Error(message.message || 'Transfer failed')); } });
		const abort = () => { cleanup(); reject(new Error('Transfer cancelled')); };
		timer = window.setTimeout(() => { cleanup(); reject(new Error(`Timed out waiting for chunk ${index}`)); }, ackTimeout);
		signal.addEventListener('abort', abort, { once: true });
	});
}

export async function sendFile(client: TransferClient, file: File, metadata: Metadata, onProgress: (bytes: number) => void, signal: AbortSignal, startIndex = 0) {
	const windowSize = 4; let next = startIndex; let acknowledged = startIndex;
	const pending = new Map<number, Promise<void>>(); const frames = new Map<number, ArrayBuffer>();
	while (acknowledged * metadata.chunkSize < file.size && !signal.aborted) {
		while (next < Math.ceil(file.size / metadata.chunkSize) && next - acknowledged < windowSize) {
			const index = next++; const chunk = await file.slice(index * metadata.chunkSize, Math.min(file.size, (index + 1) * metadata.chunkSize)).arrayBuffer(); const frame = frameChunk(index, chunk); frames.set(index, frame); pending.set(index, waitForAck(client, index, signal)); client.socket.send(frame);
		}
		let attempt = 1;
		while (attempt <= maxAttempts) {
			try { await pending.get(acknowledged); break; } catch (error) { if (attempt === maxAttempts || signal.aborted) throw error; attempt++; pending.set(acknowledged, waitForAck(client, acknowledged, signal)); client.socket.send(frames.get(acknowledged)!); }
		}
		pending.delete(acknowledged); frames.delete(acknowledged); acknowledged++; onProgress(Math.min(file.size, acknowledged * metadata.chunkSize));
	}
	if (!signal.aborted) client.send({ type: 'transfer_complete' });
}