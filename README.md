# relay: temporary chunked file transfer

`relay` moves one file between two browsers through a Go WebSocket relay. The backend forwards each binary chunk directly from sender to receiver. It has no file field, file store, temporary file, database, or Firebase Storage integration. Everything is streamed through memory and forgotten when the transfer ends.

## Features

- **Temporary by design** — links expire automatically (default 15 minutes); no permanent storage anywhere.
- **Two transports, user-selectable** — WebRTC peer-to-peer (bytes never touch the server, end-to-end encrypted) or server relay (works through any NAT), plus an **Automatic** mode that tries P2P first and falls back to relay within 10 seconds.
- **SHA-256 integrity verification** — the sender hashes incrementally before sending; the receiver hashes incrementally as chunks arrive and verifies before declaring success.
- **Chunked streaming with backpressure** — 2 MB frames, 4-chunk sliding window, per-chunk ACKs, resumable on disconnect while the sender still holds the file.
- **Capability-token security** — role-scoped sender/receiver tokens, stored only as SHA-256 hashes, compared in constant time.
- **QR code sharing** — the receiver link is rendered as a scannable QR code with a copy button.
- **Memory-efficient receiving** — uses the File System Access API writable stream where supported; Blob fallback capped at 250 MB elsewhere.

## Structure

- `backend/`: Go 1.22 HTTP/WebSocket server, in-memory transfer manager, framing, tests, and Dockerfile.
- `frontend/`: React + TypeScript + Vite app with send, receive, QR, progress, diagnostics, and responsive states.

## Prerequisites

- **Go**: 1.22 or higher
- **Node.js**: 20 or higher
- **npm**: 10 or higher

## Quick start (local development)

You need two terminals: one for the Go backend, one for the React frontend.

### Step A: Start the Go backend

```powershell
cd backend
go mod tidy
$env:PUBLIC_BASE_URL="http://localhost:5173"   # Linux/macOS: export PUBLIC_BASE_URL="http://localhost:5173"
go run ./cmd/server
```

The backend listens at `http://localhost:8080`.

### Step B: Start the React frontend

```powershell
cd frontend
npm install
npm run dev
```

The UI runs at `http://localhost:5173`; the API runs at `http://localhost:8080`.

## Running tests

### Backend

```powershell
cd backend
go test ./...
go vet ./...
go test -race ./...
```

### Frontend unit tests

```powershell
cd frontend
npm test
```

### Playwright end-to-end (E2E) tests

`npm run test:e2e` serves the **production build** (`npm run preview`) and starts the Go server, then runs a headless Chromium browser through a full sender → receiver transfer with byte-exact download verification, plus automated accessibility scans. Build the frontend first, then run:

```powershell
cd frontend
npm run build
npm run test:e2e
```

The test runner reuses already-running servers (see `frontend/playwright.config.ts`), so you can also run it against a live dev session on port 5173.

## Configuration

### Backend environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Listen port (binds `0.0.0.0`). |
| `PUBLIC_BASE_URL` | `http://localhost:5173` | Absolute URL used to prefix generated receiver links. |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | Comma-separated exact CORS origins. Wildcards are rejected at startup; must be HTTPS when `PUBLIC_BASE_URL` is HTTPS. |
| `TRANSFER_TTL` | `15m` | Session lifetime (Go duration or seconds). |
| `MAX_FILE_SIZE` | `10GB` (`10737418240`) | Per-transfer file size ceiling. |
| `MAX_CHUNK_SIZE` | `2MB` (`2097152`) | Client chunk sizes are clamped to this. |
| `MAX_ACTIVE_TRANSFERS` | `1000` | Concurrent session ceiling. |
| `MAX_CONNECTIONS` | `2000` | Concurrent WebSocket connection ceiling. |
| `CREATE_RATE_PER_MINUTE` | `20` | Transfer creations allowed per client IP per minute. |
| `METRICS_TOKEN` | *(empty)* | Bearer token for `GET /metrics`; empty disables the endpoint. |
| `TRUST_PROXY` | `false` | When `true`, trusts `X-Forwarded-For` for rate-limit keying. Enable only behind a trusted proxy. |
| `WRITE_TIMEOUT` | `5m` | HTTP server write timeout, sized for long-running transfers. |

The backend validates file names, MIME metadata, SHA-256 format, transfer size, active sessions, and WebSocket connections.

### Frontend environment variables (`.env` / `.env.production`)

| Variable | Default | Description |
|----------|---------|-------------|
| `VITE_API_URL` | `http://localhost:8080` | HTTP API base URL. |
| `VITE_WS_URL` | derived from API URL | WebSocket base URL (e.g. `ws://localhost:8080`). |
| `VITE_WEBRTC_ICE_SERVERS` | Google STUN ×2 | JSON `RTCConfiguration.iceServers` array. Default: `[{"urls":"stun:stun.l.google.com:19302"},{"urls":"stun:stun1.l.google.com:19302"}]` |
| `VITE_WEBRTC_CONNECTION_TIMEOUT` | `10000` | Milliseconds to wait for a WebRTC handshake before falling back to server relay. |
| `VITE_DEFAULT_TRANSPORT` | `auto` | Default transport (`auto`, `webrtc`, or `relay`) before any user preference. |
| `VITE_LOG_LEVEL` | `debug` in dev, `warn` in prod | Minimum log level (`debug`, `info`, `warn`, `error`). |

## Protocol

`POST /api/transfers` returns a private `senderToken` and a receiver URL containing only the receiver capability token, carried in the URL **hash fragment** (`#token=...`) so it is never sent to the server or leaked in logs or Referrer headers.

The browser sends its capability in the first JSON join message — `sender_join` or `receiver_join` to `/ws/:transferId`. Tokens are generated with cryptographically secure random bytes, stored only as SHA-256 hashes in memory, and checked with constant-time comparison. The server sends `transfer_offer` (with metadata and the next chunk index for resume). The receiver sends `accept_transfer` or `reject_transfer`.

After acceptance, binary messages use a 16-byte big-endian header followed by raw bytes: bytes 0..7 are the unsigned chunk index, bytes 8..15 are the unsigned payload length, and bytes 16 onward are the payload.

The server verifies frame length, chunk size, transfer state, and exact next index, writes the frame to the receiver before acknowledging the sender, and therefore provides backpressure. The browser allows four unacknowledged chunks. `transfer_complete` is sent after all ACKs.

Other HTTP endpoints: `GET /api/transfers/:id` returns the session snapshot; `GET /api/limits` returns configured `maxFileSize`/`maxChunkSize` (used for client-side pre-validation); `GET /healthz` returns 204; `GET /metrics` is token-protected (see below).

## Transports

Users toggle the transfer method on the Home page:

1. **Automatic (Recommended)** — attempts WebRTC peer-to-peer first. If the NAT traversal handshake times out (strict firewalls) or errors, it gracefully falls back to **Server Relay** within 10 seconds.
2. **Peer-to-Peer** — restricts communication to WebRTC data channels. If NAT traversal fails, the transfer fails. No file bytes traverse the Go server.
3. **Server Relay** — bypasses WebRTC and routes chunks through the Go WebSocket relay. Works in almost any network configuration.

The transport layer is pluggable (`frontend/src/transport/`): WebRTC uses a 60 KB sub-chunking layer over an ordered data channel with buffer-based backpressure; the relay path uses the chunk/ACK window protocol. The status card and diagnostics panel show which transport is actually active.

## Metrics

`GET /metrics` returns process-local JSON counters for `activeTransfers`, `activeConnections`, `createdTransfers`, `completedTransfers`, `cancelledTransfers`, `expiredTransfers`, `failedTransfers`, `bytesRelayed`, and `queueSaturated`. Metrics are held in memory and reset when the backend instance restarts. They never contain file names, file contents, transfer IDs, or tokens. The endpoint requires `Authorization: Bearer {METRICS_TOKEN}` and returns 404 when no token is configured.

## Security and limitations

- Transfer IDs are 128-bit cryptographically random values. The server validates size, role, state, expiry, framing, order, and chunk size.
- `POST /api/transfers` requires an `X-Requested-With: XMLHttpRequest` header (CSRF defense); WebSocket upgrades reject disallowed `Origin` headers during the handshake.
- **Relay is not end-to-end encrypted.** The server sees file bytes while forwarding (though SHA-256 still catches tampering). Only WebRTC mode is end-to-end encrypted.
- A disconnect can only recover while the sender still has the original `File`; the server cannot resume data it never stored.
- Sessions exist on one backend instance (process-local memory); `maxScale` must stay `1` until session routing or a different transport architecture is introduced.
- The receiver uses `showSaveFilePicker()` and a writable stream where supported, so large files do not need to be assembled in JavaScript memory. Browsers without that API use a Blob fallback capped at 250 MB.
- The server uses bounded request headers, read/write/idle timeouts, security response headers, exact configured origins, WebSocket ping/pong deadlines, connection limits, and graceful shutdown on `SIGTERM`. Cloud Run or the reverse proxy must terminate HTTPS and forward only trusted `X-Forwarded-For` headers. Keep `/metrics` behind platform authentication in deployments where operational counters should not be public.

No networked application can honestly guarantee complete safety. Production operators still need TLS, authenticated infrastructure access, trusted-proxy configuration, monitoring, dependency updates, and an abuse-response plan. Do not expose the backend broadly without reviewing those controls.

## Deployment

### Firebase Hosting (frontend)

```powershell
cd frontend
npm install
$env:VITE_API_URL="https://your-go-backend.example.com"   # Linux/macOS: export VITE_API_URL=...
$env:VITE_WS_URL="wss://your-go-backend.example.com"
npm run build
firebase login
firebase init hosting
firebase deploy
```

The included `frontend/firebase.json` rewrites `/receive/<id>` and `/transfer/<id>` to the SPA entrypoint and applies security headers at the CDN edge.

### Cloud Run (backend)

Quickstart (builds from source):

```powershell
cd backend
gcloud run deploy relay-backend --source . --region us-central1 --allow-unauthenticated --set-env-vars "ALLOWED_ORIGINS=https://your-project.web.app,PUBLIC_BASE_URL=https://your-project.web.app,TRANSFER_TTL=15m,MAX_FILE_SIZE=10737418240,MAX_CHUNK_SIZE=2097152"
```

Repeatable deployment (build, push, apply):

```powershell
docker build -t REGION-docker.pkg.dev/PROJECT_ID/REPOSITORY/relay-backend:TAG ./backend
docker push REGION-docker.pkg.dev/PROJECT_ID/REPOSITORY/relay-backend:TAG
gcloud run services replace backend/cloudrun.yaml --region us-central1
```

The container binds to `0.0.0.0:$PORT`, runs as a non-root distroless user, and uses only process memory.

Future extension points include passwords (end-to-end encryption in relay mode), multiple files, folders, authentication, pairing, parallel chunks, adaptive chunk sizing, and durable resumability.

## Related documents

- `FEATURES_EXPLAINER.md` — exhaustive design and usability walkthrough of every current feature.
- `IMPROVEMENT_PLAN.md` — the long-term improvement and new-feature roadmap (Tracks A–E, phased).
- `PRODUCTION_READINESS_PLAN.md` — record of the P0/P1 hardening completed for production validation.
