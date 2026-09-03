import { Component, useEffect, useRef, useState, type ErrorInfo, type ReactNode } from 'react';
import { Link, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom';
import { QRCodeSVG } from 'qrcode.react';
import { createTransfer, getTransferLimits, socketURL, type TransferLimits } from './services/api';
import { createHash, digestHex, hashFile } from './services/integrity';
import { createReceiverSink, type ReceiverSink } from './services/receiverStorage';
import type { Metadata } from './types';
import { createTransferTransport, type TransportMode, type TransportStatus } from './transport/TransportFactory';
import { type TransferTransport } from './transport/TransferTransport';
import { logger } from './services/logger';

const chunkSize = 2 * 1024 * 1024;
const bytes = (value: number) => value < 1024 ** 2 ? `${(value / 1024).toFixed(1)} KB` : value < 1024 ** 3 ? `${(value / 1024 ** 2).toFixed(2)} MB` : `${(value / 1024 ** 3).toFixed(2)} GB`;
function Shell({ children }: { children: ReactNode }) { return <main><header><Link to="/" className="brand"><span>◈</span> relay</Link><span className="privacy">Temporary by design</span></header>{children}<footer>Files stream through memory only. Nothing is permanently stored.</footer></main>; }

class ErrorBoundary extends Component<{ children: ReactNode }, { hasError: boolean }> {
  state = { hasError: false };

  static getDerivedStateFromError() { return { hasError: true }; }

  componentDidCatch(error: Error, info: ErrorInfo) {
    logger.error('Unhandled UI error', error, info.componentStack);
  }

  render() {
    if (this.state.hasError) return <Shell><section className="hero compact"><div className="card message"><h2>Something went wrong</h2><p>Reload this page or start a new transfer.</p><Link to="/" className="button">Start over</Link></div></section></Shell>;
    return this.props.children;
  }
}

function Home() {
  const navigate = useNavigate();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [limits, setLimits] = useState<TransferLimits>();

  const supportsWebRTC = typeof RTCPeerConnection !== 'undefined';
  const getInitialTransport = (): TransportMode => {
    if (!supportsWebRTC) return 'relay';
    const saved = localStorage.getItem('relay:default_transport') as TransportMode;
    if (saved === 'webrtc' || saved === 'relay' || saved === 'auto') return saved;
    return (import.meta.env.VITE_DEFAULT_TRANSPORT as TransportMode) || 'auto';
  };
  const [transportMode, setTransportMode] = useState<TransportMode>(getInitialTransport());

  const choose = (selected?: File) => { if (selected) setFile(selected); };

  useEffect(() => {
    const controller = new AbortController();
    void getTransferLimits(controller.signal).then(setLimits).catch(() => undefined);
    return () => controller.abort();
  }, []);

  const handleSelectTransport = (mode: TransportMode) => {
    if (!supportsWebRTC && mode !== 'relay') return;
    setTransportMode(mode);
    localStorage.setItem('relay:default_transport', mode);
  };

  async function create() {
    if (!file) return;
    if (limits && file.size > limits.maxFileSize) {
      setError(`This file is larger than the configured ${bytes(limits.maxFileSize)} limit.`);
      return;
    }
    setBusy(true);
    setError('');
    try {
      const sha256 = await hashFile(file, chunkSize);
      const result = await createTransfer({
        fileName: file.name,
        fileSize: file.size,
        mimeType: file.type || 'application/octet-stream',
        chunkSize,
        sha256,
        transport: transportMode
      });
      sessionStorage.setItem(`sender:${result.id}`, JSON.stringify({
        url: result.url,
        senderToken: result.senderToken,
        sha256,
        fileName: file.name,
        fileSize: file.size,
        transport: transportMode
      }));
      navigate(`/transfer/${result.id}`, { state: { file, url: result.url, senderToken: result.senderToken, sha256, transport: transportMode } });
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Could not create transfer.');
      setBusy(false);
    }
  }

  return (
    <Shell>
      <section className="hero">
        <p className="eyebrow">PRIVATE FILE RELAY</p>
        <h1>Send files without leaving a trail.</h1>
        <p className="lede">A fast, temporary bridge between your devices. Your file is streamed while you need it, then forgotten.</p>
        
        <div className="card dropzone" onDragOver={event => event.preventDefault()} onDrop={event => { event.preventDefault(); choose(event.dataTransfer.files[0]); }}>
          {file ? (
            <>
              <div className="file-icon">↗</div>
              <h2>{file.name}</h2>
              <p>{bytes(file.size)} <span className="muted">· {file.type || 'Unknown type'}</span></p>
              
              <div className="transport-selector">
                <p className="transport-selector-title">TRANSFER METHOD</p>
                <div className="transport-options">
                  <div 
                    className={`transport-option ${transportMode === 'auto' ? 'selected' : ''} ${!supportsWebRTC ? 'disabled' : ''}`}
                    onClick={() => handleSelectTransport('auto')}
                  >
                    <div className="transport-radio" />
                    <div className="transport-info">
                      <p className="transport-name">✨ Automatic</p>
                      <p className="transport-desc">Try peer-to-peer first, then use relay</p>
                    </div>
                  </div>

                  <div 
                    className={`transport-option ${transportMode === 'webrtc' ? 'selected' : ''} ${!supportsWebRTC ? 'disabled' : ''}`}
                    onClick={() => handleSelectTransport('webrtc')}
                  >
                    <div className="transport-radio" />
                    <div className="transport-info">
                      <p className="transport-name">⚡ Peer-to-Peer</p>
                      <p className="transport-desc">Direct connection • Faster • Bypasses server</p>
                    </div>
                  </div>

                  <div 
                    className={`transport-option ${transportMode === 'relay' ? 'selected' : ''}`}
                    onClick={() => handleSelectTransport('relay')}
                  >
                    <div className="transport-radio" />
                    <div className="transport-info">
                      <p className="transport-name">↔ Server Relay</p>
                      <p className="transport-desc">Route through server • Simple • Extremely reliable</p>
                    </div>
                  </div>
                </div>

                {!supportsWebRTC && (
                  <p className="error" style={{ marginTop: '12px', textAlign: 'center' }}>
                    WebRTC is not supported in this browser. Peer-to-Peer transfer is disabled.
                  </p>
                )}

                {supportsWebRTC && transportMode === 'webrtc' && (
                  <div className="transport-explainer">
                    <strong>Peer-to-Peer:</strong>
                    <ul>
                      <li>File bytes travel directly between your devices.</li>
                      <li>Go server is used for signaling handshake only.</li>
                      <li>May fail on restrictive firewalls without a TURN server.</li>
                    </ul>
                  </div>
                )}
                {supportsWebRTC && transportMode === 'relay' && (
                  <div className="transport-explainer">
                    <strong>Server Relay:</strong>
                    <ul>
                      <li>Streamed in transit through the transfer server.</li>
                      <li>Works in almost any network configuration.</li>
                      <li>Simple and reliable connection.</li>
                    </ul>
                  </div>
                )}
              </div>

              <div className="actions">
                <button className="secondary" onClick={() => input.current?.click()}>Change file</button>
                <button onClick={create} disabled={busy}>{busy ? 'Hashing file…' : 'Create transfer link →'}</button>
              </div>
            </>
          ) : (
            <>
              <div className="upload-mark">↑</div>
              <h2>Drop a file here</h2>
              <p>or choose one from this device</p>
              <button onClick={() => input.current?.click()}>Choose a file</button>
            </>
          )}
          <input ref={input} hidden type="file" onChange={event => choose(event.target.files?.[0])} />
        </div>
        
        {error && <p className="error">{error}</p>}
        
        <div className="trust">
          <span>⌁</span>
          <div><strong>No permanent storage</strong><br/><span>Links expire automatically after 15 minutes.</span></div>
          <span>◷</span>
          <div><strong>Streamed in chunks</strong><br/><span>Integrity checked with SHA-256.</span></div>
        </div>
      </section>
    </Shell>
  );
}

function DiagnosticsPanel({ 
  mode, 
  activeMode, 
  status, 
  iceState, 
  dcState, 
  fallbackReason 
}: { 
  mode: TransportMode; 
  activeMode: TransportMode; 
  status: TransportStatus; 
  iceState?: string; 
  dcState?: string; 
  fallbackReason?: string;
}) {
  const [open, setOpen] = useState(false);
  const webrtcSupported = typeof RTCPeerConnection !== 'undefined';
  
  return (
    <div className="diagnostics-section">
      <button className="diagnostics-toggle" onClick={() => setOpen(!open)}>
        {open ? '▼ Hide Connection Details' : '▶ Show Connection Details'}
      </button>
      {open && (
        <div className="diagnostics-content">
          <div className="diagnostics-row">
            <div className="diagnostics-key">Transport Mode:</div>
            <div className="diagnostics-val" style={{ textTransform: 'capitalize' }}>{mode}</div>
          </div>
          <div className="diagnostics-row">
            <div className="diagnostics-key">Active Transport:</div>
            <div className="diagnostics-val" style={{ fontWeight: 'bold', color: activeMode === 'webrtc' ? '#d4f25c' : '#e9eef1' }}>
              {activeMode === 'webrtc' ? 'Peer-to-Peer (WebRTC)' : 'Server Relay (WebSockets)'}
            </div>
          </div>
          <div className="diagnostics-row">
            <div className="diagnostics-key">Connection Status:</div>
            <div className="diagnostics-val">{status}</div>
          </div>
          <div className="diagnostics-row">
            <div className="diagnostics-key">WebRTC Supported:</div>
            <div className="diagnostics-val">{webrtcSupported ? 'YES' : 'NO'}</div>
          </div>
          {activeMode === 'webrtc' && (
            <>
              <div className="diagnostics-row">
                <div className="diagnostics-key">ICE Gathering:</div>
                <div className="diagnostics-val">{iceState || 'new'}</div>
              </div>
              <div className="diagnostics-row">
                <div className="diagnostics-key">Data Channel:</div>
                <div className="diagnostics-val">{dcState || 'closed'}</div>
              </div>
            </>
          )}
          <div className="diagnostics-row">
            <div className="diagnostics-key">Server:</div>
            <div className="diagnostics-val">{activeMode === 'webrtc' ? 'Signaling & Handshake Only' : 'Active Byte Relay'}</div>
          </div>
          {fallbackReason && (
            <div className="diagnostics-row">
              <div className="diagnostics-key">Fallback Reason:</div>
              <div className="diagnostics-val" style={{ color: '#ff9b87' }}>{fallbackReason}</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function Sender() {
  const { transferId } = useParams();
  const location = useLocation();
  const routeDetails = location.state as { file?: File; url: string; senderToken: string; sha256: string; transport?: TransportMode; fileName?: string; fileSize?: number } | undefined;
  const saved = transferId ? (() => { try { return JSON.parse(sessionStorage.getItem(`sender:${transferId}`) || 'null') as { url:string; senderToken:string; sha256:string; transport?: TransportMode; fileName?:string; fileSize?:number } | null; } catch { return null; } })() : null;
  const details = routeDetails || saved;
  const [selectedFile, setSelectedFile] = useState<File | undefined>(routeDetails?.file);
  const file = routeDetails?.file || selectedFile;
  const [progress, setProgress] = useState(0);
  const [waiting, setWaiting] = useState(true);
  const [error, setError] = useState('');
  const [transportStatus, setTransportStatus] = useState<TransportStatus>('new');
  const [activeMode, setActiveMode] = useState<TransportMode>(details?.transport || 'auto');
  
  // Diagnostics
  const [iceState, setIceState] = useState('new');
  const [dcState, setDcState] = useState('closed');
  const [fallbackReason, setFallbackReason] = useState<string | undefined>(undefined);

  const clientRef = useRef<TransferTransport | undefined>(undefined);
  const startChunk = useRef(0);
  const senderToken = details?.senderToken;
  const sha256Val = details?.sha256;

  useEffect(() => {
    if (!transferId || !file || !senderToken) return;
    const controller = new AbortController();
    let sending = false;

    const transport = createTransferTransport(details?.transport || 'auto', {
      role: 'sender',
      url: socketURL(transferId),
      id: transferId,
      token: senderToken,
      file,
      metadata: { fileName: file.name, fileSize: file.size, mimeType: file.type || 'application/octet-stream', chunkSize, sha256: sha256Val || '', transport: details?.transport || 'auto' }
    });
    
    clientRef.current = transport;

    transport.onProgress(prog => {
      setProgress(prog.bytesSent / file.size);
    });

    transport.onError(err => {
      setError(err.message);
      if (details?.transport === 'auto' && transport.getActiveTransport() === 'relay') {
        setFallbackReason(err.message);
      }
    });

    transport.onStatusChange((status, mode) => {
      setTransportStatus(status);
      setActiveMode(mode);
      if (status === 'connected' || status === 'transferring' || status === 'completed') {
        setWaiting(false);
      }
      
      if (mode === 'webrtc') {
        setIceState('connected');
        setDcState(status === 'connected' || status === 'transferring' || status === 'completed' ? 'open' : 'connecting');
      } else {
        setIceState('n/a');
        setDcState('n/a');
      }

      if (status === 'connected' || status === 'transferring') {
        if (!sending) {
          sending = true;
          startChunk.current = transport.getStartChunk?.() || 0;
          void startTransfer(transport);
        }
      }
    });

    async function startTransfer(t: TransferTransport) {
      console.log('[Sender] startTransfer called');
      const f = file;
      if (!f) return;
      try {
        const totalChunks = Math.ceil(f.size / chunkSize);
        console.log('[Sender] Total chunks:', totalChunks);
        for (let i = startChunk.current; i < totalChunks; i++) {
          if (controller.signal.aborted) break;
          const chunk = await f.slice(i * chunkSize, Math.min(f.size, (i + 1) * chunkSize)).arrayBuffer();
          console.log('[Sender] Sending chunk:', i);
          await t.sendChunk(chunk, i);
          console.log('[Sender] Chunk sent:', i);
        }
        if (!controller.signal.aborted) {
          console.log('[Sender] Calling t.complete()');
          await t.complete?.();
          console.log('[Sender] t.complete() finished');
        }
      } catch (e) {
        console.error('SENDER: transfer loop error:', e);
        if (!controller.signal.aborted) {
          setError(e instanceof Error ? e.message : 'Transfer failed');
        }
      }
    }

    transport.connect().catch(err => {
      if (!controller.signal.aborted) {
        setError(err.message || 'Connection failed');
      }
    });

    return () => {
      controller.abort();
      transport.close();
    };
  }, [transferId, file, senderToken, sha256Val, details?.transport]);

  if (!details?.senderToken) return <Shell><Empty title="Sender session unavailable" text="Return home and create a new transfer link." /></Shell>;
  if (!file) return <Shell><section className="hero compact"><div className="card message"><h2>Choose the original file to resume</h2><p>{details.fileName || 'Original file'}{details.fileSize ? ` · ${bytes(details.fileSize)}` : ''}</p><input type="file" onChange={event => setSelectedFile(event.target.files?.[0])} /></div></section></Shell>;

  const renderBadge = () => {
    if (waiting) {
      return <div className="transport-badge connecting">◌ Establishing connection...</div>;
    }
    if (activeMode === 'webrtc') {
      return <div className="transport-badge webrtc">⚡ Connected directly (P2P)</div>;
    }
    if (activeMode === 'relay') {
      const isFallback = details?.transport === 'auto';
      return (
        <div className="transport-badge relay">
          {isFallback ? '↔ Direct connection unavailable • Using relay' : '↔ Server Relay active'}
        </div>
      );
    }
    return null;
  };

  return (
    <Shell>
      <section className="hero compact">
        <p className="eyebrow">TRANSFER READY</p>
        <h1>Share the bridge.</h1>
        <div className="card status-card">
          <div className="qr">
            <QRCodeSVG value={details.url} size={148} />
          </div>
          <div>
            <p className="label">YOUR LINK</p>
            <div className="linkbox">
              {details.url}
              <button onClick={() => navigator.clipboard.writeText(details.url)}>Copy</button>
            </div>
            <p className="waiting">
              {error ? `! ${error}` : waiting ? '◉ Waiting for receiver to accept…' : progress < 1 ? '◉ Sending and verifying…' : '✓ Transfer complete'}
            </p>
            {renderBadge()}
          </div>
        </div>
        {!waiting && <Progress value={progress} file={{ name: file.name, fileSize: file.size }} />}
        
        <DiagnosticsPanel 
          mode={details?.transport || 'auto'}
          activeMode={activeMode}
          status={transportStatus}
          iceState={iceState}
          dcState={dcState}
          fallbackReason={fallbackReason}
        />
      </section>
    </Shell>
  );
}

function Receiver() {
  const { transferId } = useParams();
  const receiverToken = (() => { const hash = window.location.hash.slice(1); return new URLSearchParams(hash).get('token') || ''; })();
  const [metadata, setMetadata] = useState<Metadata>();
  const metadataRef = useRef<Metadata | undefined>(undefined);
  const [state, setState] = useState(receiverToken ? 'Connecting…' : 'This receiver link is missing its access token.');
  const [progress, setProgress] = useState(0);
  const [transportStatus, setTransportStatus] = useState<TransportStatus>('new');
  const [activeMode, setActiveMode] = useState<TransportMode>('auto');

  // Diagnostics
  const [iceState, setIceState] = useState('new');
  const [dcState, setDcState] = useState('closed');
  const [fallbackReason, setFallbackReason] = useState<string | undefined>(undefined);

  const transportRef = useRef<TransferTransport | undefined>(undefined);
  const sink = useRef<ReceiverSink | undefined>(undefined);
  const received = useRef<BlobPart[]>([]);
  const expectedChunk = useRef(0);
  const receivedBytes = useRef(0);
  const hash = useRef(createHash());
  const writes = useRef(Promise.resolve());

  useEffect(() => {
    if (!transferId || !receiverToken) return;

    const transport = createTransferTransport('auto', {
      role: 'receiver',
      url: socketURL(transferId),
      id: transferId,
      token: receiverToken,
    });
    
    transportRef.current = transport;

    transport.onMetadata?.(meta => {
      metadataRef.current = meta;
      setMetadata(meta);
      setState('Waiting for your approval');
    });

    transport.onChunk(chunk => {
      console.log('[Receiver] Chunk index:', chunk.index, 'bytes:', chunk.bytes.byteLength);
      try {
        if (chunk.index < expectedChunk.current) return;
        if (chunk.index !== expectedChunk.current) {
          console.error('[Receiver] Chunks out of order!', chunk.index, 'expected:', expectedChunk.current);
          setState('Transfer failed: chunks arrived out of order.');
          transport.close();
          return;
        }

        const typed = new Uint8Array(chunk.bytes);
        hash.current.update(typed);
        receivedBytes.current += typed.byteLength;
        expectedChunk.current++;
        
        writes.current = writes.current.then(() => sink.current?.write(typed));
        if (sink.current?.blobParts) {
          received.current = sink.current.blobParts;
        }
        
        const totalSize = metadataRef.current?.fileSize || 1;
        setProgress(receivedBytes.current / totalSize);
      } catch (e) {
        console.error('[Receiver] chunk processing error:', e);
        setState('Transfer failed: invalid chunk received.');
        transport.close();
      }
    });

    transport.onError(err => {
      console.error('[Receiver] transport error:', err);
      setState(err.message || 'Transfer failed');
      setFallbackReason(err.message);
    });

    transport.onStatusChange((status, mode) => {
      console.log('[Receiver] status changed:', status, 'mode:', mode);
      setTransportStatus(status);
      setActiveMode(mode);
      
      if (status === 'completed') {
        void finishReceive();
      }

      if (mode === 'webrtc') {
        setIceState('connected');
        setDcState(status === 'connected' || status === 'transferring' || status === 'completed' ? 'open' : 'connecting');
      } else {
        setIceState('n/a');
        setDcState('n/a');
      }
    });

    transport.connect().catch(err => {
      console.error('[Receiver] connect error:', err);
      setState(err.message || 'Connection failed');
    });

    return () => {
      transport.close();
    };
  }, [transferId, receiverToken]);

  async function finishReceive() {
    console.log('[Receiver] finishReceive called. Received bytes:', receivedBytes.current);
    await writes.current;
    const current = metadataRef.current;
    if (!current) {
      console.error('[Receiver] No metadata in finishReceive');
      return;
    }

    if (receivedBytes.current !== current.fileSize) {
      console.error('[Receiver] Size mismatch! Received:', receivedBytes.current, 'Expected:', current.fileSize);
      setState('Transfer failed: received size did not match.');
      return;
    }
    const actualHash = digestHex(hash.current);
    console.log('[Receiver] Expected hash:', current.sha256, 'Actual hash:', actualHash);
    if (current.sha256 && actualHash !== current.sha256) {
      console.error('[Receiver] Hash verification failed!');
      setState('Transfer failed: SHA-256 verification failed.');
      return;
    }
    
    console.log('[Receiver] File verified. Saving...');
    await sink.current?.close();
    if (sink.current?.blobParts) {
      const blob = new Blob(received.current, { type: current.mimeType });
      const anchor = document.createElement('a');
      anchor.href = URL.createObjectURL(blob);
      anchor.download = current.fileName;
      anchor.click();
    }
    setProgress(1);
    setState('Download complete');
  }

  async function accept() {
    const current = metadata;
    if (!current || !transportRef.current) return;
    try {
      sink.current = await createReceiverSink(current.fileName, current.mimeType, current.fileSize);
      transportRef.current.accept?.();
      setState('Receiving…');
    } catch (reason) {
      setState(reason instanceof Error ? reason.message : 'Could not prepare the download.');
    }
  }

  const renderBadge = () => {
    if (state === 'Connecting…') {
      return <div className="transport-badge connecting">◌ Connecting...</div>;
    }
    if (activeMode === 'webrtc') {
      return <div className="transport-badge webrtc">⚡ Connected directly (P2P)</div>;
    }
    if (activeMode === 'relay') {
      const isFallback = metadata?.transport === 'auto';
      return (
        <div className="transport-badge relay">
          {isFallback ? '↔ Direct connection unavailable • Using relay' : '↔ Server Relay active'}
        </div>
      );
    }
    return null;
  };

  return (
    <Shell>
      <section className="hero compact">
        <p className="eyebrow">INCOMING TRANSFER</p>
        <h1>{state === 'Download complete' ? 'File received.' : 'A file is waiting.'}</h1>
        {metadata ? (
          <div className="card receive-card">
            <div className="file-icon">↘</div>
            <h2>{metadata.fileName}</h2>
            <p>{bytes(metadata.fileSize)} <span className="muted">· From another device</span></p>
            {state === 'Waiting for your approval' ? (
              <div className="actions">
                <button onClick={() => void accept()}>Download file</button>
                <button className="secondary" onClick={() => transportRef.current?.close()}>Reject</button>
              </div>
            ) : (
              <>
                <Progress value={progress} file={{ name: metadata.fileName, fileSize: metadata.fileSize }} />
                <p className="muted" style={{ marginTop: '12px' }}>{state}</p>
                {renderBadge()}
              </>
            )}
            
            <DiagnosticsPanel 
              mode={metadata?.transport as TransportMode || 'auto'}
              activeMode={activeMode}
              status={transportStatus}
              iceState={iceState}
              dcState={dcState}
              fallbackReason={fallbackReason}
            />
          </div>
        ) : (
          <div className="card message">{state}</div>
        )}
      </section>
    </Shell>
  );
}

function Progress({ value, file }: { value:number; file:{ name:string; fileSize:number } }) { const total = file.fileSize; return <div className="progress-wrap"><div className="progress-label"><strong>{Math.round(value * 100)}%</strong><span>{bytes(Math.min(value * total, total))} / {bytes(total)}</span></div><div className="bar"><span style={{ width:`${value * 100}%` }} /></div><p className="muted">{value >= 1 ? `✓ ${file.name} verified by SHA-256` : file.name}</p></div>; }
function Empty({ title, text }: { title:string; text:string }) { return <section className="hero compact"><div className="card message"><h2>{title}</h2><p>{text}</p><Link to="/" className="button">Start over</Link></div></section>; }
export default function App() { return <ErrorBoundary><Routes><Route path="/" element={<Home />} /><Route path="/transfer/:transferId" element={<Sender />} /><Route path="/receive/:transferId" element={<Receiver />} /><Route path="*" element={<Empty title="Page not found" text="This transfer path does not exist." />} /></Routes></ErrorBoundary>; }
