import { sha256 } from '@noble/hashes/sha2.js';

export type IncrementalHash = ReturnType<typeof sha256.create>;

export function createHash(): IncrementalHash { return sha256.create(); }
export function digestHex(hash: IncrementalHash): string { return Array.from(hash.digest() as Uint8Array, byte => byte.toString(16).padStart(2, '0')).join(''); }
export async function hashFile(file: File, chunkSize: number): Promise<string> {
  return hashFileSync(file, chunkSize);
}
export async function hashFileSync(file: File, chunkSize: number): Promise<string> {
  const hash = createHash();
  for (let offset = 0; offset < file.size; offset += chunkSize) {
    hash.update(new Uint8Array(await file.slice(offset, Math.min(offset + chunkSize, file.size)).arrayBuffer()));
  }
  return digestHex(hash);
}