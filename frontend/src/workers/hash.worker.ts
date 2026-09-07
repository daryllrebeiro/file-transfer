import { sha256 } from '@noble/hashes/sha2.js';

type HashMessage = {
  type: 'hash';
  file: File;
  chunkSize: number;
  id: number;
};

type HashResultMessage = {
  type: 'hash-result';
  id: number;
  hash: string;
  error?: string;
};

let messageId = 0;
const pending = new Map<number, { resolve: (hash: string) => void; reject: (err: Error) => void }>();

self.onmessage = async (event: MessageEvent<HashMessage>) => {
  const { type, file, chunkSize, id } = event.data;
  
  if (type !== 'hash') return;
  
  try {
    const hash = sha256.create();
    for (let offset = 0; offset < file.size; offset += chunkSize) {
      const slice = file.slice(offset, Math.min(offset + chunkSize, file.size));
      const buffer = await slice.arrayBuffer();
      hash.update(new Uint8Array(buffer));
    }
    const digest = Array.from(hash.digest() as Uint8Array, byte => byte.toString(16).padStart(2, '0')).join('');
    (self as unknown as Worker).postMessage({ type: 'hash-result', id, hash } as HashResultMessage);
  } catch (err) {
    (self as unknown as Worker).postMessage({ type: 'hash-result', id, hash: '', error: err instanceof Error ? err.message : 'Hash failed' } as HashResultMessage);
  }
};

export {};