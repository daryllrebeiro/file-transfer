# relay: temporary chunked file transfer

`relay` moves one file between two browsers through a Go WebSocket relay. The backend forwards each binary chunk directly from sender to receiver. It has no file field, file store, temporary file, database, or Firebase Storage integration.

## Structure

- `backend/`: Go 1.22 HTTP/WebSocket server, in-memory transfer manager, framing, tests, and Dockerfile.
- `frontend/`: React + TypeScript + Vite app with send, receive, QR, progress, and responsive states.

## Local development

Prerequisites: Go 1.22+, Node.js 20+, and npm.

```powershell
cd backend
go mod tidy
go test ./...
$env:PUBLIC_BASE_URL="http://localhost:5173"
go run ./cmd/server
```

In another terminal:

```powershell
cd frontend
npm install
npm run dev
```

The UI runs at `http://localhost:5173`; the API runs at `http://localhost:8080`.

## Environment

`PORT` defaults to `8080`; `PUBLIC_BASE_URL` defaults to `http://localhost:5173`; `ALLOWED_ORIGINS` defaults to `http://localhost:5173`; `TRANSFER_TTL` defaults to `15m`; `MAX_FILE_SIZE` defaults to `10GB`; `MAX_CHUNK_SIZE` defaults to `2097152` bytes; `MAX_ACTIVE_TRANSFERS` defaults to `1000`; `MAX_CONNECTIONS` defaults to `2000`; `CREATE_RATE_PER_MINUTE` defaults to `20` per client IP; `METRICS_TOKEN` is empty by default, which disables `/metrics`; and `TRUST_PROXY` is `false` by default. Set `TRUST_PROXY=true` only when requests come through a trusted proxy. The backend validates file names, MIME metadata, SHA-256 format, transfer size, active sessions, and WebSocket connections. The frontend uses `VITE_API_URL` and `VITE_WS_URL`, defaulting to the local API and derived WebSocket origin.

## Protocol

`POST /api/transfers` returns a private `senderToken` and a receiver URL containing only the receiver capability token. The browser sends the capability in the first JSON join message: `sender_join` or `receiver_join` to `/ws/:transferId`. Tokens are generated with cryptographically secure random bytes, stored only as SHA-256 hashes in memory, and checked with constant-time comparison. The server sends `transfer_offer`. The receiver sends `accept_transfer` or `reject_transfer`. After acceptance, binary messages use a 16-byte big-endian header followed by raw bytes: bytes 0..7 are the unsigned chunk index, bytes 8..15 are the unsigned payload length, and bytes 16 onward are the payload.

The server verifies frame length, chunk size, transfer state, and exact next index, writes the frame to the receiver before acknowledging the sender, and therefore provides backpressure. The browser allows four unacknowledged chunks. `transfer_complete` is sent after all ACKs.

## Deployment

```powershell
cd frontend
npm install
$env:VITE_API_URL="https://your-go-backend.example.com"
$env:VITE_WS_URL="wss://your-go-backend.example.com"
npm run build
firebase login
firebase init hosting
firebase deploy
```

The included `frontend/firebase.json` rewrites `/receive/<id>` and `/transfer/<id>` to the SPA entrypoint.

For Cloud Run:

```powershell
cd backend
gcloud run deploy relay-backend --source . --region us-central1 --allow-unauthenticated --set-env-vars "ALLOWED_ORIGINS=https://your-project.web.app,PUBLIC_BASE_URL=https://your-project.web.app,TRANSFER_TTL=15m,MAX_FILE_SIZE=10737418240,MAX_CHUNK_SIZE=2097152"
```

For a repeatable deployment, replace placeholders in `backend/cloudrun.yaml`, build and push the image, then apply the service:

```powershell
docker build -t REGION-docker.pkg.dev/PROJECT_ID/REPOSITORY/relay-backend:TAG ./backend
docker push REGION-docker.pkg.dev/PROJECT_ID/REPOSITORY/relay-backend:TAG
gcloud run services replace backend/cloudrun.yaml --region us-central1
```

The manifest intentionally sets `maxScale: "1"` because transfer sessions are process-local memory. Do not scale the relay horizontally until session routing or a different transport architecture is introduced. The CI workflow builds the Docker image on every push and pull request even when Docker is unavailable on a developer workstation.

The container binds to `0.0.0.0:$PORT`, runs as a non-root distroless user, and uses only process memory.

The server uses bounded request headers, read/write/idle timeouts, security response headers, exact configured origins, WebSocket ping/pong deadlines, connection limits, and graceful shutdown on `SIGTERM`. Cloud Run or the reverse proxy must terminate HTTPS and forward only trusted `X-Forwarded-For` headers. Keep `/metrics` behind platform authentication or an internal network in deployments where operational counters should not be public.

## Metrics

`GET /metrics` returns process-local JSON counters for `activeTransfers`, `activeConnections`, `createdTransfers`, `completedTransfers`, `cancelledTransfers`, `expiredTransfers`, `failedTransfers`, `bytesRelayed`, and `queueSaturated`. Metrics are held in memory and reset when the backend instance restarts. They never contain file names, file contents, transfer IDs, or tokens.

## Security and limitations

Transfer IDs are 128-bit cryptographically random values. The server validates size, role, state, expiry, framing, order, and chunk size. Configure exact origins in production. The relay sees file bytes while forwarding, so this is **not end-to-end encryption**. A disconnect can only recover while the sender still has the original `File`; the server cannot resume data it never stored. Sessions exist on one backend instance, so use session affinity for this MVP before scaling horizontally.

No networked application can honestly guarantee complete safety. This MVP reduces common risks with validation, bounded resources, rate limiting, origin checks, timeouts, and shutdown handling, but production operators still need TLS, authenticated infrastructure access, trusted-proxy configuration, monitoring, dependency updates, and an abuse-response plan. Do not expose the backend broadly without reviewing those controls.

The receiver uses `showSaveFilePicker()` and a writable stream where supported, so large files do not need to be assembled in JavaScript memory. Browsers without that API use a Blob fallback capped at 250 MB. The sender hashes the file incrementally before creating the transfer, and the receiver hashes incoming chunks incrementally before declaring success. The server still sees the bytes during relay; this is not end-to-end encryption.

## Future transport

`TransferClient` isolates browser transport calls. A future `WebRTCTransport` can replace the WebSocket data path while Go remains a signaling and metadata service. That would keep file bytes between devices and enable actual end-to-end encryption at the transport layer. Future extension points include passwords, multiple files, folders, authentication, pairing, parallel chunks, adaptive chunk sizing, and durable resumability.
