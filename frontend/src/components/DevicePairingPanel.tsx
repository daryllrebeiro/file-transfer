import { useEffect, useState } from 'react';
import { useT } from '../i18n';
import { getMyIdentity, generateDeviceIdentity, getPairedDevices, addPairedDevice, removePairedDevice, formatLastSeen, type DeviceIdentity } from '../services/devicePairing';

export function DevicePairingPanel({ onClose }: { onClose?: () => void }) {
  const translate = useT();
  const [myIdentity, setMyIdentity] = useState<DeviceIdentity | null>(null);
  const [pairedDevices, setPairedDevices] = useState<DeviceIdentity[]>([]);
  const [newDeviceName, setNewDeviceName] = useState('');

  useEffect(() => {
    import('../services/devicePairing').then(m => {
      m.getMyIdentity().then(setMyIdentity);
      m.getPairedDevices().then(setPairedDevices);
    });
  }, []);

  const handleCreateIdentity = async () => {
    if (!newDeviceName.trim()) return;
    const { generateDeviceIdentity, getPairedDevices } = await import('../services/devicePairing');
    const identity = await generateDeviceIdentity(newDeviceName.trim());
    setMyIdentity(identity);
    const devices = await getPairedDevices();
    setPairedDevices(devices);
    setNewDeviceName('');
  };

  const handleRemoveDevice = async (publicKey: string) => {
    const { removePairedDevice, getPairedDevices } = await import('../services/devicePairing');
    await removePairedDevice(publicKey);
    const devices = await getPairedDevices();
    setPairedDevices(devices);
  };

  const formatLastSeenLocal = (timestamp: number) => {
    const diff = Date.now() - timestamp;
    if (diff < 60000) return 'just now';
    if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
    if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
    return `${Math.floor(diff / 86400000)}d ago`;
  };

  return (
    <div style={{ padding: '12px', background: 'rgba(212,242,92,0.08)', borderRadius: 8, border: '1px solid rgba(212,242,92,0.2)' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
        <strong>{translate('devices.title') || 'Device Pairing'}</strong>
        <button onClick={onClose} style={{ padding: '4px 8px', borderRadius: 4, border: '1px solid #3a4a47', background: '#1a2522', color: '#d4f25c', cursor: 'pointer' }}>✕</button>
      </div>
      
      <div style={{ marginBottom: '12px', padding: '8px', background: 'rgba(212,242,92,0.1)', borderRadius: 6 }}>
        <strong>{translate('devices.myIdentity') || 'My Identity'}</strong>
        <div style={{ fontSize: 12, color: '#94a7a0', marginTop: 4 }}>
          {myIdentity ? `${myIdentity.name} (${myIdentity.publicKey.slice(0, 12)}...)` : translate('devices.noIdentity') || 'No identity created'}
        </div>
      </div>

      <div style={{ marginBottom: '12px' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, marginBottom: 8 }}>
          <span style={{ fontSize: 12, color: '#94a7a0' }}>{translate('devices.newDeviceName') || 'New Device Name'}</span>
          <input value={newDeviceName} onChange={e => setNewDeviceName(e.target.value)} placeholder={translate('devices.newDevicePlaceholder') || 'Enter device name'} style={{ padding: '8px 12px', borderRadius: 6, border: '1px solid #3a4a47', background: '#1a2522', color: '#e9eef1' }} />
        </label>
        <button onClick={handleCreateIdentity} disabled={!newDeviceName.trim()} style={{ padding: '8px 16px', borderRadius: 6, border: 'none', background: '#d4f25c', color: '#142019', cursor: 'pointer' }}>{translate('devices.createIdentity') || 'Create Identity'}</button>
      </div>

      <div>
        <strong style={{ display: 'block', marginBottom: 8 }}>{translate('devices.pairedDevices') || 'Paired Devices'}</strong>
        <ul style={{ listStyle: 'none', padding: 0 }}>
          {pairedDevices.length === 0 ? (
            <li style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '8px', background: 'rgba(255,255,255,0.02)', borderRadius: 6, marginBottom: 4 }}>
              <div>
                <strong>{translate('devices.noDevices') || 'No devices paired yet'}</strong>
              </div>
            </li>
          ) : (
            pairedDevices.map(device => (
              <li key={device.publicKey} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '8px', background: 'rgba(255,255,255,0.02)', borderRadius: 6, marginBottom: 4 }}>
                <div>
                  <strong>{device.name}</strong>
                  <div style={{ fontSize: 11, color: '#94a7a0' }}>
                    {device.publicKey.slice(0, 12)}... · {formatLastSeen(device.lastSeen)}
                  </div>
                </div>
                <button onClick={() => { removePairedDevice(device.publicKey).then(() => import('../services/devicePairing').then(m => m.getPairedDevices().then(setPairedDevices))); }} style={{ padding: '4px 8px', borderRadius: 4, border: '1px solid #ff6b6b', background: 'transparent', color: '#ff6b6b', cursor: 'pointer' }}>
                  {translate('devices.remove') || 'Remove'}
                </button>
              </li>
            ))
          )}
          </ul>
        </div>
    </div>
  );
}

export default DevicePairingPanel;