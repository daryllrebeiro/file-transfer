import { Component, useEffect, useRef, useState, type ErrorInfo, type ReactNode } from 'react';
import { Link, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom';
import { QRCodeSVG } from 'qrcode.react';
import { createTransfer, extendTransfer, getTransferLimits, socketURL, type TransferLimits } from './services/api';
import { createHash, digestHex, hashFile } from './services/integrity';
import { createReceiverSink, type ReceiverSink } from './services/receiverStorage';
import type { Metadata } from './types';
import { createTransferTransport, type TransportMode, type TransportStatus } from './transport/TransportFactory';
import { type TransferTransport } from './transport/TransferTransport';
import { logger } from './services/logger';
import { addHistory, clearHistory, getHistory, updateHistory, type HistoryEntry } from './services/history';
import { locales, t, useLocale, useT, type Locale } from './i18n';

const chunkSize = 2 * 1024 * 1024;
const bytes = (value: number) => value < 1024 ** 2 ? `${(value / 1024).toFixed(1)} KB` : value < 1024 ** 3 ? `${(value / 1024 ** 2).toFixed(2)} MB` : `${(value / 1024 ** 3).toFixed(2)} GB`;
function Shell({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<'light' | 'dark'>(() => {
    try {
      return (localStorage.getItem('relay:theme') as 'light' | 'dark') || 'dark';
    } catch {
      return 'dark';
    }
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem('relay:theme', theme);
    } catch {
      // best-effort persistence
    }
  }, [theme]);
  const { locale, change } = useLocale();
  const translate = useT();
  return <main><header><Link to="/" className="brand"><span>◈</span> relay</Link><span className="privacy">{translate('brand.tagline')}</span><span className="header-actions"><button className="theme-toggle" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')} aria-label={theme === 'dark' ? translate('theme.toLight') : translate('theme.toDark')}>{theme === 'dark' ? translate('theme.light') : translate('theme.dark')}</button><select className="lang-select" aria-label={translate('lang.label')} value={locale} onChange={event => change(event.target.value as Locale)}>{locales.map(localeOption => <option key={localeOption} value={localeOption}>{localeOption.toUpperCase()}</option>)}</select></span></header>{children}<footer>{translate('brand.footer')}</footer></main>;
}

class ErrorBoundary extends Component<{ children: ReactNode }, { hasError: boolean }> {
  state = { hasError: false };

  static getDerivedStateFromError() { return { hasError: true }; }

  componentDidCatch(error: Error, info: ErrorInfo) {
    logger.error('Unhandled UI error', error, info.componentStack);
  }

  render() {
    if (this.state.hasError) return <Shell><section className="hero compact"><div className="card message"><h2>{t('errorBoundary.title')}</h2><p>{t('errorBoundary.text')}</p><Link to="/" className="button">{t('startOver')}</Link></div></section></Shell>;
    return this.props.children;
  }
}

function Home() {
  const navigate = useNavigate();
  const input = useRef<HTMLInputElement>(null);
  const translate = useT();
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
  const [history, setHistory] = useState<HistoryEntry[]>(() => getHistory());
  const [sendMode, setSendMode] = useState<'file' | 'text'>('file');
  const [text, setText] = useState('');
  const textLimit = 64 * 1024;

  const choose = (selected?: File) => { if (selected) { setFile(selected); setSendMode('file'); } };

  useEffect(() => {
    const controller = new AbortController();
    void getTransferLimits(controller.signal).then(setLimits).catch(() => undefined);
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const sharedTitle = params.get('title') || '';
    const sharedText = params.get('text') || '';
    if (sharedTitle || sharedText) {
      const combined = [sharedTitle, sharedText].filter(Boolean).join('\n').slice(0, textLimit);
      setText(combined);
      setSendMode('text');
      window.history.replaceState({}, '', '/');
    }
  }, []);

  const handleSelectTransport = (mode: TransportMode) => {
    if (!supportsWebRTC && mode !== 'relay') return;
    setTransportMode(mode);
    localStorage.setItem('relay:default_transport', mode);
  };

  async function create(chosen?: File) {
    const target = chosen ?? file;
    if (!target) return;
    if (limits && target.size > limits.maxFileSize) {
      setError(translate('tooLarge', { max: bytes(limits.maxFileSize) }));
      return;
    }
    setBusy(true);
    setError('');
    try {
      const sha256 = await hashFile(target, chunkSize);
      const result = await createTransfer({
        fileName: target.name,
        fileSize: target.size,
        mimeType: target.type || 'application/octet-stream',
        chunkSize,
        sha256,
        transport: transportMode
      });
      sessionStorage.setItem(`sender:${result.id}`, JSON.stringify({
        url: result.url,
        senderToken: result.senderToken,
        sha256,
        fileName: target.name,
        fileSize: target.size,
        expiresAt: result.expiresAt,
        transport: transportMode
      }));
      addHistory({ id: result.id, name: target.name, size: target.size, ts: Date.now(), outcome: 'created' });
      setHistory(getHistory());
      navigate(`/transfer/${result.id}`, { state: { file: target, url: result.url, senderToken: result.senderToken, sha256, expiresAt: result.expiresAt, transport: transportMode } });
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Could not create transfer.');
      setBusy(false);
    }
  }

  async function createText() {
    const value = text.trim();
    if (!value) return;
    const textFile = new File([value], 'message.txt', { type: 'text/plain;charset=utf-8' });
    setSendMode('file');
    await create(textFile);
  }

  return (
    <Shell>
      <section className="hero">
        <p className="eyebrow">{translate('home.eyebrow')}</p>
        <h1>{translate('home.title')}</h1>
        <p className="lede">{translate('home.lede')}</p>

        <div className="send-tabs" role="tablist" aria-label={translate('tabs.what')}>
          <button className={`send-tab ${sendMode === 'file' ? 'active' : ''}`} role="tab" aria-selected={sendMode === 'file'} onClick={() => setSendMode('file')}>{translate('tab.file')}</button>
          <button className={`send-tab ${sendMode === 'text' ? 'active' : ''}`} role="tab" aria-selected={sendMode === 'text'} onClick={() => setSendMode('text')}>{translate('tab.text')}</button>
        </div>

        <div className="card dropzone" onDragOver={event => event.preventDefault()} onDrop={event => { event.preventDefault(); choose(event.dataTransfer.files[0]); }}>
          {sendMode === 'text' ? (
            <>
              <div className="file-icon">T</div>
              <h2>{translate('send.text')}</h2>
              <p className="muted">{translate('send.textHint')}</p>
              <textarea className="text-input" value={text} onChange={event => setText(event.target.value.slice(0, textLimit))} rows={8} placeholder={translate('send.textPlaceholder')} aria-label={translate('send.textAria')} />
              <p className="text-count">{translate('send.textCount', { used: bytes(text.length), limit: bytes(textLimit) })}</p>
              <div className="actions">
                <button className="secondary" onClick={() => setSendMode('file')}>{translate('send.fileInstead')}</button>
                <button onClick={() => void createText()} disabled={busy || text.trim().length === 0}>{busy ? translate('create.textHashing') : translate('create.text')}</button>
              </div>
            </>
          ) : file ? (
            <>
              <div className="file-icon">↗</div>
              <h2>{file.name}</h2>
              <p>{bytes(file.size)} <span className="muted">· {file.type || translate('file.unknownType')}</span></p>
              
              <div className="transport-selector">
                <p className="transport-selector-title">{translate('transport.title')}</p>
                <div className="transport-options" role="radiogroup" aria-label="Transfer method">
                  <label
                    className={`transport-option ${transportMode === 'auto' ? 'selected' : ''} ${!supportsWebRTC ? 'disabled' : ''}`}
                  >
                    <input type="radio" name="transport" value="auto" disabled={!supportsWebRTC} checked={transportMode === 'auto'} onChange={() => handleSelectTransport('auto')} />
                    <span className="transport-radio" aria-hidden="true" />
                    <span className="transport-info">
                      <span className="transport-name">{translate('transport.auto.name')}</span>
                      <span className="transport-desc">{translate('transport.auto.desc')}</span>
                    </span>
                  </label>

                  <label
                    className={`transport-option ${transportMode === 'webrtc' ? 'selected' : ''} ${!supportsWebRTC ? 'disabled' : ''}`}
                  >
                    <input type="radio" name="transport" value="webrtc" disabled={!supportsWebRTC} checked={transportMode === 'webrtc'} onChange={() => handleSelectTransport('webrtc')} />
                    <span className="transport-radio" aria-hidden="true" />
                    <span className="transport-info">
                      <span className="transport-name">{translate('transport.webrtc.name')}</span>
                      <span className="transport-desc">{translate('transport.webrtc.desc')}</span>
                    </span>
                  </label>

                  <label
                    className={`transport-option ${transportMode === 'relay' ? 'selected' : ''}`}
                  >
                    <input type="radio" name="transport" value="relay" checked={transportMode === 'relay'} onChange={() => handleSelectTransport('relay')} />
                    <span className="transport-radio" aria-hidden="true" />
                    <span className="transport-info">
                      <span className="transport-name">{translate('transport.relay.name')}</span>
                      <span className="transport-desc">{translate('transport.relay.desc')}</span>
                    </span>
                  </label>
                </div>

                {!supportsWebRTC && (
                  <p className="error" style={{ marginTop: '12px', textAlign: 'center' }}>
                    {translate('transport.webrtcUnsupported')}
                  </p>
                )}

                {supportsWebRTC && transportMode === 'webrtc' && (
                  <div className="transport-explainer">
                    <strong>{translate('transport.explainer.webrtc')}</strong>
                    <ul>
                      <li>{translate('transport.explainer.webrtc.1')}</li>
                      <li>{translate('transport.explainer.webrtc.2')}</li>
                      <li>{translate('transport.explainer.webrtc.3')}</li>
                    </ul>
                  </div>
                )}
                {supportsWebRTC && transportMode === 'relay' && (
                  <div className="transport-explainer">
                    <strong>{translate('transport.explainer.relay')}</strong>
                    <ul>
                      <li>{translate('transport.explainer.relay.1')}</li>
                      <li>{translate('transport.explainer.relay.2')}</li>
                      <li>{translate('transport.explainer.relay.3')}</li>
                    </ul>
                  </div>
                )}
              </div>

              <div className="actions">
                <button className="secondary" onClick={() => input.current?.click()}>{translate('file.change')}</button>
                <button onClick={() => void create()} disabled={busy}>{busy ? translate('create.fileHashing') : translate('create.file')}</button>
              </div>
            </>
          ) : (
            <>
              <div className="upload-mark">↑</div>
              <h2>{translate('drop.here')}</h2>
              <p>{translate('drop.or')}</p>
              <button onClick={() => input.current?.click()}>{translate('drop.choose')}</button>
            </>
          )}
          <input ref={input} hidden type="file" onChange={event => choose(event.target.files?.[0])} />
        </div>
        
        {error && <p className="error" role="alert">{error}</p>}
        
        <div className="trust">
          <span>⌁</span>
          <div><strong>{translate('trust.1.title')}</strong><br/><span>{translate('trust.1.sub')}</span></div>
          <span>◷</span>
          <div><strong>{translate('trust.2.title')}</strong><br/><span>{translate('trust.2.sub')}</span></div>
        </div>

        {history.length > 0 && (
          <section className="history" aria-label={translate('history.title')}>
            <div className="history-head">
              <h2>{translate('history.title')}</h2>
              <button className="secondary" onClick={() => { clearHistory(); setHistory([]); }}>{translate('history.clear')}</button>
            </div>
            <ul>
              {history.map(entry => {
                const resumable = (() => { try { return !!sessionStorage.getItem(`sender:${entry.id}`); } catch { return false; } })();
                const outcomeLabel = entry.outcome === 'sent' ? translate('history.outcome.sent') : entry.outcome === 'failed' ? translate('history.outcome.failed') : translate('history.outcome.created');
                return (
                  <li key={entry.id}>
                    {resumable ? (
                      <button className="history-row" onClick={() => navigate(`/transfer/${entry.id}`)}>
                        <span className="history-outcome" data-outcome={entry.outcome} aria-hidden="true" />
                        <span className="history-name">{entry.name}</span>
                        <span className="history-meta">{bytes(entry.size)} · {new Date(entry.ts).toLocaleString()} · {outcomeLabel}</span>
                        <span className="history-resume">{translate('history.resume')}</span>
                      </button>
                    ) : (
                      <span className="history-row history-row-static">
                        <span className="history-outcome" data-outcome={entry.outcome} aria-hidden="true" />
                        <span className="history-name">{entry.name}</span>
                        <span className="history-meta">{bytes(entry.size)} · {new Date(entry.ts).toLocaleString()} · {outcomeLabel}</span>
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
          </section>
        )}
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
      <button className="diagnostics-toggle" aria-expanded={open} aria-controls="diagnostics-content" onClick={() => setOpen(!open)}>
        {open ? '▼ Hide Connection Details' : '▶ Show Connection Details'}
      </button>
      {open && (
        <div className="diagnostics-content" id="diagnostics-content">
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
  const translate = useT();
  const { transferId } = useParams();
  const location = useLocation();
  const routeDetails = location.state as { file?: File; url: string; senderToken: string; sha256: string; transport?: TransportMode; fileName?: string; fileSize?: number; expiresAt?: string } | undefined;
  const saved = transferId ? (() => { try { return JSON.parse(sessionStorage.getItem(`sender:${transferId}`) || 'null') as { url:string; senderToken:string; sha256:string; transport?: TransportMode; fileName?:string; fileSize?:number; expiresAt?:string } | null; } catch { return null; } })() : null;
  const details = routeDetails || saved;
  const [selectedFile, setSelectedFile] = useState<File | undefined>(routeDetails?.file);
  const file = routeDetails?.file || selectedFile;
  const [progress, setProgress] = useState(0);
  const [waiting, setWaiting] = useState(true);
  const [error, setError] = useState('');
  const [transportStatus, setTransportStatus] = useState<TransportStatus>('new');
  const [activeMode, setActiveMode] = useState<TransportMode>(details?.transport || 'auto');
  const [expiresAt, setExpiresAt] = useState<string | undefined>(details?.expiresAt);
  const [clock, setClock] = useState(() => Date.now());
  const [extending, setExtending] = useState(false);
  const remainingMs = expiresAt ? Math.max(0, Date.parse(expiresAt) - clock) : undefined;
  const fmtCountdown = (ms: number) => { const s = Math.floor(ms / 1000); return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`; };

  useEffect(() => {
    if (!expiresAt) return;
    const timer = window.setInterval(() => setClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [expiresAt]);

  const historyRecorded = useRef(false);
  useEffect(() => {
    if (!transferId || historyRecorded.current) return;
    if (transportStatus === 'completed') {
      historyRecorded.current = true;
      updateHistory(transferId, { outcome: 'sent', ts: Date.now() });
    } else if (transportStatus === 'failed') {
      historyRecorded.current = true;
      updateHistory(transferId, { outcome: 'failed', ts: Date.now() });
    }
  }, [transportStatus, transferId]);

  async function handleExtend() {
    if (!transferId || !senderToken || !expiresAt) return;
    setExtending(true);
    try {
      const next = await extendTransfer(transferId, senderToken);
      setExpiresAt(next.expiresAt);
      setClock(Date.now());
      try {
        const raw = sessionStorage.getItem(`sender:${transferId}`);
        if (raw) {
          const data = JSON.parse(raw);
          data.expiresAt = next.expiresAt;
          sessionStorage.setItem(`sender:${transferId}`, JSON.stringify(data));
        }
      } catch { /* non-fatal */ }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Could not extend this link.');
    } finally {
      setExtending(false);
    }
  }
  
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
      // A sender may only begin streaming once the receiver has accepted. For relay the
      // 'connected' state fires at join (before acceptance); only WebRTC's 'connected'
      // already implies an established, post-accept data channel.
      const canStart = status === 'transferring' || (status === 'connected' && mode === 'webrtc');
      if (canStart || status === 'completed') {
        setWaiting(false);
      }
      
      if (mode === 'webrtc') {
        setIceState('connected');
        setDcState(status === 'connected' || status === 'transferring' || status === 'completed' ? 'open' : 'connecting');
      } else {
        setIceState('n/a');
        setDcState('n/a');
      }

      if (canStart) {
        if (!sending) {
          sending = true;
          startChunk.current = transport.getStartChunk?.() || 0;
          void startTransfer(transport);
        }
      }
    });

    async function startTransfer(t: TransferTransport) {
      logger.debug('[Sender] startTransfer called');
      const f = file;
      if (!f) return;
      try {
        const baseSize = chunkSize;
        let offset = startChunk.current * baseSize;
        let index = startChunk.current;
        while (offset < f.size) {
          if (controller.signal.aborted) break;
          const size = Math.min(t.getChunkSize?.() ?? baseSize, f.size - offset);
          logger.debug('[Sender] Sending chunk:', index, 'size:', size);
          const chunk = await f.slice(offset, offset + size).arrayBuffer();
          await t.sendChunk(chunk, index);
          offset += chunk.byteLength;
          index++;
        }
        if (!controller.signal.aborted) {
          logger.debug('[Sender] Calling t.complete()');
          await t.complete?.();
          logger.debug('[Sender] t.complete() finished');
        }
      } catch (e) {
        logger.error('SENDER: transfer loop error:', e);
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

  if (!details?.senderToken) return <Shell><Empty title={translate('sender.unavailable')} text={translate('sender.unavailable.text')} /></Shell>;
  if (!file) return <Shell><section className="hero compact"><div className="card message"><h2>{translate('sender.resume')}</h2><p>{details.fileName || translate('sender.originalFile')}{details.fileSize ? ` · ${bytes(details.fileSize)}` : ''}</p><input type="file" onChange={event => setSelectedFile(event.target.files?.[0])} /></div></section></Shell>;

  const renderBadge = () => {
    if (waiting) {
      return <div className="transport-badge connecting">{translate('badge.connecting')}</div>;
    }
    if (activeMode === 'webrtc') {
      return <div className="transport-badge webrtc">{translate('badge.webrtc')}</div>;
    }
    if (activeMode === 'relay') {
      const isFallback = details?.transport === 'auto';
      return (
        <div className="transport-badge relay">
          {isFallback ? translate('badge.relayFallback') : translate('badge.relayActive')}
        </div>
      );
    }
    return null;
  };

  return (
    <Shell>
      <section className="hero compact">
        <p className="eyebrow">{translate('sender.readyEyebrow')}</p>
        <h1>{translate('sender.shareTitle')}</h1>
        <div className="card status-card">
          <div className="qr">
            <QRCodeSVG value={details.url} size={148} />
          </div>
          <div>
            <p className="label">{translate('sender.yourLink')}</p>
            <div className="linkbox">
              {details.url}
              <button onClick={() => navigator.clipboard.writeText(details.url)}>{translate('copy')}</button>
            </div>
            {expiresAt && remainingMs !== undefined && (
              <p className={`expiry ${remainingMs === 0 ? 'urgent' : remainingMs < 120000 ? 'urgent' : ''}`} role="status">
                {remainingMs === 0
                  ? translate('link.expired')
                  : <><span>{translate('link.expiresIn', { time: fmtCountdown(remainingMs) })}</span>{remainingMs < 120000 && senderToken && (
                      <button className="link-extend" onClick={handleExtend} disabled={extending}>{extending ? translate('link.extending') : translate('link.extend')}</button>
                    )}</>}
              </p>
            )}
            <p className="waiting" role="status" aria-live="polite">
              {error ? `! ${error}` : waiting ? translate('status.waiting') : progress < 1 ? translate('status.sending') : translate('status.complete')}
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
  const translate = useT();
  const statusKeys: Record<string, string> = {
    'Connecting…': 'status.connecting',
    'Waiting for your approval': 'receive.waitingApproval',
    'Receiving…': 'status.receiving',
    'Download complete': 'receive.done',
    'File received.': 'receive.done',
    'This receiver link is missing its access token.': 'empty.missingToken',
  };
  const statusLabel = (raw: string) => { const key = statusKeys[raw]; return key ? translate(key) : raw; };
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
      logger.debug('[Receiver] Chunk index:', chunk.index, 'bytes:', chunk.bytes.byteLength);
      try {
        if (chunk.index < expectedChunk.current) return;
        if (chunk.index !== expectedChunk.current) {
          logger.error('[Receiver] Chunks out of order!', chunk.index, 'expected:', expectedChunk.current);
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
        logger.error('[Receiver] chunk processing error:', e);
        setState('Transfer failed: invalid chunk received.');
        transport.close();
      }
    });

    transport.onError(err => {
      logger.error('[Receiver] transport error:', err);
      setState(err.message || 'Transfer failed');
      setFallbackReason(err.message);
    });

    transport.onStatusChange((status, mode) => {
      logger.debug('[Receiver] status changed:', status, 'mode:', mode);
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
      logger.error('[Receiver] connect error:', err);
      setState(err.message || 'Connection failed');
    });

    return () => {
      transport.close();
    };
  }, [transferId, receiverToken]);

  async function finishReceive() {
    logger.debug('[Receiver] finishReceive called. Received bytes:', receivedBytes.current);
    await writes.current;
    const current = metadataRef.current;
    if (!current) {
      logger.error('[Receiver] No metadata in finishReceive');
      return;
    }

    if (receivedBytes.current !== current.fileSize) {
      logger.error('[Receiver] Size mismatch! Received:', receivedBytes.current, 'Expected:', current.fileSize);
      setState('Transfer failed: received size did not match.');
      return;
    }
    const actualHash = digestHex(hash.current);
    logger.debug('[Receiver] Expected hash:', current.sha256, 'Actual hash:', actualHash);
    if (current.sha256 && actualHash !== current.sha256) {
      logger.error('[Receiver] Hash verification failed!');
      setState('Transfer failed: SHA-256 verification failed.');
      return;
    }
    
    logger.debug('[Receiver] File verified. Saving...');
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
      return <div className="transport-badge connecting">{translate('badge.connecting')}</div>;
    }
    if (activeMode === 'webrtc') {
      return <div className="transport-badge webrtc">{translate('badge.webrtc')}</div>;
    }
    if (activeMode === 'relay') {
      const isFallback = metadata?.transport === 'auto';
      return (
        <div className="transport-badge relay">
          {isFallback ? translate('badge.relayFallback') : translate('badge.relayActive')}
        </div>
      );
    }
    return null;
  };

  return (
    <Shell>
      <section className="hero compact">
        <p className="eyebrow">{translate('receive.incoming')}</p>
        <h1>{state === 'Download complete' ? translate('receive.done') : translate('receive.waitingFile')}</h1>
        {metadata ? (
          <div className="card receive-card">
            <div className="file-icon">↘</div>
            <h2>{metadata.fileName}</h2>
            <p>{bytes(metadata.fileSize)} <span className="muted">· {translate('receive.fromDevice')}</span></p>
            {state === 'Waiting for your approval' ? (
              <div className="actions">
                <button onClick={() => void accept()}>{translate('receive.download')}</button>
                <button className="secondary" onClick={() => transportRef.current?.close()}>{translate('receive.reject')}</button>
              </div>
            ) : (
              <>
                <Progress value={progress} file={{ name: metadata.fileName, fileSize: metadata.fileSize }} />
                <p className="muted" style={{ marginTop: '12px' }} role="status" aria-live="polite">{statusLabel(state)}</p>
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
          <div className="card message">{statusLabel(state)}</div>
        )}
      </section>
    </Shell>
  );
}

function Progress({ value, file }: { value:number; file:{ name:string; fileSize:number } }) { const translate = useT(); const total = file.fileSize; const percent = Math.round(value * 100); return <div className="progress-wrap"><div className="progress-label"><strong>{percent}%</strong><span>{bytes(Math.min(value * total, total))} / {bytes(total)}</span></div><div className="bar" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-label={`Progress for ${file.name}`}><span style={{ width:`${value * 100}%` }} /></div><p className="muted">{value >= 1 ? translate('progress.verified', { name: file.name }) : file.name}</p></div>; }
function Empty({ title, text }: { title:string; text:string }) { const translate = useT(); return <section className="hero compact"><div className="card message"><h1>{title}</h1><p>{text}</p><Link to="/" className="button">{translate('startOver')}</Link></div></section>; }
export default function App() { return <ErrorBoundary><Routes><Route path="/" element={<Home />} /><Route path="/transfer/:transferId" element={<Sender />} /><Route path="/receive/:transferId" element={<Receiver />} /><Route path="*" element={<Shell><Empty title="Page not found" text="This transfer path does not exist." /></Shell>} /></Routes></ErrorBoundary>; }
