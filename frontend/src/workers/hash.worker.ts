import { sha256 as createHashSync } from '@noble/hashes/sha2';

self.onmessage = async (event: MessageEvent) => {
  const { type, payload } = event.data;
  
  if (type === 'hash-file') {
    const { fileData, chunkSize } = payload;
    const hash = createHashSync();
    let offset = 0;
    
    while (offset < fileData.byteLength) {
      const chunk = fileData.slice(offset, Math.min(fileData.byteLength, offset + chunkSize));
      hash.update(new Uint8Array(chunk));
      offset += chunk.byteLength;
    }
    
    const digest = new Uint8Array(hash.digest());
    const hex = Array.from(digest).map(b => b.toString(16).padStart(2, '0')).join('');
    self.postMessage({ type: 'hash-result', digest: hex });
  } else if (type === 'hash-incremental') {
    const { chunks } = payload;
    const hash = createHashSync();
    
    for (const chunk of chunks) {
      hash.update(new Uint8Array(chunk));
    }
    
    const digest = new Uint8Array(hash.digest());
    const hex = Array.from(digest).map(b => b.toString(16).padStart(2, '0')).join('');
    self.postMessage({ type: 'hash-result', digest: hex });
  }
};