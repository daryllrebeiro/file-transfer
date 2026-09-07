export interface PersistedTransfer {
  id: string;
  role: 'sender' | 'receiver';
  url: string;
  token: string;
  fileName: string;
  fileSize: number;
  mimeType: string;
  chunkSize: number;
  sha256?: string;
  sha256Ciphertext?: string;
  encryption?: EncryptionMetadata;
  password?: string;
  transport: string;
  expiresAt: string;
  createdAt: number;
  startChunk: number;
  receivedBytes: number;
  expectedChunk: number;
  isEncrypted: boolean;
}

export interface EncryptionMetadata {
  scheme: 'aes-gcm-pbkdf2';
  salt: string;
  iterations: number;
  nonce: string;
}

const DB_NAME = 'relay-transfers';
const DB_VERSION = 1;
const STORE_NAME = 'transfers';

let dbPromise: Promise<IDBDatabase> | null = null;

function openDB(): Promise<IDBDatabase> {
  if (dbPromise) return dbPromise;
  
  dbPromise = new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, DB_VERSION);
    
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
    
    request.onupgradeneeded = (event) => {
      const db = (event.target as IDBOpenDBRequest).result;
      if (!db.objectStoreNames.contains(STORE_NAME)) {
        const store = db.createObjectStore(STORE_NAME, { keyPath: 'id' });
        store.createIndex('expiresAt', 'expiresAt', { unique: false });
        store.createIndex('role', 'role', { unique: false });
      }
    };
  });
  
  return dbPromise;
}

export async function saveTransfer(transfer: PersistedTransfer): Promise<void> {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(STORE_NAME, 'readwrite');
    const store = transaction.objectStore(STORE_NAME);
    const request = store.put(transfer);
    request.onsuccess = () => resolve();
    request.onerror = () => reject(request.error);
  });
}

export async function getTransfer(id: string): Promise<PersistedTransfer | undefined> {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(STORE_NAME, 'readonly');
    const store = transaction.objectStore(STORE_NAME);
    const request = store.get(id);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

export async function getAllTransfers(): Promise<PersistedTransfer[]> {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(STORE_NAME, 'readonly');
    const store = transaction.objectStore(STORE_NAME);
    const request = store.getAll();
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

export async function deleteTransfer(id: string): Promise<void> {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(STORE_NAME, 'readwrite');
    const store = transaction.objectStore(STORE_NAME);
    const request = store.delete(id);
    request.onsuccess = () => resolve();
    request.onerror = () => reject(request.error);
  });
}

export async function cleanupExpiredTransfers(): Promise<number> {
  const db = await openDB();
  const now = Date.now();
  
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(STORE_NAME, 'readwrite');
    const store = transaction.objectStore(STORE_NAME);
    const index = store.index('expiresAt');
    const range = IDBKeyRange.upperBound(now);
    const request = index.openCursor(range);
    
    let deleted = 0;
    request.onsuccess = (event) => {
      const cursor = (event.target as IDBRequest).result;
      if (cursor) {
        cursor.delete();
        deleted++;
        cursor.continue();
      } else {
        resolve(deleted);
      }
    };
    request.onerror = () => reject(request.error);
  });
}

export async function getFileHandle(id: string): Promise<FileSystemFileHandle | null> {
  try {
    // Check if we have a stored file handle
    const handle = await (window as any).localStorage.getItem(`file-handle-${id}`);
    if (handle) {
      // Note: FileSystemFileHandle cannot be serialized directly
      // In practice, we'd need to use the File System Access API differently
      // This is a placeholder for the actual implementation
      return null;
    }
  } catch (e) {
    // File System Access API not available
  }
  return null;
}

export function generateManifest(files: File[]): string {
  // Generate a simple manifest for multi-file transfers
  return JSON.stringify(files.map(f => ({ name: f.name, size: f.size, type: f.type })));
}

export async function verifyManifestIntegrity(manifest: string, expectedSha256: string): Promise<boolean> {
  const encoder = new TextEncoder();
  const data = encoder.encode(manifest);
  const hashBuffer = await crypto.subtle.digest('SHA-256', data);
  const hashArray = Array.from(new Uint8Array(hashBuffer));
  const hashHex = hashArray.map(b => b.toString(16).padStart(2, '0')).join('');
  return hashHex === expectedSha256;
}