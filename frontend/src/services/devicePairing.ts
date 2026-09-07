export interface DeviceIdentity {
  publicKey: string; // base64url encoded Ed25519 public key
  name: string; // user-friendly name
  createdAt: number;
  lastSeen: number;
}

export interface SignedToken {
  payload: {
    deviceId: string; // public key
    transferId: string;
    role: 'sender' | 'receiver';
    timestamp: number;
    expiresAt: number;
  };
  signature: string; // base64url encoded Ed25519 signature
}

const DEVICE_KEY_PREFIX = 'relay-device-';
const PAIRED_DEVICES_KEY = 'relay-paired-devices';
const MY_IDENTITY_KEY = 'relay-my-identity';

let cryptoSubtle: SubtleCrypto | null = null;
function getCrypto(): SubtleCrypto {
  if (!cryptoSubtle) {
    cryptoSubtle = window.crypto.subtle;
  }
  return cryptoSubtle;
}

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

export async function generateDeviceIdentity(name: string): Promise<DeviceIdentity> {
  const keyPair = await getCrypto().generateKey(
    { name: 'Ed25519' },
    true,
    ['sign', 'verify']
  );
  
  const publicKeyRaw = await getCrypto().exportKey('raw', keyPair.publicKey);
  const publicKey = base64urlEncode(new Uint8Array(publicKeyRaw as ArrayBuffer));
  
  const identity: DeviceIdentity = {
    publicKey,
    name,
    createdAt: Date.now(),
    lastSeen: Date.now(),
  };
  
  // Store private key in secure storage (IndexedDB)
  const privateKeyRaw = await getCrypto().exportKey('pkcs8', keyPair.privateKey);
  localStorage.setItem(`${DEVICE_KEY_PREFIX}${publicKey}`, base64urlEncode(new Uint8Array(privateKeyRaw as ArrayBuffer)));
  
  return identity;
}

export async function getMyIdentity(): Promise<DeviceIdentity | null> {
  try {
    const stored = localStorage.getItem(MY_IDENTITY_KEY);
    if (stored) {
      return JSON.parse(stored);
    }
  } catch {}
  return null;
}

export async function setMyIdentity(identity: DeviceIdentity): Promise<void> {
  localStorage.setItem(MY_IDENTITY_KEY, JSON.stringify(identity));
}

export async function getPairedDevices(): Promise<DeviceIdentity[]> {
  try {
    const stored = localStorage.getItem(PAIRED_DEVICES_KEY);
    if (stored) {
      return JSON.parse(stored);
    }
  } catch {}
  return [];
}

export async function addPairedDevice(device: DeviceIdentity): Promise<void> {
  const devices = await getPairedDevices();
  const existing = devices.findIndex(d => d.publicKey === device.publicKey);
  if (existing >= 0) {
    devices[existing] = device;
  } else {
    devices.push(device);
  }
  localStorage.setItem(PAIRED_DEVICES_KEY, JSON.stringify(devices));
}

export async function removePairedDevice(publicKey: string): Promise<void> {
  const devices = await getPairedDevices();
  const filtered = devices.filter(d => d.publicKey !== publicKey);
  localStorage.setItem(PAIRED_DEVICES_KEY, JSON.stringify(filtered));
}

async function getPrivateKey(publicKey: string): Promise<CryptoKey | null> {
  try {
    const stored = localStorage.getItem(`${DEVICE_KEY_PREFIX}${publicKey}`);
    if (!stored) return null;
    const raw = base64urlDecode(stored);
    return await getCrypto().importKey('pkcs8', raw.buffer as ArrayBuffer, { name: 'Ed25519' }, false, ['sign']);
  } catch {
    return null;
  }
}

export async function createSignedToken(transferId: string, role: 'sender' | 'receiver', expiresInMs: number = 300000): Promise<SignedToken | null> {
  const myIdentity = await getMyIdentity();
  if (!myIdentity) return null;
  
  const privateKey = await getPrivateKey(myIdentity.publicKey);
  if (!privateKey) return null;
  
  const now = Date.now();
  const payload = {
    deviceId: myIdentity.publicKey,
    transferId,
    role,
    timestamp: now,
    expiresAt: now + expiresInMs,
  };
  
  const encoder = new TextEncoder();
  const data = encoder.encode(JSON.stringify(payload));
  const signatureRaw = await getCrypto().sign('Ed25519', privateKey, data);
  const signature = base64urlEncode(new Uint8Array(signatureRaw));
  
  return { payload, signature };
}

export async function verifySignedToken(token: SignedToken, expectedTransferId: string, expectedRole: 'sender' | 'receiver'): Promise<DeviceIdentity | null> {
  const { payload, signature: sig } = token;
  
  // Verify transfer ID and role
  if (payload.transferId !== expectedTransferId || payload.role !== expectedRole) {
    return null;
  }
  
  // Check expiration
  if (Date.now() > payload.expiresAt) {
    return null;
  }
  
  // Verify signature
  const publicKeyRaw = base64urlDecode(payload.deviceId);
  const publicKey = await getCrypto().importKey('raw', publicKeyRaw.buffer as ArrayBuffer, { name: 'Ed25519' }, false, ['verify']);
  
  const encoder = new TextEncoder();
  const data = encoder.encode(JSON.stringify(payload));
  const signature = base64urlDecode(sig);
  
  const valid = await getCrypto().verify('Ed25519', publicKey, signature.buffer as ArrayBuffer, data.buffer as ArrayBuffer);
  if (!valid) return null;
  
  // Check if device is paired or is ours
  const myIdentity = await getMyIdentity();
  if (myIdentity && myIdentity.publicKey === payload.deviceId) {
    return myIdentity;
  }
  
  const pairedDevices = await getPairedDevices();
  const paired = pairedDevices.find(d => d.publicKey === payload.deviceId);
  if (paired) {
    return paired;
  }
  
  // Unknown device
  return { publicKey: payload.deviceId, name: 'Unknown Device', createdAt: payload.timestamp, lastSeen: Date.now() };
}

export async function updateDeviceLastSeen(publicKey: string): Promise<void> {
  const myIdentity = await getMyIdentity();
  if (myIdentity && myIdentity.publicKey === publicKey) {
    myIdentity.lastSeen = Date.now();
    await setMyIdentity(myIdentity);
  }
  
  const pairedDevices = await getPairedDevices();
  const device = pairedDevices.find(d => d.publicKey === publicKey);
  if (device) {
    device.lastSeen = Date.now();
    localStorage.setItem(PAIRED_DEVICES_KEY, JSON.stringify(pairedDevices));
  }
}

export function formatDeviceName(device: DeviceIdentity): string {
  return `${device.name} (${device.publicKey.slice(0, 8)}...)`;
}

export function formatLastSeen(timestamp: number): string {
  const diff = Date.now() - timestamp;
  if (diff < 60000) return 'just now';
  if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
  return `${Math.floor(diff / 86400000)}d ago`;
}