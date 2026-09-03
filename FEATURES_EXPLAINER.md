# relay: Features Explainer

This document explains every feature of the `relay` file-transfer application in exhaustive detail, covering design rationale, implementation mechanics, and usability considerations. It is organized so that each feature can be understood independently, with cross-references where features interact.

---

## Table of Contents

1. [Project Vision & Core Promise](#1-project-vision--core-promise)
2. [File Selection & Drag-and-Drop Home Page](#2-file-selection--drag-and-drop-home-page)
3. [Transport Mode Selection](#3-transport-mode-selection)
4. [Transfer Link Generation & QR Codes](#4-transfer-link-generation--qr-codes)
5. [Sender Flow (`/transfer/:id`)](#5-sender-flow-transferid)
6. [Receiver Flow (`/receive/:id`)](#6-receiver-flow-receiveid)
7. [Progress Tracking](#7-progress-tracking)
8. [SHA-256 Integrity Verification](#8-sha-256-integrity-verification)
9. [WebRTC Peer-to-Peer Transport](#9-webrtc-peer-to-peer-transport)
10. [Server Relay Transport](#10-server-relay-transport)
11. [Auto Fallback Transport](#11-auto-fallback-transport)
12. [Chunked Transfer Protocol](#12-chunked-transfer-protocol)
13. [Backpressure & Sliding Window](#13-backpressure--sliding-window)
14. [Capability Token Authentication](#14-capability-token-authentication)
15. [Session Management, State Machine & TTL](#15-session-management-state-machine--ttl)
16. [Rate Limiting & Resource Limits](#16-rate-limiting--resource-limits)
17. [Graceful Shutdown](#17-graceful-shutdown)
18. [Metrics & Observability](#18-metrics--observability)
19. [Security Headers, CORS & CSRF](#19-security-headers-cors--csrf)
20. [Responsive Design & Visual Language](#20-responsive-design--visual-language)
21. [Diagnostics Panel](#21-diagnostics-panel)
22. [File Save Mechanisms](#22-file-save-mechanisms)
23. [Session Persistence (sessionStorage)](#23-session-persistence-sessionstorage)
24. [Error Handling & Recovery](#24-error-handling--recovery)
25. [Testing Strategy](#25-testing-strategy)
26. [CI/CD Pipeline](#26-cicd-pipeline)
27. [Docker & Cloud Run Deployment](#27-docker--cloud-run-deployment)
28. [Firebase Hosting Support](#28-firebase-hosting-support)
29. [Configuration Reference](#29-configuration-reference)
30. [Known Limitations & Future Extensions](#30-known-limitations--future-extensions)

---

## 1. Project Vision & Core Promise

### Design Philosophy
`relay` is designed around a single, uncompromising promise: **temporary by design**. Unlike cloud storage services that persist files indefinitely, `relay` treats every transfer as an ephemeral bridge that self-destructs. The server never writes file bytes to disk — it forwards each binary chunk directly from the sender's WebSocket to the receiver's WebSocket, holding at most one chunk of deduplication state in memory.

This is not a limitation; it is the core value proposition:
- **Zero account friction**: No sign-up, no login. The only credentials are automatically generated capability tokens embedded in the transfer link.
- **Mental model clarity**: The UI communicates temporariness at every layer — the product name, the header tagline "Temporary by design," the footer "Files stream through memory only. Nothing is permanently stored," and the trust badges ("Links expire automatically after 15 minutes").
- **Privacy-first positioning**: Because nothing is stored, there is nothing to subpoena, breach, or accidentally retain.

### Architectural Consequence
The "no storage" promise shapes the entire backend:
- Sessions live in a single in-memory `map[string]*Session` (`backend/internal/transfer/manager.go`). There is no database, no file field, no Firebase Storage integration.
- A disconnect can only recover while the sender still holds the original `File` handle in the browser; the server cannot resume data it never stored.
- The system is single-instance by design: `cloudrun.yaml` pins `maxScale: "1"` because sessions are process-local memory.

### Transport Spectrum
The application offers two data paths with a third automatic mode that chooses between them:
1. **WebRTC Peer-to-Peer** — file bytes stream directly between browsers (DTLS-encrypted), with the Go server acting only as a signaling channel. This is the fast, private path.
2. **Server Relay** — file bytes stream through the Go server over WebSockets using a chunk-acknowledgement window protocol. This is the always-works path.
3. **Automatic** — attempts WebRTC first, falls back to relay within 10 seconds if the direct connection fails or times out.

---

## 2. File Selection & Drag-and-Drop Home Page

### Design Goal
Reduce the path from "I have a file to send" to "I have a link to share" to the absolute minimum number of interactions: one drop or one click.

### The Drop Zone

**Visual design** (`frontend/src/styles.css`):
- A large card with a dashed border (`.dropzone`) occupying the center of the viewport — the dashed border is a universal affordance for "drop here."
- **Hover feedback**: The border transitions to the lime accent color (`#d4f25c`) and the card lifts 3px via `transform: translateY(-3px)` with a 0.2s transition. This provides tactile confirmation that the zone is interactive.
- **Empty state**: A rounded square upload glyph (`↑` on a lime background), the heading "Drop a file here," the subtext "or choose one from this device," and a "Choose a file" button.

**Drag-and-drop mechanics** (`frontend/src/App.tsx:75`):
```tsx
<div className="card dropzone"
     onDragOver={event => event.preventDefault()}
     onDrop={event => { event.preventDefault(); choose(event.dataTransfer.files[0]); }}>
```
- `onDragOver` must call `preventDefault()` for the browser to allow a subsequent `drop` event; without it, the browser would navigate to the dropped file.
- `onDrop` extracts `event.dataTransfer.files[0]` — only the first file is taken (single-file transfers by design).

### The File Input Fallback
A hidden `<input type="file" ref={input} hidden>` is triggered programmatically by `input.current?.click()`. This is the standard accessible pattern: it works with keyboard navigation and screen readers (browsers expose the button as a file-picker activator), while keeping the visual design unconstrained by the native input widget.

### Post-Selection State
Once a file is chosen, the drop zone transforms into a confirmation card:
- **File icon** (`↗` on a mint background) — visually signaling "ready to send."
- **File name** as an `<h2>` heading.
- **Size and MIME type**: e.g., `1.50 MB · application/pdf`, with the type muted. Sizes are formatted by the `bytes()` helper (`App.tsx:12`), which renders KB with one decimal, MB/GB with two decimals, and picks the largest sensible unit.
- **"Change file" button** (secondary style) — allows re-selection without losing the chosen transport mode.
- **"Create transfer link →" button** (primary style) — initiates the transfer.

### Usability Considerations
- The MIME type falls back to `'application/octet-stream'` when `file.type` is empty, and the UI displays "Unknown type" for empty types — honest metadata rather than a guess.
- There is no client-side pre-validation against `MAX_FILE_SIZE` before hashing. For a 10 GB file on a slow connection, the user waits for the full SHA-256 pass before the server can reject it. (Server-side enforcement exists; client-side pre-checks are a listed future improvement.)
- Only one file at a time is supported. Multi-file and folder transfers are explicit non-goals for this MVP.

---

## 3. Transport Mode Selection

### Design Goal
Give users explicit control over the speed-vs-reliability trade-off while providing a default ("Automatic") that handles the complexity invisibly.

### The Three Modes

The transport selector appears on the home page **only after a file is selected** — this ordering prevents users from configuring a mode before they have anything to send, keeping the initial screen uncluttered.

| Mode | Badge | Behavior | Advertisement copy |
|------|-------|----------|--------------------|
| **Automatic** | ✨ | WebRTC first, relay fallback after 10s timeout | "Try peer-to-peer first, then use relay" |
| **Peer-to-Peer** | ⚡ | WebRTC only; fails hard if NAT traversal fails | "Direct connection • Faster • Bypasses server" |
| **Server Relay** | ↔ | WebSocket relay only; WebRTC never attempted | "Route through server • Simple • Extremely reliable" |

### Selector UI Mechanics (`App.tsx:82-145`)
- Rendered as a vertical stack of custom radio cards (`.transport-option`), each with:
  - A custom radio circle (`.transport-radio`) whose inner dot scales from 0 to 1 when selected — a CSS `::after` transform animation.
  - A name line (e.g., "✨ Automatic") and a muted one-line description.
- **Selected state**: lime border + darker background + filled radio dot.
- **Hover state**: semi-lime border + elevated background.
- Clicking a card calls `handleSelectTransport(mode)`, which persists the choice to `localStorage` under `relay:default_transport` — so the preference survives across visits.

### WebRTC Support Detection
```tsx
const supportsWebRTC = typeof RTCPeerConnection !== 'undefined';
```
- In browsers without WebRTC (or where it is disabled by policy), the Automatic and Peer-to-Peer options render with a `.disabled` class (dimmed, click-guarded), and the default mode is forced to `'relay'`.
- A centered error note explains: "WebRTC is not supported in this browser. Peer-to-Peer transfer is disabled."

### Contextual Explainers
Selecting a mode reveals a contextual explainer box (`.transport-explainer`, a left-bordered panel):
- **Peer-to-Peer**: bullets covering direct byte transfer, server-for-signaling-only, and the warning that restrictive firewalls may fail without a TURN server.
- **Server Relay**: bullets covering streaming through the server, near-universal network compatibility, and reliability.

This just-in-time education keeps the initial view clean while still informing the decision.

### Default Resolution Order
1. `localStorage.getItem('relay:default_transport')` (if a valid value)
2. `VITE_DEFAULT_TRANSPORT` environment variable
3. `'auto'`

The receiver never chooses a transport — the receiver always uses Auto so it can follow whichever path the sender's choice and network conditions produce.

---

## 4. Transfer Link Generation & QR Codes

### Design Goal
Make sharing work across both digital channels (copy-paste, chat apps) and physical ones (scanning a screen with a phone camera).

### Creation Flow (`App.tsx:39-66`)
When "Create transfer link →" is clicked:
1. **Busy state**: Button shows "Hashing file…" and disables. This is honest — the client computes a full SHA-256 of the file before creating the transfer (see Section 8).
2. **API call**: `createTransfer({ fileName, fileSize, mimeType, chunkSize, sha256, transport })` POSTs to the backend with an `X-Requested-With: XMLHttpRequest` header (CSRF defense, Section 19).
3. **Response**: `{ id, expiresAt, url, senderToken }`.
4. **Persistence**: The sender context (`url`, `senderToken`, `sha256`, `fileName`, `fileSize`, `transport`) is serialized into `sessionStorage` under `sender:${id}` so a refresh doesn't lose the session (Section 23).
5. **Navigation**: React Router pushes to `/transfer/${id}` with the `File` object and metadata passed via `location.state` (in-memory, survives the navigation but not a refresh).

### Receiver URL Structure
```
{PUBLIC_BASE_URL}/receive/{transferId}#token={receiverToken}
```

**Critical design decision — hash fragment instead of query string**: The receiver capability token lives in the URL *fragment* (`#token=`), never the query string. Browsers do not send fragments to servers, which means:
- The token never appears in backend access logs or intermediary proxy logs.
- The token is not leaked via `Referrer` headers if the receiver navigates onward.
- The frontend reads it locally via `window.location.hash`.

The trade-off: the token is visible in the address bar and browser history on the receiver's device. For an ephemeral, 15-minute capability this is an accepted, documented risk.

### QR Code
- Rendered as an SVG via `qrcode.react` (`QRCodeSVG`), 148×148 px — large enough to scan from a phone at arm's length.
- Encoding the full receiver URL (including the `#token=` fragment) means a scanned link is immediately usable with zero typing.
- The QR sits inside the sender's status card next to the link box, making "show your screen" a first-class sharing path.

### Copy to Clipboard
- A "Copy" button inside the `.linkbox` calls `navigator.clipboard.writeText(url)`.
- The full URL is also rendered as selectable text in the box, so manual selection works if the Clipboard API is unavailable (non-secure contexts, older browsers).

---

## 5. Sender Flow (`/transfer/:id`)

### Design Goal
Keep the sender informed with minimal cognitive load while the transfer proceeds, and survive page refreshes gracefully.

### Session Recovery (Two-Tier)
The `Sender` component reconstructs its context from two sources, in priority order:
1. **Route state** (`location.state`): holds the live `File` object plus `url`, `senderToken`, `sha256`, `transport`. Present only when navigating directly from the home page.
2. **sessionStorage** (`sender:${transferId}`): holds the same metadata except the `File` object (browsers cannot serialize `File` references into sessionStorage). The `JSON.parse` is wrapped in a try/catch so corrupted storage degrades to `null` instead of crashing the component.

**Fallback UX**: If route state exists but the `File` is missing (post-refresh), the sender sees "Choose the original file to resume" with the remembered file name and size, plus a file input. Re-selecting the same file resumes the transfer from the server-tracked chunk index. If even the `senderToken` is unavailable, the page renders an `Empty` state directing the user home.

### Transport Wiring (`App.tsx:271-359`)
A single `useEffect` (deps: `transferId, file, senderToken, sha256Val, details?.transport`) builds the transport:

```tsx
const transport = createTransferTransport(details?.transport || 'auto', {
  role: 'sender', url: socketURL(transferId), id: transferId,
  token: senderToken, file,
  metadata: { fileName, fileSize, mimeType, chunkSize, sha256, transport }
});
```

Three callbacks are registered:
- **onProgress**: `setProgress(prog.bytesSent / file.size)` — a 0..1 ratio.
- **onError**: surfaces the message; in auto mode, if the failure happened after fallback to relay, the message is also stored as `fallbackReason` for the diagnostics panel.
- **onStatusChange**: drives the whole UI lifecycle:
  - `connected | transferring | completed` → stop showing "waiting for receiver".
  - On the first `connected`/`transferring`, the chunk-sending loop starts exactly once (guarded by a `sending` flag), seeded with `transport.getStartChunk()` so a resumed session skips already-acknowledged chunks.

### The Sending Loop
```tsx
const totalChunks = Math.ceil(f.size / chunkSize);
for (let i = startChunk.current; i < totalChunks; i++) {
  if (controller.signal.aborted) break;
  const chunk = await f.slice(i * chunkSize, Math.min(f.size, (i + 1) * chunkSize)).arrayBuffer();
  await t.sendChunk(chunk, i);
}
if (!controller.signal.aborted) await t.complete?.();
```
- `file.slice(...).arrayBuffer()` reads only the current 2 MB slice — the browser streams from disk, so memory stays flat regardless of file size.
- `await t.sendChunk(...)` does not resolve until the chunk is *acknowledged* (relay mode) or *buffered into the data channel* (WebRTC mode), which is what throttles the loop to network speed.
- An `AbortController` makes the loop cancellable: the `useEffect` cleanup calls `controller.abort()` and `transport.close()` on unmount or dependency change, so navigating away never leaks a half-open transfer.

### Sender Status Card
The page shows, top to bottom:
- **QR code** for the receiver URL.
- **Link box** with copy button.
- **Status line** that walks through: `◉ Waiting for receiver to accept…` → `◉ Sending and verifying…` → `✓ Transfer complete`, or `! {error}` on failure.
- **Transport badge** (Section 20): `◌ Establishing connection…` while waiting; `⚡ Connected directly (P2P)` on WebRTC; `↔ Direct connection unavailable • Using relay` when auto-fallback occurred (vs. `↔ Server Relay active` when relay was chosen explicitly).
- **Progress bar** once the receiver has accepted.
- **Diagnostics panel** (Section 21).

---

## 6. Receiver Flow (`/receive/:id`)

### Design Goal
Zero-friction receiving: open link → see what's coming → one click to save. No account, no install, no configuration.

### Token Extraction
```tsx
const receiverToken = (() => {
  const hash = window.location.hash.slice(1);
  return new URLSearchParams(hash).get('token') || '';
})();
```
If the fragment token is absent, the page immediately states "This receiver link is missing its access token" — a clear, non-technical error rather than a silent failure.

### Connection & Offer
The receiver always constructs an `AutoTransport` (it must follow whatever path emerges):
- On connect, the WebSocket `receiver_join` message is sent with the receiver token.
- The server responds with `transfer_offer` carrying the metadata (`fileName`, `fileSize`, `mimeType`, `sha256`, `transport`) and `nextChunk` (the server's expected next index — used for resume).
- The UI transitions to **"Waiting for your approval"**, showing the file name, human-readable size, and "From another device."

### Consent Gate: Accept / Reject
Two buttons are presented before any byte flows:
- **Download file** → `accept()`:
  1. Prepares the save sink via `createReceiverSink()` (Section 22). This happens *first* so that a sink failure (e.g., user cancels the save dialog) aborts cleanly before the transfer starts.
  2. Sends `accept_transfer` to the server, which transitions the session to `Transferring` and notifies the sender (`transfer_accepted`) — which in WebRTC mode triggers the sender's `createOffer()`.
- **Reject** → `transportRef.current?.close()`, which disconnects; the server's detach handling pauses the session and notifies the sender (`receiver_disconnected`).

This explicit consent step is a deliberate privacy/usability feature: the receiver can inspect the file name and size before committing, and the browser's save-file dialog (Chromium) grants a second, OS-level consent.

### Chunk Processing (`App.tsx:459-487`)
```tsx
transport.onChunk(chunk => {
  if (chunk.index < expectedChunk.current) return;        // duplicate: silently drop
  if (chunk.index !== expectedChunk.current) { fail(); }  // out of order: hard fail
  const typed = new Uint8Array(chunk.bytes);
  hash.current.update(typed);                             // incremental SHA-256
  receivedBytes.current += typed.byteLength;
  expectedChunk.current++;
  writes.current = writes.current.then(() => sink.current?.write(typed)); // serialized writes
  setProgress(receivedBytes.current / totalSize);
});
```
Design decisions embedded here:
- **Strict in-order acceptance**: the protocol guarantees ordering (relay: server-enforced exact next index; WebRTC: `ordered: true` data channel), so an out-of-order chunk indicates protocol violation and fails the transfer rather than attempting speculative reassembly.
- **Duplicate tolerance**: a re-sent chunk with a lower index (possible when the sender retries an ACK that the receiver already processed) is dropped without error — this is what makes sender-side retry logic safe.
- **Serialized writes**: `writes.current` is a promise chain. Every chunk write is appended to the chain, guaranteeing that sink writes (which are async, especially for `FileSystemWritableFileStream`) execute in order. This prevents interleaved writes corrupting the output file.
- **Refs, not state**: `expectedChunk`, `receivedBytes`, `hash`, and `writes` are `useRef`s — they mutate per chunk without triggering React re-renders; only `setProgress` triggers rendering (throttled naturally by chunk arrival rate).

### Completion & Verification (`finishReceive`)
Triggered when the transport reports `completed`:
1. `await writes.current` — wait for all queued writes to flush.
2. **Size check**: `receivedBytes === metadata.fileSize`, else fail with a precise message.
3. **Hash check**: `digestHex(hash.current) === metadata.sha256`, else fail (Section 8).
4. `sink.close()` — finalizes the file (or the blob fallback triggers a download via an invisible `<a download>` element).
5. UI shows "Download complete" and 100% progress.

On any verification failure the receiver shows a specific error ("received size did not match" / "SHA-256 verification failed") and does not present the file — integrity is a gate, not a warning.

---

## 7. Progress Tracking

### Design Goal
Continuous, honest, byte-accurate feedback that reduces perceived wait time and makes stalls immediately visible.

### The `Progress` Component (`App.tsx:628`)
```tsx
function Progress({ value, file }: { value: number; file: { name: string; fileSize: number } })
```
Renders:
- **Percentage**: `Math.round(value * 100)%` in bold.
- **Byte counter**: `{bytes(min(value*total, total))} / {bytes(total)}` — e.g., `14.00 MB / 157.29 MB`.
- **Bar**: a `.bar` container with an inner span whose `width` is `value * 100%` — smooth CSS filling.
- **Caption**: the file name; once `value >= 1`, it becomes `✓ {name} verified by SHA-256`, tying completion to integrity confirmation in a single glance.

### What "Progress" Means Per Mode
- **Sender (relay)**: progress advances per *acknowledged* chunk — the sender's `onProgress` fires after ACKs, so the bar reflects confirmed delivery, not merely bytes shoved into a socket.
- **Sender (WebRTC)**: progress advances per chunk handed to the data channel (after its sub-chunks clear the buffered-amount checks), so it reflects end-to-end SCTP acceptance.
- **Receiver (both modes)**: progress is `receivedBytes / fileSize`, computed as chunks arrive and are written. This is the ground truth — when the receiver's bar hits 100% and the hash matches, the transfer genuinely succeeded.

### Edge Cases
- Before the receiver has metadata, the total is unknown; the progress component renders only after the accept gate, so a division-by-zero (`totalSize || 1` guard in the chunk handler) never surfaces in the UI.
- The percentage is rounded, so 99.6% displays as `100%` while the caption still shows the file name (not the "verified" string) until `value` truly reaches 1 — the two signals can disagree momentarily, by design, to avoid claiming verification before it happened.

---

## 8. SHA-256 Integrity Verification

### Design Goal
Guarantee that what arrived is bit-for-bit identical to what was sent — protecting against network corruption, truncation, and (in relay mode) any tampering by the relay itself.

### End-to-End Design
Integrity is computed **incrementally on both sides**, never by re-reading the assembled file:

**Sender — pre-transfer hash** (`frontend/src/services/integrity.ts`):
```ts
export async function hashFile(file: File, chunkSize: number): Promise<string> {
  const hash = createHash();
  for (let offset = 0; offset < file.size; offset += chunkSize) {
    hash.update(new Uint8Array(await file.slice(offset, Math.min(file.size, offset + chunkSize)).arrayBuffer()));
  }
  return digestHex(hash);
}
```
- Uses `@noble/hashes` (`sha256.create()`), an auditable pure-JS implementation.
- Reads the file in the same 2 MB slices that will later be sent, so hashing doubles as an early read-error check.
- The 64-character lowercase hex digest is included in the `POST /api/transfers` metadata; the server validates its *shape* (exactly 64 hex characters) at creation and stores it in the session.

**Receiver — streaming hash**: every accepted chunk is fed into an identical incremental hasher (`hash.current.update(typed)`) in arrival order. Since SHA-256 is sequential, order matters — another reason the receiver enforces strict chunk ordering.

**Receiver — verification**: at completion, `digestHex(hash.current)` is compared against the sender's hash from the `transfer_offer` metadata. Mismatch = hard failure, no file presented.

### Trust Model Nuance
- In **WebRTC mode**, the hash verifies against network corruption; the bytes were already DTLS-encrypted end-to-end.
- In **relay mode**, the hash verifies against both corruption *and* a malicious/compromised relay, because the digest travels in the offer metadata which (while relayed) was computed by the sender from the original file. The relay sees the bytes but cannot forge a file that matches the sender's hash.
- The check is conditional on the sender having provided `sha256`; the sender always does in the current client, and the server validates the format when present.

### Performance Characteristics
- SHA-256 runs at hundreds of MB/s on modern hardware; the browser implementation is the bottleneck but stays off the critical path for typical files (the sender's pre-hash pass is the one user-visible wait, surfaced as "Hashing file…").
- Memory footprint is constant: the hasher maintains a small internal state regardless of file size; only the current 2 MB slice is in memory.

---

## 9. WebRTC Peer-to-Peer Transport

### Design Goal
Move file bytes directly between the two browsers at LAN/WAN line speed with mandatory encryption (DTLS), with the Go server demoted to a signaling relay that never touches payload bytes.

### Signaling Architecture
WebRTC needs an out-of-band channel to exchange session descriptions (SDP) and ICE candidates. `relay` reuses its existing authenticated WebSocket connection for this — no second server, no extra auth:

| Message | Direction | Purpose |
|---------|-----------|---------|
| `webrtc_offer` (SDP) | sender → server → receiver | initiate connection |
| `webrtc_answer` (SDP) | receiver → server → sender | accept connection |
| `webrtc_ice_candidate` | either → server → other | trickle ICE connectivity probes |
| `webrtc_connected` | either → server → other + server | marks session `ActiveTransport = webrtc` |
| `webrtc_failed` / `webrtc_fallback` | either → server → other + server | marks fallback; signals peer to switch to relay |

The Go handler (`backend/internal/websocket/handler.go`) routes these by role — an offer is only accepted from the sender, an answer only from the receiver — so one peer cannot spoof the other's signaling role.

### ICE Configuration
- Default: two Google STUN servers (`stun:stun.l.google.com:19302`, `stun:stun1.l.google.com:19302`).
- Overridable at build time via `VITE_WEBRTC_ICE_SERVERS` (a JSON `RTCConfiguration.iceServers` array), enabling TURN deployment for hostile NATs. Malformed JSON falls back to defaults with a console error rather than breaking the flow.
- Trickle ICE is used: candidates are forwarded the moment `onicecandidate` fires, minimizing connection setup time.

### Data Channel
```ts
this.dataChannel = this.pc.createDataChannel('file-transfer', { ordered: true });
```
- **`ordered: true`** is protocol-critical: the receiver's chunk handler (Section 6) assumes strict sequence, and SCTP ordering guarantees it under this configuration.
- The receiver receives the channel via `ondatachannel` (the sender creates it; the receiver accepts it).
- `binaryType = 'arraybuffer'` so `onmessage` delivers raw buffers.

### Sub-Chunking (SCTP Message-Size Safety)
Browsers cap practical single SCTP message sizes (~64–256 KB depending on vendor). The transport splits each 2 MB application chunk into **60 KB sub-chunks** (`SUB_CHUNK_SIZE = 60 * 1024`), each with a 20-byte binary header:

| Offset | Size | Field |
|--------|------|-------|
| 0 | 8 | `chunkIndex` (uint64 BE) |
| 8 | 4 | `subIndex` (uint32 BE) |
| 12 | 4 | `totalSubChunks` (uint32 BE) |
| 16 | 4 | `payloadLength` (uint32 BE) |
| 20 | … | payload |

**Receiver-side reassembly** (`receiveAssembly` map): sub-chunks are placed into a pre-sized array by `subIndex`; when `receivedCount === totalSubChunks`, the full chunk is concatenated into one buffer, the assembly entry is deleted, and the completed chunk is handed to the application layer — which therefore never sees sub-chunking at all. The special frame `totalSubChunks === 0, subIndex === 1` is a control frame meaning `transfer_complete` delivered over the data channel itself (belt-and-braces alongside the signaling-path complete message).

**Sender-side encoding** is the mirror: `encodeSubChunk` builds the 20-byte header and copies the slice. `decodeSubChunk` validates header length and that `payloadLength` matches the actual buffer — malformed frames fail the transport rather than corrupting the stream.

### Buffer-Based Backpressure
```ts
const BUFFER_THRESHOLD = 4 * 1024 * 1024; // low-water mark
const MAX_BUFFER = 8 * 1024 * 1024;       // high-water mark
this.dataChannel.bufferedAmountLowThreshold = BUFFER_THRESHOLD;
if (this.dataChannel.bufferedAmount > MAX_BUFFER) {
  await new Promise(resolve => { this.dataChannel.onbufferedamountlow = () => { /*...*/ resolve(); }; });
}
```
- Before each sub-chunk send, if the socket buffer exceeds **8 MB**, the sender parks until the browser drains it below **4 MB** (the `bufferedamountlow` event).
- This converts kernel/browser memory pressure into application-level flow control, preventing tab crashes on high-throughput links.
- For the **final chunk**, the threshold is set to 0 and the sender waits for `bufferedAmount === 0` before declaring completion — guaranteeing the receiver has every byte before the complete signal.

### Connection Lifecycle & Failure
- `onconnectionstatechange === 'connected'` → sends `webrtc_connected` (server records active transport; both peers cancel fallback timers).
- `failed` or `closed` → `handleError`, which in Auto mode triggers relay fallback (Section 11); in pure webrtc mode it simply fails the transfer with the state in the message.
- `oniceconnectionstatechange` failures surface identically.

---

## 10. Server Relay Transport

### Design Goal
A transport that works in essentially every network configuration — restrictive NATs, corporate proxies, WebRTC-blocked networks — by streaming bytes through the Go server over plain WebSockets.

### Connection & Join
`WebSocketRelayTransport` wraps a `TransferClient` (a small WebSocket wrapper, Section 12). On connect:
- A `TransferClient` is created (or reused — Auto mode hands over its existing socket) and the join message (`sender_join`/`receiver_join` + token) is sent automatically on open.
- Message and binary listeners are registered. The receiver additionally registers the binary handler that parses framed chunks.

### Control Message Handling (`handleMessage`)
| Message | Effect |
|---------|--------|
| `transfer_offer` | stores `startChunk` (resume point), emits metadata, status → `connected` |
| `transfer_accepted` | status → `transferring` |
| `chunk_ack` | resolves the pending-ack promise for that index; advances `acknowledged` |
| `sender_disconnected` / `receiver_disconnected` | error: "The other device disconnected." |
| `transfer_complete` | status → `completed` |
| `transfer_cancelled` | error: "Transfer cancelled." |
| `error` | error with server-provided friendly message |

### Sliding-Window Send (`sendChunk`)
1. **Window check**: while `index - acknowledged >= windowSize (4)`, poll every 50 ms until the window opens or the transport fails — this is the sender-side half of the protocol's backpressure (Section 13).
2. **Frame & send**: the chunk is framed (16-byte header) and written to the socket; the raw frame is retained in `frames` for potential retry.
3. **Pending-ack registration**: a promise is stored in `pendingAcks[index]` with a retry timer.
4. **Last chunk**: when `index === totalChunks - 1`, wait until `pendingAcks` is empty, then send `transfer_complete` and mark `completed`.

### Retry Logic
- Each in-flight chunk gets a **15-second ACK timeout**.
- On timeout: resend the retained frame, up to **3 attempts total**.
- After the third failure the chunk's promise rejects and the transport enters `failed` — the sender UI surfaces "Timed out waiting for chunk N".
- Retries are safe end-to-end because the receiver drops duplicates (Section 6) and the server itself recognizes exact retransmissions of the last chunk via its `LastChunk` comparison (Section 13).

### Server-Side Relay Path (`handler.go: forward`)
```go
index, payload, ok := transfer.DecodeChunk(frame)
receiver, sender, duplicate, err := handler.Manager.PrepareChunk(id, index, frame)
if err := receiver.SendBinary(frame); err != nil { RecordQueueSaturation(); return false }
RecordBytesRelayed(len(payload))
if !duplicate { RecordChunk(id, index, frame) }
sender.SendControl(chunk_ack(index))
```
Note the ordering: the receiver's write is enqueued **before** the sender's ACK is generated, so an ACK genuinely means "queued for delivery to the receiver" — server-enforced backpressure, not optimistic acknowledgment. If the receiver's per-peer outbound queue (capacity 4) is full, `SendBinary` fails, the transfer is marked failed, and `queueSaturated` increments — a metric that directly signals receiver-side slowness.

---

## 11. Auto Fallback Transport

### Design Goal
Combine both transports into one zero-decision experience: try the fast private path, and if it doesn't connect within a bounded window, switch to the reliable path — on both peers, coordinated, without user intervention.

### Structure
`AutoTransport` is a delegating wrapper holding one `activeTransport` (initially `WebRTCTransport`) plus the shared `TransferClient` used for signaling. All interface methods (`sendChunk`, `onChunk`, `accept`, `complete`, …) forward to the active transport; callbacks are captured and re-wired on switch.

### Fallback Triggers
1. **Sender timeout**: when `transfer_accepted` arrives (receiver just consented), start a `VITE_WEBRTC_CONNECTION_TIMEOUT` (default 10 s) timer. If the data channel hasn't opened by then — fall back.
2. **Receiver timeout**: when `transfer_offer` arrives, start the same timer. If no WebRTC connection materializes (e.g., the sender's offer was lost to a NAT) — fall back. This closes what was previously an unbounded wait on the receiver side.
3. **WebRTC error**: any `failed` status or `onError` from the WebRTC transport (ICE failure, data channel error, connect rejection) triggers fallback immediately.
4. **Peer signal**: receiving `webrtc_fallback` from the remote peer (which may have detected the failure first) triggers the local switch.
5. **Cancellation**: any `webrtc_connected` message or a successful `connected` status clears the timer — fallback never fires after success.

### The Switch (`triggerFallback`)
```ts
if (this.currentActiveTransportMode === 'relay') return;   // idempotent
this.currentActiveTransportMode = 'relay';
if (this.role === 'sender') this.client.send({ type: 'webrtc_fallback' }); // tell the peer
this.activeTransport.close();                              // tear down WebRTC
const relay = new WebSocketRelayTransport(..., this.client); // REUSE the same WebSocket
```
Key design points:
- **Idempotence**: the mode guard makes double-triggers (error + timeout, both peers signaling) harmless.
- **Peer coordination**: only the sender broadcasts `webrtc_fallback` — the receiver follows either the signal or its own triggers, avoiding message storms.
- **Socket reuse**: the new relay transport receives the *existing* `TransferClient`. Both peers stay on their original WebSocket connections; only the payload path changes. No re-join, no re-auth, no new session.
- **Callback rewiring**: all UI callbacks (`onChunk`, `onProgress`, `onMetadata`, `onStatusChange`, `onError`) are re-attached to the relay transport, and subsequent `onChunk`-style registrations made later are forwarded too.
- **State continuity**: the sender's sending loop (`startTransfer`) doesn't restart — it simply continues calling `sendChunk` on the Auto wrapper, which now routes to relay. The server's `nextChunk` tracking and the receiver's `expectedChunk` accounting line up because both sides switch at the same protocol position.

### UX Surfaces
- Badge: "↔ Direct connection unavailable • Using relay" (auto mode) vs. "↔ Server Relay active" (explicit relay choice) — the user is told *that* fallback happened, not buried in it.
- Diagnostics: `Fallback Reason: {message}` shows the originating WebRTC error.

---

## 12. Chunked Transfer Protocol

### Design Goal
Transfer arbitrary-size files as a sequence of independently validated, acknowledged, retryable units — the foundation for backpressure, resume, and integrity.

### Wire Format (Relay Path)
Every binary WebSocket message is a framed chunk with a 16-byte big-endian header:

| Offset | Size | Field |
|--------|------|-------|
| 0 | 8 | `chunkIndex` (uint64 BE) |
| 8 | 8 | `payloadLength` (uint64 BE) |
| 16 | … | payload bytes |

The transfer identity is **not** in the frame: the WebSocket connection itself is authenticated to a specific session (via the join token), so a per-frame ID would be redundant bytes and a spoofing surface.

### Symmetric Implementation
- **Frontend** (`services/transferClient.ts`): `frameChunk` (build via `DataView.setBigUint64`) and `parseChunk` (validate minimum length and that the declared `payloadLength` equals actual bytes — any mismatch throws "Invalid chunk received").
- **Backend** (`internal/transfer/protocol.go`): `EncodeChunk` / `DecodeChunk` mirror the exact same layout and the exact same validation (`length != len(frame)-16 → reject`). The shared `manager_test.go` `TestChunkFrame` proves a corrupted length byte is rejected, and `transferClient.test.ts` proves the JS side rejects the mirrored corruption.

### Chunk Size
- Fixed at **2 MB** (`2 * 1024 * 1024`) in the client.
- The server clamps any client-requested `chunkSize` to its configured `MAX_CHUNK_SIZE` (default 2 MB) at session creation, so a misbehaving client cannot inflate frames.
- The WebSocket read limit (`maxMessageSize = 4 << 20`, 4 MB) on the server comfortably bounds the maximum legal frame (header + 2 MB) with headroom, acting as a hard backstop against oversized garbage.

### Transfer Finalization
- Sender sends `transfer_complete` (control message) only after all chunks are acknowledged (relay) or fully drained (WebRTC).
- Server validates the session is `Transferring`, walks `Transferring → Completing → Completed`, clears the last-chunk dedup buffer, and forwards `transfer_complete` to the receiver.
- The receiver treats completion as the trigger for verification (Section 8), not as proof of success — the hash is the proof.

### Idempotent Resume
Because every chunk is index-addressed and the server tracks `NextChunk` persistently (for the session's lifetime), a reconnecting sender reads `nextChunk` from the fresh `transfer_offer` and resumes exactly where the stream left off. The receiver's duplicate-drop rule (Section 6) and the server's last-chunk dedup (Section 13) make the overlap window — chunks sent but unacknowledged at disconnect time — safe to resend.

---

## 13. Backpressure & Sliding Window

### Design Goal
Ensure the sender can never outrun the receiver by more than a small, bounded window — protecting server memory, browser memory, and TCP/SCTP buffers from unbounded queuing.

### The Three Layers

**Layer 1 — Sender application window (client)**: The sender keeps at most **4 unacknowledged chunks** in flight (`windowSize = 4`, ~8 MB of payload at 2 MB chunks). `sendChunk` blocks while the window is full, polling every 50 ms. The ACK-driven `acknowledged` cursor advances one chunk at a time, in order.

**Layer 2 — Server forwarding gate (`PrepareChunk`)** (`manager.go:391-406`):
```go
if session.State != Transferring || session.Receiver == nil ||
   index != session.NextChunk || len(frame)-16 > session.Metadata.ChunkSize {
    // exact retransmission of the last chunk is admitted as a duplicate
    if session.HasLastChunk() && bytes.Equal(frame, session.LastChunk) {
        return session.Receiver, session.Sender, true, nil
    }
    return nil, nil, false, ErrInvalidState
}
session.NextChunk++;
```
The server admits **only the exact next index** in the exact expected size while in the `Transferring` state. A sender that races ahead of its window gets `ErrInvalidState` and the connection fails — the protocol trusts the client's window but verifies on every frame. The duplicate branch recognizes a *byte-identical* retransmission of the previous chunk (sender retry racing an ACK) and forwards it again idempotently: the receiver drops the duplicate, the sender gets its ACK, and the stream survives ACK loss.

**Layer 3 — Per-peer bounded queues (server)**: Each WebSocket peer has an outbound channel of capacity **4** (`peerQueueSize = 4`). Enqueues are non-blocking with a `default:` case — if the receiver's network is slower than the sender's, `SendBinary` returns `errPeerQueueFull` immediately, the transfer fails fast, and the `queueSaturated` metric increments. This converts slow-consumer risk into an observable, countable event instead of unbounded buffering.

**Layer 4 (WebRTC) — Browser buffer watermarks**: the 8 MB high / 4 MB low `bufferedAmount` gates described in Section 9.

### ACK Ordering Guarantee
On the server, `receiver.SendBinary(frame)` is invoked **before** `sender.SendControl(chunk_ack)` is even attempted — an ACK therefore certifies "accepted into the receiver's outbound queue", never "we hope the receiver will get it". This single ordering choice is what makes the sender's window meaningful.

---

## 14. Capability Token Authentication

### Design Goal
Prevent anyone who merely knows a transfer ID from joining it, while requiring zero accounts. Each transfer carries two independent, role-scoped capabilities.

### Token Lifecycle
1. **Generation**: at `POST /api/transfers`, the server generates two 32-byte tokens with `crypto/rand` (cryptographically secure), base64url-encoded (~43 chars each): `SenderToken` and `ReceiverToken`.
2. **Delivery**: the response body carries `senderToken` (shown only to the sender, persisted only in *their* sessionStorage) and the receiver URL embedding `#token={receiverToken}`.
3. **Storage**: the server stores **only SHA-256 hashes** of the tokens (`SenderTokenHash`, `ReceiverTokenHash`) on the session — never the plaintext.
4. **Presentation**: clients present the plaintext token in the first WebSocket message (`sender_join`/`receiver_join`). Token-free joins fail.
5. **Validation** (`ValidateToken`): runs entirely under the manager read lock; selects the hash for the claimed role, rejects if absent, and compares with `crypto/subtle.ConstantTimeCompare` — no timing side channel.
6. **Role scoping**: the sender token cannot join as receiver or vice versa (tested in `TestCapabilityTokensAreRoleScopedAndNotInSnapshots`).

### Design Properties
- **Compartmentalization**: stealing the receiver link (which contains only the receiver token) does not grant sender privileges, and vice versa. The sender token exists solely in the sender's browser memory/sessionStorage.
- **No snapshot leakage**: `Session.Snapshot()` — the shape returned by `GET /api/transfers/:id` and by offers — structurally cannot carry the hashes (verified by test).
- **One-time-ish exposure**: tokens die with the session (15-minute TTL, or completion/cancel). There is no revocation list because there is nothing long-lived to revoke.
- **Sessions created via `Create` (test path) have no tokens**: their hashes are the zero value, which `ValidateToken` explicitly rejects — the two entry points are deliberately inseparable from their security posture.

### What Tokens Do Not Do
They are bearer capabilities: possession = authority. There is no per-message signing, no replay protection beyond the single-session lifetime, and no sender identity. The token *is* the security model, sized to the threat model of an ephemeral, link-based sharing tool.

---

## 15. Session Management, State Machine & TTL

### Design Goal
Make every legal transition explicit and every illegal one impossible — a transfer's life is a strict finite-state machine, enforced server-side, observable client-side.

### The States (`internal/transfer/state.go`)
```
created → waiting_for_receiver → waiting_for_accept → transferring ⇄ paused → completing → completed
                    ↘                  ↘                  ↘                              ↗
                  cancelled / expired / failed  (reachable from every non-terminal state)
```
- **Terminal states**: `completed`, `cancelled`, `expired`, `failed` — no outgoing edges; the session can no longer carry data.
- `CanTransition(from, to)` is a pure table; `transition()` is the only writer of `session.State`, returning an error (and leaving the state untouched) on illegal moves — verified by `TestStateTransitionsRejectInvalidMoves`.

### Key Transitions & Their Meanings
| Transition | Trigger | Meaning |
|-----------|---------|---------|
| → `waiting_for_accept` | second role attaches (`OnRoleAttached`) | both peers connected; receiver may now consent |
| → `transferring` | `accept_transfer` | data may flow |
| `transferring` → `paused` | either peer disconnects (`Detach`) | stream suspended, awaiting reconnect |
| `paused` → `transferring` | paused peer re-attaches (`Accepted && Paused` branch of `OnRoleAttached`) | automatic resume — the disconnect/reconnect story |
| → `completing` → `completed` | `transfer_complete` from sender | finalization, dedup buffer cleared |
| → `expired` | TTL elapses (checked lazily on access + by the cleanup loop) | resources released, connections closed |

### Attachment Logic (`Session.OnRoleAttached`)
Extracted (from formerly duplicated manager code) into one session method:
- Rejects double-attachment of the same role (`ErrRoleConnected`).
- When **both** roles are connected: `waiting_for_receiver → waiting_for_accept`; or, if the session was `paused` after a prior acceptance, straight back to `transferring` — this asymmetry is what makes resume seamless rather than requiring re-consent.
- The manager's `AttachSender`/`AttachReceiver` set the peer pointer and delegate; all state-writing stays under the session mutex.

### Snapshot Isolation
`Snapshot()` returns a deep-enough copy (identity, metadata, timestamps, state booleans, transport modes) under the session lock. Peers, chunk cursors, dedup buffers, and token hashes are structurally excluded — snapshots are safe to serialize to untrusted clients.

### TTL & Cleanup
- Every session gets `ExpiresAt = CreatedAt + TRANSFER_TTL` (default **15 minutes**).
- Expiry is enforced **lazily** (any `getActive` access checks it) and **periodically** (a background goroutine ticks every minute, calls `Cleanup(now)`, which expires, closes connections, deletes sessions, and increments the `expired` counter).
- Terminal sessions are left in the map until TTL cleanup reaps them (bounded by the 15-minute horizon), keeping completed/cancelled transfers queryable for a grace period — the receiver of a `completed` transfer can inspect final state; a late `GET` on a cancelled transfer reports `cancelled` rather than a confusing 404.

### Concurrency Model
- `manager.mu` (RWMutex) guards the sessions map and limits.
- `session.Mu` guards per-session mutable state.
- Metrics are `atomic.Uint64` counters — readable without any lock.
- `ValidateToken` reads token hashes under the manager read lock (they are written only at creation under the write lock), eliminating the earlier Get-then-Lock race window.

---

## 16. Rate Limiting & Resource Limits

### Design Goal
Make the service cheap to abuse and impossible to crash: every scarce resource has a ceiling, every ceiling has a clear failure mode, and every rejection is observable.

### Creation Rate Limiting (`CreationLimiter`)
- **Scope**: `POST /api/transfers` per client IP.
- **Algorithm**: fixed 1-minute window, per-IP counter, limit `CREATE_RATE_PER_MINUTE` (default **20**).
- **Keying**: the client IP is taken from `RemoteAddr` by default; only when `TRUST_PROXY=true` is the first hop of `X-Forwarded-For` trusted (spoofable header otherwise — the default-distrust posture is tested in `TestClientIPDoesNotTrustForwardedHeaderByDefault`).
- **Failure mode**: HTTP 429 "creation rate limit exceeded" — deterministic and immediately understandable.
- **Memory bound**: the windows map holds one small entry per active IP-minute; stale entries are replaced on window rollover.

### Global Ceilings
| Limit | Default | Enforced at | Failure mode |
|-------|---------|-------------|--------------|
| `MAX_ACTIVE_TRANSFERS` | 1000 | session creation | 400 "active transfer limit reached" |
| `MAX_CONNECTIONS` | 2000 | WebSocket upgrade (`AcquireConnection`) | 503 "connection limit reached" — checked *before* the upgrade handshake |
| `MAX_FILE_SIZE` | 10 GB | metadata validation at creation | 400 (client maps to "This file is too large…") |
| `MAX_CHUNK_SIZE` | 2 MB | creation clamp + per-frame `PrepareChunk` check + 4 MB socket read limit | frame rejected / transfer failed |
| Request body | 64 KB | `http.MaxBytesReader` on the create endpoint | 400 "invalid metadata" |
| Headers | 32 KB | `http.Server.MaxHeaderBytes` | connection-level rejection |
| Per-peer outbound queue | 4 messages | `enqueue` non-blocking | `errPeerQueueFull` → transfer fails, `queueSaturated`++ |

### Connection Accounting Discipline
`AcquireConnection` increments on success; the handler releases exactly once via `defer ReleaseConnection()` — including on upgrade failure, join failure, and every read-loop exit. `Manager.Shutdown` zeroes the counter after closing everything, so a drained server reports clean zeros (asserted by `TestManagerShutdownClosesAndRemovesSessions`).

### Why These Numbers
The defaults are sized for a single small Cloud Run instance (512 MiB): 1000 sessions × (metadata + ~2 MB worst-case dedup buffer on the one active chunk) is the dominant memory term; 2000 connections ≈ 1000 sessions × 2 peers with slack for reconnect overlap. All are environment-tunable precisely because the right ceiling depends on deployment.

---

## 17. Graceful Shutdown

### Design Goal
When the platform says "stop" (Cloud Run sends SIGTERM before killing the instance), end cleanly: no leaked goroutines, no half-closed sockets, no sessions silently lost.

### The Sequence (`cmd/server/main.go`)
1. `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` captures Ctrl+C and SIGTERM into a context.
2. A cleanup goroutine runs the TTL sweep every minute until the context dies — so expiry works even on an idle server.
3. On signal:
   - `manager.Shutdown()` runs *first*: under the manager lock, every session is removed from the map and its `closeConnections()` called — both peers of every active transfer receive a WebSocket close, causing their frontends to surface "The other device disconnected" style errors rather than hanging.
   - `server.Shutdown(ctx-with-10s-timeout)` then drains: stops accepting, waits up to 10 s for in-flight HTTP requests.
4. `ListenAndServe` returns `http.ErrServerClosed`, which is swallowed; `main` exits normally.

### Ordering Rationale
Sessions before server: closing the WebSocket connections first unblocks the per-connection read loops (they get read errors and return), which is what lets the HTTP shutdown actually quiesce. Killing the listener first would strand active transfers in the dark.

### What Is Deliberately Not Preserved
There is no "wait for transfers to finish" mode: SIGTERM kills active streams at up-to-15-minutes TTL semantics. This is the honest trade-off of the zero-storage design — durability would require exactly the persistence the product promises not to have. The sender's resume flow (Section 5) plus a fresh session is the recovery story for interrupted transfers.

### Server-Level Timeouts (defense in depth)
`ReadHeaderTimeout: 10s`, `ReadTimeout: 30s`, `WriteTimeout: WRITE_TIMEOUT` (default **5 minutes**, configurable for long single-request flows), `IdleTimeout: 120s` — a complete slowloris-resistant envelope. WebSocket connections escape `ReadTimeout`'s reach after upgrade (they're hijacked), which is why the WS layer adds its own ping/pong deadlines (Section 10/18 context) — every layer owns its own liveness.

---

## 18. Metrics & Observability

### Design Goal
Give operators a real-time, privacy-clean pulse of the service without introducing a metrics dependency, a scrape framework, or any data that could identify a transfer.

### The Endpoint
`GET /metrics` returns a small JSON document of process-local counters:
```json
{
  "activeTransfers": 3,
  "activeConnections": 6,
  "createdTransfers": 412,
  "completedTransfers": 380,
  "cancelledTransfers": 9,
  "expiredTransfers": 21,
  "failedTransfers": 2,
  "bytesRelayed": 53174609412,
  "queueSaturated": 0
}
```

### Access Control
- **Disabled by default**: with no `METRICS_TOKEN` configured, the endpoint returns 404 "metrics unavailable" — a fresh deployment leaks nothing.
- When enabled, it requires `Authorization: Bearer {token}`, compared with `constant-time compare` — same no-timing-oracle discipline as capability tokens (tested in `TestMetricsRequiresToken`).

### What Each Counter Means Operationally
| Counter | Signals |
|---------|---------|
| `activeTransfers` vs `activeConnections` | ratio should hover near 1:2; divergence = reconnect storms or stuck sockets |
| `created` vs `completed + cancelled + expired + failed` | funnel health; a growing gap = abandoned links never opened |
| `failed` | protocol violations, chunk timeouts, queue issues |
| `bytesRelayed` | egress volume (relay mode only — WebRTC bytes never touch the server) |
| `queueSaturated` | receiver-side slowness: outbound queues of capacity 4 overflowing — the early-warning metric for slow consumers |

### Privacy Properties
The counters are counts and byte sums — never file names, contents, transfer IDs, tokens, or IPs. Metrics reset on restart (documented) — acceptable for a stateless, single-instance MVP; a Prometheus exporter would be the natural production upgrade path.

### Health Check
`GET /healthz` returns **204 No Content** — no body to parse, trivially probeable by Cloud Run/container health checks and load balancers.

---

## 19. Security Headers, CORS & CSRF

### Design Goal
Ship a browser-facing API whose default posture is "deny everything not explicitly needed", with layer-specific policies rather than one blanket header set.

### Security Headers (`httpapi.SecurityHeaders`)
Applied to **every** route (API, health, metrics, WS):
| Header | Value | Purpose |
|--------|-------|---------|
| `X-Content-Type-Options` | `nosniff` | blocks MIME-type sniffing attacks |
| `X-Frame-Options` | `DENY` | no clickjacking via iframes |
| `Referrer-Policy` | `no-referrer` | URLs (which contain the receiver token fragment path) never leak via referrer |
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'` | the API serves JSON only; any content injection attempt is inert by default |

### CORS (exact-origin model)
- `ALLOWED_ORIGINS` (default `http://localhost:5173`) is an **exact-match allowlist** — the middleware compares the full `Origin` header string, character for character, against the configured set.
- Matching origins get `Access-Control-Allow-Origin: {origin}` + `Vary: Origin` (cache-correctness) + `Content-Type` in allow-headers + `GET, POST, OPTIONS` in allow-methods.
- `OPTIONS` preflight short-circuits with 204.
- **Wildcards are rejected at startup** (`validateSettings` returns an error for `*` in `ALLOWED_ORIGINS`), and an HTTPS `PUBLIC_BASE_URL` forces HTTPS origins — misconfiguration fails fast at boot, not at runtime.

### WebSocket Origin Enforcement
Origin checking for the WS route is performed **inside the upgrade path**: `NewHandler` builds a `gorilla/websocket.Upgrader` whose `CheckOrigin` closure validates the `Origin` header against the same allowlist *during the handshake*. A disallowed origin is refused with 403 before the 101 Switching Protocols response — no connection resources are spent. Non-browser clients (no `Origin` header) are permitted, which is correct for a protocol where curl-style tooling may legitimately connect with a token.

### CSRF on Transfer Creation
`POST /api/transfers` additionally requires `X-Requested-With: XMLHttpRequest`:
- Plain HTML forms and "simple" cross-origin requests cannot set custom headers without a CORS preflight — and the preflight will fail for disallowed origins.
- The frontend sets the header on its `fetch` (`api.ts`).
- A same-origin form-post attacker (the residual CSRF surface) is blocked by the header check itself.

### Application-Layer Validation (recap of the web)
File names: non-empty, ≤255 bytes, no `\ / \0 \r \n` (path-traversal and header-injection resistant). MIME: ≤128 bytes, no CRLF. SHA-256: exactly 64 hex characters. Sizes: positive and under limits. Body: capped at 64 KB. Every field the client sends is validated before a session exists.

---

## 20. Responsive Design & Visual Language

### Design Goal
A single, distinctive aesthetic that communicates the product's values — temporary, technical, trustworthy — while working from phones to desktops.

### Visual System (`styles.css`)
- **Palette**: deep green-black background (`#101918`) with a radial gradient to `#28443a` at the top — depth without brightness. The signature **lime accent** (`#d4f25c`) is reserved for primary actions, selection states, and confirmation; muted sage (`#94a7a0`, `#aabbb5`) carries secondary text.
- **Typography**: *Manrope* (sans) for UI text at weights 400–800; *DM Mono* for eyebrow labels, diagnostics, and anything machine-flavored (`TRANSFER METHOD`, `YOUR LINK`) — a deliberate "technical instrument" tone.
- **Cards**: `rgba(27,43,39,.86)` on translucent borders with a soft, large-radius shadow — content floats on the gradient.
- **Dark-first**: `color-scheme: dark` is declared so form controls, scrollbars, and the file picker render dark natively.

### Layout & Responsiveness
- `main` is capped at 1120 px and centered; the hero column at `min(680px, 100%)`.
- Fluid headline sizing via `clamp(36px, 6vw, 68px)`; `min-width: 320px` floor keeps the layout sane on the smallest phones.
- The hero sits at `margin-top: 12vh` (9vh on compact pages) — vertical rhythm that feels intentional rather than top-stacked.
- Buttons are 13 px-padded pill-ish rectangles with a brightness filter on hover; `:disabled` fades and switches to a wait cursor — states are always legible.

### Component-Level Design Decisions
- **Dashed dropzone border** — the universal "drop target" glyph; animating to lime on hover teaches interactivity instantly.
- **Transport badges** are color-coded pills: lime/bright for WebRTC success (`⚡ Connected directly (P2P)`), neutral gray for relay, dashed/ghost for the connecting state — transport state is glanceable without reading.
- **Progress bar** is a hairline container with a lime fill; the label pairs percentage with byte counts (`14.00 MB / 157.29 MB`) — precision for large files where "45%" is ambiguous.
- **Linkbox** renders the full URL as text with an inline Copy button — copy affordance and manual fallback in one element.
- **Trust badges** on the home page ("No permanent storage / Links expire automatically after 15 minutes", "Streamed in chunks / Integrity checked with SHA-256") state the privacy contract in plain language at the moment of decision.

---

## 21. Diagnostics Panel

### Design Goal
Expose connection internals for debugging and trust ("is this really peer-to-peer?") without cluttering the primary UI — progressive disclosure.

### UI Mechanics (`App.tsx` DiagnosticsPanel)
- A quiet text toggle (`▶ Show Connection Details` / `▼ Hide Connection Details`) under the transfer card; content is a monospace key/value grid (`.diagnostics-row`, 140 px label column) that reads like instrument output.
- Both sender and receiver render the panel, so either side can diagnose.

### Reported Fields
| Field | Meaning |
|-------|---------|
| Transport Mode | what was chosen on the home page (`auto`/`webrtc`/`relay`) |
| Active Transport | what is *actually* carrying bytes — `Peer-to-Peer (WebRTC)` in lime or `Server Relay (WebSockets)` — the anti-"trust me" field |
| Connection Status | the transport lifecycle: `new → connecting → connected → transferring → completed/failed/closed` |
| WebRTC Supported | `YES`/`NO` — capability truth for the current browser |
| ICE Gathering / Data Channel | live states (`connected`/`n/a`, `open`/`connecting`) when WebRTC is active |
| Server | `Signaling & Handshake Only` (WebRTC) vs `Active Byte Relay` — answers "does the server see my bytes?" inline |
| Fallback Reason | the originating WebRTC error message when auto-fallback fired — the "why" behind a relay badge |

### Design Rationale
- The Active Transport field is the product's honesty feature: the README claims E2E for P2P and not for relay, and this panel lets any user *verify* which mode they got instead of taking the marketing's word.
- Monospace + key/value grid mirrors developer tooling, signaling "raw truth" rather than marketing copy; the toggle keeps it out of the happy path.

---

## 22. File Save Mechanisms

### Design Goal
Write incoming bytes to disk as they arrive — never assembling whole files in browser memory — while degrading gracefully on browsers without the File System Access API.

### Primary Path: File System Access API (`receiverStorage.ts`)
On Chromium browsers, `showSaveFilePicker()` is invoked at accept time with:
- `suggestedName`: the sender's file name.
- `types`: a single accept entry keyed by the sender's MIME type, mapping to the file's extension — the OS dialog filters sensibly.

The returned handle yields a `FileSystemWritableFileStream`:
- Each chunk's `Uint8Array` is `write()`-en directly — data lands on disk incrementally through the serialized write chain (Section 6).
- `close()` finalizes the file atomically (the stream commits on close).
- **Consent model**: the picker dialog is an OS-level save grant — the user chooses the destination and name, giving a second, stronger consent than the in-page "Download file" button.
- If the user cancels the picker, `createReceiverSink` throws *before* `accept_transfer` is sent — the transfer never starts with nowhere to write.

### Fallback Path: In-Memory Blob
Where the picker API is absent (Firefox, Safari):
- Chunks accumulate as `BlobPart`s in an array (the sink exposes `blobParts` so the UI can track them).
- At completion, `new Blob(parts, { type: mimeType })` is materialized and downloaded via a synthetic `<a download>` click — the standard programmatic-download pattern.
- **Hard cap of 250 MB**: above it, `createReceiverSink` throws a specific, actionable error ("Use a Chromium-based browser or another device") rather than letting a tab quietly OOM. This turns a silent catastrophic failure into an explicit, early, explained one.

### Why This Split Matters
The E2E test (`e2e/transfer.spec.ts`) deliberately disables `showSaveFilePicker` on the receiver page — exercising the blob path — because that path has the most failure modes (memory, ordering, download triggering) and Playwright can intercept its `download` event for byte-exact verification. The picker path is exercised implicitly by the primary happy path and manual use.

---

## 23. Session Persistence (sessionStorage)

### Design Goal
Survive the most common accidents of the sender's journey — refreshes, tab reloads, accidental navigation — without any server-side sender state.

### What Is Stored
After creating a transfer, the sender serializes to `sessionStorage["sender:{id}"]`:
```json
{ "url", "senderToken", "sha256", "fileName", "fileSize", "transport" }
```
`sessionStorage` (not `localStorage`) is deliberate:
- **Tab-scoped**: the sender token dies with the tab — a second tab or a later browsing session cannot resurrect a stale sender capability.
- **Survives refresh**: the Sender component's recovery path re-reads it and (after the user re-selects the original file, since `File` handles cannot be serialized) resumes from the server's `nextChunk`.

### Hardening
The recovery read wraps `JSON.parse` in a try/catch (corrupted or hand-tampered storage degrades to `null` → the "session unavailable" empty state) — a crashed white screen from bad storage is treated as a bug, not an acceptable failure.

### Receiver Side
The receiver persists nothing: its entire context (transfer ID + token) is the URL, making receiver links forwardable and device-independent by construction.

---

## 24. Error Handling & Recovery

### Design Goal
Every failure has a specific, human-readable message and a defined recovery path — no generic "something went wrong", no dead ends, no silent hangs.

### Server-Friendly Errors (`websocket/handler.go`)
Join-time failures are translated via `friendlyError` before being sent to the client:
| Cause | User sees |
|-------|-----------|
| `ErrTransferNotFound` | "This transfer was not found." |
| `ErrTransferExpired` | "This transfer has expired." |
| `ErrRoleConnected` | "This transfer role is already connected." |
| anything else | "Could not join this transfer." |

The frontend's `waitForAck` rejects on any `error`/`transfer_cancelled` message with the server's message — so server-side rejections propagate with their text intact.

### Client-Side Error Surfaces
| Situation | Message | Recovery |
|-----------|---------|----------|
| Oversized file (server 400) | "This file is too large for the configured limit." | pick smaller file / raise server limit |
| API unreachable | "The transfer service is unavailable." | retry / check backend |
| Missing receiver token | "This receiver link is missing its access token." | obtain full link |
| Out-of-order chunk | "Transfer failed: chunks arrived out of order." | protocol violation — restart transfer |
| Size mismatch at completion | "Transfer failed: received size did not match." | corruption — restart transfer |
| Hash mismatch | "Transfer failed: SHA-256 verification failed." | corruption/tampering — restart transfer |
| Chunk ACK timeout ×3 | "Timed out waiting for chunk N" | automatic 3 retries, then fail |
| Peer disconnect mid-transfer | "The other device disconnected." | sender-side resume: re-select file, continue |
| Browser can't handle >250 MB blob | "This browser cannot safely save files larger than 250 MB…" | switch to Chromium |
| WebRTC failure (auto mode) | (silent fallback) + "Fallback Reason: …" | automatic relay fallback |

### The Resume Story (honest limits)
- A **sender** disconnect is recoverable: the session pauses (`paused`), the server retains `NextChunk`, the sender returns, re-attaches, and the state machine auto-resumes to `transferring`; the UI restarts its loop from `getStartChunk()`.
- A **receiver** disconnect pauses similarly, but received-and-written bytes are not retransmitted (the sink continues from `expectedChunk`), so receiver resume works only if the same tab/sink persists.
- Server restart or TTL expiry is terminal by design — durability would require storage, which is the one thing this product refuses.

---

## 25. Testing Strategy

### Design Goal
Test the protocol contract from three altitudes: unit (frame math, hashing, state machine), integration (real WebSockets against the real handler), and end-to-end (two real browser pages moving a real file through a real server).

### Backend (`go test`)
- **Transfer package** (`manager_test.go`): unique IDs and expiry; chunk frame round-trip *and* corrupted-length rejection; illegal state transitions leave state untouched; accept-only-after-both-attach; detach/reconnect; **paused→transferring resume**; exact-last-chunk duplicate admission (and rejection of *altered* duplicates); active-transfer and connection limit enforcement; unsafe metadata rejection (`../secret` path traversal, malformed SHA-256); metrics accounting; shutdown clears everything; token role-scoping and snapshot non-leakage.
- **HTTP API** (`handlers_test.go`): metrics requires bearer token; client-IP resolution distrusts `X-Forwarded-For` unless `TrustProxy`.
- **WebSocket handler** (`handler_test.go`): real `httptest.Server` + gorilla dialer — origin rejected **at upgrade** (403); allowed origin joins and receives `transfer_offer`; invalid token/type/ID-mismatch produce friendly errors; the **full protocol flow** (both joins → accept → chunk → ACK → forward → complete); detach notifies the peer (`receiver_disconnected`); WebRTC offer/answer relay with SDP intact; cancel propagates to both peers.
- **Race detector**: CI runs `go test -race ./...`; the concurrency-sensitive packages pass clean under it (the lock-scoped `ValidateToken` fix was verified this way).

### Frontend (`vitest`)
- **Framing** (`transferClient.test.ts`): round-trip index+bytes; rejection of truncated frames and length-field corruption — the JS mirror of the Go frame tests, pinning the wire format from both ends.
- **Integrity** (`integrity.test.ts`): known SHA-256 vector (`"hello"` → `2cf24d…`); incremental `hashFile` over a real `File` — proving the streaming hash equals a whole-file hash.
- **Transports** (`transport.test.ts`): with a mock client and mocked `RTCPeerConnection` — `transfer_offer` → `connected`; binary chunk parsing on the receiver; `accept()` sends `accept_transfer`; `complete()` sends `transfer_complete`; cancelled transfers surface errors; Auto mode starts on WebRTC and **switches to relay on `webrtc_fallback`**.

### End-to-End (`playwright`)
`e2e/transfer.spec.ts` performs the definitive proof:
1. Sender page: set a real file buffer (`"real local transfer payload"`), create the link, assert `YOUR LINK` appears.
2. Extract the receiver URL from the link box.
3. Open a **second page** in the same context, with `showSaveFilePicker` deliberately undefined (forcing the blob path).
4. Receiver: click "Download file", await the browser **download event**, read the downloaded file from disk.
5. Assert `downloaded.equals(original)` — **byte-exact** end-to-end verification through the full stack (Vite dev server + Go server, both auto-started by the Playwright config with port reuse).
6. Sender page must show "Transfer complete" within 15 s.

The `webServer` config starts both the frontend (`npm run dev`) and the backend (`go run ./cmd/server`) and reuses already-running instances — so the E2E suite works locally against a live dev session and in CI from cold.

---

## 26. CI/CD Pipeline

### Design Goal
Three parallel GitHub Actions jobs that answer the only three questions that matter on every push and PR: does the backend work, does the frontend work, does it containerize.

### `backend` job
`go test` → `go vet` → `go test -race` → `go build ./cmd/server`, on Go 1.22.x / ubuntu-latest. The race step runs the full suite under the detector — concurrency regressions fail the build, not production.

### `frontend` job
`npm ci` (locked install) → `npm test` (vitest) → `npm run build` (tsc type-check + vite production build), on Node 20 with npm caching keyed to `package-lock.json`. The build step doubles as the TypeScript compiler gate — type errors are CI failures.

### `container` job
Docker Buildx builds `backend/Dockerfile` (tag `relay-backend:ci`, no push) on every push/PR — proving image buildability even when no deployment is intended, and catching Dockerfile rot (base image changes, layer caching breakage) at PR time.

### Trigger Model
`push` to `main` and all `pull_request`s — the pipeline is validation-only by design; deployment remains a deliberate, documented manual step (Section 27), matching the project's single-instance, operator-gated posture.

---

## 27. Docker & Cloud Run Deployment

### Dockerfile (multi-stage, distroless)
```
golang:1.22-alpine  → CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'
gcr.io/distroless/static-debian12:nonroot → copy binary, USER nonroot, EXPOSE 8080
```
- **Static, stripped binary**: no C dependencies, no symbol tables — minimal image and attack surface.
- **Distroless nonroot**: no shell, no package manager, no root — the container can essentially only execute the server binary.
- Dependency layers (`go.mod`/`go.sum` copy + `go mod download`) precede source copy for maximal layer-cache hits.

### Cloud Run Manifest (`cloudrun.yaml`)
| Setting | Value | Rationale |
|---------|-------|-----------|
| `maxScale` | **"1"** | sessions are process-local memory; horizontal scale would strand half of each transfer on the wrong instance. Pinned intentionally, documented loudly. |
| `containerConcurrency` | 200 | headroom for many idle WS connections |
| `timeoutSeconds` | 900 | Cloud Run's long-request ceiling, matching long transfers |
| resources | 1 CPU / 512 MiB | sized to the in-memory session/chunk budget |
| env | PORT, PUBLIC_BASE_URL, ALLOWED_ORIGINS, TRUST_PROXY=false, limits | production values differ from dev defaults; the manifest makes them explicit |

The README documents both deployment paths: `gcloud run deploy --source .` (builds the container from source, quickstart) and `docker build/push` + `gcloud run services replace cloudrun.yaml` (repeatable, versioned). TLS termination and trusted-proxy behavior are delegated to the platform — `TRUST_PROXY` stays false unless the operator consciously enables header trust.

---

## 28. Firebase Hosting Support

### Design Goal
Host the SPA as close to users as possible with correct SPA routing and hardened response headers, with zero server-side logic.

### `firebase.json`
- **`public: "dist"`** — deploys the Vite production build directly.
- **SPA rewrites**: `"source": "**" → "/index.html"` — `/receive/:id` and `/transfer/:id` deep links (the entire sharing model) resolve to the app instead of 404ing.
- **`Cache-Control: no-cache`** on everything: HTML must always be fresh so new deployments reach users immediately; hashed asset filenames (Vite's `index-BeepMh7Q.js`-style outputs) make asset caching safe despite the blanket header.
- **Security headers** deployed at the CDN edge: `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and a `Permissions-Policy` denying camera/microphone/geolocation — the frontend needs none of those capabilities, so the policy states it.
- `.firebaserc` + the documented `firebase deploy` flow make hosting a two-command operation after `npm run build`.

The split-horizon architecture — static SPA on Firebase Hosting, WebSocket relay on Cloud Run — means the frontend's `VITE_API_URL`/`VITE_WS_URL` point at the backend origin, and the backend's `ALLOWED_ORIGINS` lists the Firebase origin: the two services meet only over CORS-checked HTTP/WebSocket.

---

## 29. Configuration Reference

### Backend Environment Variables (`internal/config`)
| Variable | Default | Effect |
|----------|---------|--------|
| `PORT` | `8080` | listen port (binds `0.0.0.0`) |
| `PUBLIC_BASE_URL` | `http://localhost:5173` | prefix for generated receiver links; **must be absolute** (validated at boot) |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | comma-separated exact origins; `*` rejected; must be HTTPS if base URL is HTTPS |
| `TRANSFER_TTL` | `15m` | session lifetime (Go duration or seconds) |
| `MAX_FILE_SIZE` | `10737418240` (10 GiB) | per-transfer ceiling |
| `MAX_CHUNK_SIZE` | `2097152` (2 MiB) | client chunk sizes clamped to this |
| `MAX_ACTIVE_TRANSFERS` | `1000` | concurrent sessions |
| `MAX_CONNECTIONS` | `2000` | concurrent WebSockets |
| `CREATE_RATE_PER_MINUTE` | `20` | transfer creations per client IP per minute |
| `METRICS_TOKEN` | *(empty → metrics disabled)* | bearer token for `GET /metrics` |
| `TRUST_PROXY` | `false` | trust `X-Forwarded-For` for rate-limit keying |
| `WRITE_TIMEOUT` | `5m` | `http.Server.WriteTimeout` (sized for long transfers) |

Invalid values fall back to defaults silently; structurally invalid *combinations* (wildcard origins, non-absolute base URL, HTTP origins with HTTPS base) fail startup loudly.

### Frontend Build Variables (Vite `import.meta.env`)
| Variable | Default | Effect |
|----------|---------|--------|
| `VITE_API_URL` | `http://localhost:8080` | HTTP API base |
| `VITE_WS_URL` | *(derived: API URL with `http→ws`)* | WebSocket base |
| `VITE_WEBRTC_ICE_SERVERS` | Google STUN ×2 | JSON `iceServers` array; malformed JSON falls back to defaults |
| `VITE_WEBRTC_CONNECTION_TIMEOUT` | `10000` (ms) | auto-mode fallback deadline (both roles) |
| `VITE_DEFAULT_TRANSPORT` | `auto` | transport default before any localStorage preference |

---

## 30. Known Limitations & Future Extensions

### Honest Limitations (by design or MVP scope)
- **Single instance only**: in-memory sessions pin the deployment to `maxScale: 1`; scaling out requires session routing or a different transport architecture.
- **No durable resume**: server restarts and TTL expiry are terminal; only the sender-side, same-session pause/reconnect path is resumable.
- **Relay is not E2E**: in relay mode the server sees every byte (though SHA-256 still catches tampering); only WebRTC mode is end-to-end encrypted — the UI says so, the README says so, the diagnostics panel proves it.
- **Receiver token in URL fragment**: protected from logs and referrers, but visible in the receiver's address bar/history for the session's lifetime.
- **Single file per transfer**: no multi-file, folder, or parallel-chunk transfers.
- **Fixed 2 MB chunks / window of 4**: simple and safe, not adaptive to link quality.
- **Metrics are process-local JSON**: no history, no alerting — a Prometheus exporter is the upgrade path.
- **No TURN server shipped**: strict NATs will fail P2P and (correctly) fall back to relay; production P2P quality requires deploying TURN via `VITE_WEBRTC_ICE_SERVERS`.
- **Sender pre-hash wait**: the full SHA-256 pass runs before link creation — the one user-visible cost of the integrity design on very large files.

### Documented Future Extensions
The README closes with the roadmap: passwords on transfers, multiple files and folders, authentication, device pairing, parallel chunks, adaptive chunk sizing, and durable resumability — each of which extends this feature set without breaking its "temporary by design" contract.

---

*This document reflects the codebase as of the production-readiness hardening sprint: all P0 (race fix, upgrade-time origin enforcement, receiver fallback timeout, configurable WriteTimeout) and P1 (state-machine extraction, backend WS integration tests, frontend transport tests, storage hardening, CSRF header, hash-fragment tokens) items complete.*





