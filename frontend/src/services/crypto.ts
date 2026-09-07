const subtle = window.crypto.subtle;
const getRandomValues = window.crypto.getRandomValues.bind(window.crypto);

export interface EncryptionMetadata {
  scheme: 'aes-gcm-pbkdf2';
  salt: string;
  iterations: number;
  nonce: string;
}

export interface CryptoKeys {
  key: CryptoKey;
  nonce: Uint8Array;
}

const PBKDF2_ITERATIONS = 250000;
const SALT_LENGTH = 16;
const NONCE_LENGTH = 32;
const IV_LENGTH = 12;

function base64urlEncode(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=/g, '');
}

function base64urlDecode(str: string): Uint8Array {
  str = str.replace(/-/g, '+').replace(/_/g, '/');
  const pad = str.length % 4;
  if (pad) str += '='.repeat(4 - pad);
  const binary = atob(str);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

export async function deriveKey(password: string, salt: Uint8Array): Promise<CryptoKey> {
  const encoder = new TextEncoder();
  const keyMaterial = await subtle.importKey('raw', encoder.encode(password), { name: 'PBKDF2' }, false, ['deriveKey']);
  return subtle.deriveKey(
    { name: 'PBKDF2', salt: salt as BufferSource, iterations: PBKDF2_ITERATIONS, hash: 'SHA-256' },
    keyMaterial,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt']
  );
}

export function generateSalt(): Uint8Array {
  const salt = new Uint8Array(SALT_LENGTH);
  getRandomValues(salt);
  return salt;
}

export function generateNonce(): Uint8Array {
  const nonce = new Uint8Array(NONCE_LENGTH);
  getRandomValues(nonce);
  return nonce;
}

function buildIV(transferNonce: Uint8Array, chunkIndex: number): Uint8Array {
  const iv = new Uint8Array(IV_LENGTH);
  iv.set(transferNonce.slice(0, 4));
  for (let i = 0; i < 8; i++) iv[4 + i] = (chunkIndex >> (56 - i * 8)) & 0xff;
  return iv;
}

export async function encryptChunk(key: CryptoKey, transferNonce: Uint8Array, chunkIndex: number, plaintext: Uint8Array, chunkHeader: Uint8Array): Promise<Uint8Array> {
  const iv = buildIV(transferNonce, chunkIndex);
  const ciphertext = await subtle.encrypt({ name: 'AES-GCM', iv: iv as BufferSource, additionalData: chunkHeader as BufferSource }, key, plaintext as BufferSource);
  return new Uint8Array(ciphertext);
}

export async function decryptChunk(key: CryptoKey, transferNonce: Uint8Array, chunkIndex: number, ciphertext: Uint8Array, chunkHeader: Uint8Array): Promise<Uint8Array> {
  const iv = buildIV(transferNonce, chunkIndex);
  const plaintext = await subtle.decrypt({ name: 'AES-GCM', iv: iv as BufferSource, additionalData: chunkHeader as BufferSource }, key, ciphertext as BufferSource);
  return new Uint8Array(plaintext);
}

export async function createEncryptionMetadata(password: string): Promise<{ metadata: EncryptionMetadata; key: CryptoKey; nonce: Uint8Array }> {
  const salt = generateSalt();
  const nonce = generateNonce();
  const key = await deriveKey(password, salt);
  return {
    metadata: { scheme: 'aes-gcm-pbkdf2', salt: base64urlEncode(salt), iterations: PBKDF2_ITERATIONS, nonce: base64urlEncode(nonce) },
    key,
    nonce,
  };
}

export async function loadEncryptionKey(password: string, metadata: EncryptionMetadata): Promise<CryptoKey> {
  const salt = base64urlDecode(metadata.salt);
  return deriveKey(password, salt);
}