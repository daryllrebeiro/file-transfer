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

`PORT` defaults to `8080`; `PUBLIC_BASE_URL` defaults to `http://localhost:5173`; `ALLOWED_ORIGINS` defaults to `http://localhost:5173`; `TRANSFER_TTL` defaults to `15m`; `MAX_FILE_SIZE` defaults to `10GB`; and `MAX_CHUNK_SIZE` defaults to `2097152` bytes. The frontend uses `VITE_API_URL` and `VITE_WS_URL`, defaulting to the local API and derived WebSocket origin.

## Protocol

The browser sends JSON `sender_join` or `receiver_join` to `/ws/:transferId`. The server sends `transfer_offer`. The receiver sends `accept_transfer` or `reject_transfer`. After acceptance, binary messages use a 16-byte big-endian header followed by raw bytes: bytes 0..7 are the unsigned chunk index, bytes 8..15 are the unsigned payload length, and bytes 16 onward are the payload.

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

The container binds to `0.0.0.0:$PORT`, runs as a non-root distroless user, and uses only process memory.

## Security and limitations

Transfer IDs are 128-bit cryptographically random values. The server validates size, role, state, expiry, framing, order, and chunk size. Configure exact origins in production. The relay sees file bytes while forwarding, so this is **not end-to-end encryption**. A disconnect can only recover while the sender still has the original `File`; the server cannot resume data it never stored. Sessions exist on one backend instance, so use session affinity for this MVP before scaling horizontally.

The receiver uses `showSaveFilePicker()` and a writable stream where supported, so large files do not need to be assembled in JavaScript memory. Browsers without that API use a Blob fallback capped at 250 MB. The sender hashes the file incrementally before creating the transfer, and the receiver hashes incoming chunks incrementally before declaring success. The server still sees the bytes during relay; this is not end-to-end encryption.

## Future transport

`TransferClient` isolates browser transport calls. A future `WebRTCTransport` can replace the WebSocket data path while Go remains a signaling and metadata service. That would keep file bytes between devices and enable actual end-to-end encryption at the transport layer. Future extension points include passwords, multiple files, folders, authentication, pairing, parallel chunks, adaptive chunk sizing, and durable resumability.
