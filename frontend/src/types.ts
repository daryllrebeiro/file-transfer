export type EncryptionMetadata = { scheme: 'aes-gcm-pbkdf2'; salt: string; iterations: number; nonce: string };

export type FileEntry = { 
  name: string; 
  size: number; 
  mimeType: string; 
  sha256?: string; 
  sha256Ciphertext?: string;
  offset?: number; // for manifest
};

export type Metadata = { 
  fileName?: string; // deprecated, kept for backward compat
  fileSize?: number; // deprecated
  mimeType?: string; // deprecated
  chunkSize: number; 
  sha256?: string; // deprecated
  transport?: string; 
  encryption?: EncryptionMetadata; 
  sha256Ciphertext?: string; // deprecated
  files?: FileEntry[]; // new: multi-file support
  totalSize?: number; // new: total size of all files
  manifestSha256?: string; // SHA-256 of manifest
  manifestSha256Ciphertext?: string; // encrypted manifest hash
};

export type Message = { type: string; transferId?: string; metadata?: Metadata; chunkIndex?: number; message?: string; token?: string; nextChunk?: number; sdp?: string; candidate?: any; sdpMid?: string; sdpMLineIndex?: number; chunkSize?: number; features?: string[] };