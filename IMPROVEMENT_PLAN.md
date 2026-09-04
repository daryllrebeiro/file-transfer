# Improvement & Additional Features Plan

This plan takes `relay` from a hardened MVP to a mature product. It is grounded in the current codebase: every item references real files, real limitations, and real design seams. Items are organized into five tracks and four phases, with effort estimates, dependencies, risks, and acceptance criteria for each.

**Relationship to prior work**: `PRODUCTION_READINESS_PLAN.md` covered P0 (critical fixes) and P1 (near-term improvements) — all complete. This plan absorbs the unfinished P2 technical debt as Track A and extends into new territory (Tracks B–E).

---

## Table of Contents

- [Guiding Principles](#guiding-principles)
- [Track A — Hardening & Technical Debt](#track-a--hardening--technical-debt)
- [Track B — Protocol & Performance](#track-b--protocol--performance)
- [Track C — Major New Features](#track-c--major-new-features)
- [Track D — UX & Product Polish](#track-d--ux--product-polish)
- [Track E — Scale & Operations](#track-e--scale--operations)
- [Phased Roadmap](#phased-roadmap)
- [Effort Summary](#effort-summary)
- [Success Metrics](#success-metrics)
- [Items Explicitly Deferred](#items-explicitly-deferred)

---

## Guiding Principles

Every item in this plan must respect the product's core contract:

1. **Temporary by design** — no feature may introduce server-side file persistence. Durable resume (C3) pushes against this boundary; its design explicitly keeps all durability client-side.
2. **The relay sees bytes; the hash catches lies** — today, relay-mode integrity rests on SHA-256 verification. Item C1 (password encryption) closes this gap properly; until then, no feature should weaken the hash contract.
3. **Single instance until we pay for scale** — Track E's horizontal scaling is opt-in infrastructure work, not an implicit requirement of any feature.
4. **Protocol changes are versioned** — any wire-format change (B1, B2, B3) must negotiate: old clients talking to a new server must keep working during rollout.
5. **Test-first for protocol features** — every Track B/C item lands with backend integration tests and, where feasible, Playwright coverage before it ships.

---

## Track A — Hardening & Technical Debt

Carryovers from the P2 tier of the production readiness plan, plus debt discovered during the features-explainer audit.

### A1. LastChunk Memory: Hash Instead of Full Frame

**Problem**: `manager.go:408-419` (`RecordChunk`) retains a full copy of the last frame (up to `MAX_CHUNK_SIZE + 16` bytes) for duplicate detection. With 1000 active transfers and 2 MB chunks, worst case is ~2 GB of dedup memory — the single largest memory term on a 512 MiB Cloud Run instance.

**Design**:
- Replace `LastChunk []byte` with `LastChunkHash [32]byte` (SHA-256 of the frame).
- `PrepareChunk`'s duplicate branch compares `sha256(frame)` against `LastChunkHash`.
- Collision risk: a 32-byte hash compared against one alternative frame per session; the practical forgery surface is negligible, and a false-positive duplicate only suppresses one retransmission (the receiver drops duplicates anyway, so the sender's ACK arrives either way — the failure mode is benign).
- Memory per session drops from ~2 MB to 32 bytes.

**Files**: `backend/internal/transfer/session.go`, `manager.go`, `manager_test.go` (extend `TestManagerAllowsExactLastChunkRetransmission` to prove altered duplicates still rejected).

**Effort**: 0.5 day. **Risks**: none material. **Acceptance**: duplicate-retransmission test green; memory profile shows flat session footprint.

### A2. Rate-Limit Headers

**Problem**: `handlers.go` returns 429 with no machine-readable metadata; well-behaved clients cannot back off intelligently.

**Design**: On 429 responses, set `Retry-After: {seconds-to-window-reset}`. On all create responses, set `X-RateLimit-Limit` and `X-RateLimit-Remaining`. Requires `CreationLimiter.Allow` to return `(allowed bool, retryAfter time.Duration, remaining int)` instead of `bool`.

**Files**: `backend/internal/httpapi/handlers.go`, `handlers_test.go`.

**Effort**: 0.5 day. **Acceptance**: test asserts headers at-limit, over-limit, and after window rollover.

### A3. Distinguish API Error Causes

**Problem**: `handlers.go:83-89` collapses JSON decode errors, validation failures, and rate limits into generic 400s. Users cannot tell "file too large" from "bad request shape".

**Design**:
- JSON decode failure → 400 `"invalid request body"`.
- `Manager.CreateWithTokens` returns typed errors (`ErrFileNameInvalid`, `ErrFileSizeExceeded`, …) mapped to specific 400 messages; the frontend's `createTransfer` maps status + body message to actionable copy.
- Keep error strings free of user data (echo back nothing the client sent).

**Files**: `manager.go` (typed errors), `handlers.go`, `api.ts` (client mapping).

**Effort**: 1 day. **Acceptance**: each rejection cause has a distinct, tested response.

### A4. WebRTC Assembly Buffer Pooling

**Problem**: `WebRTCTransport.ts:52,179-185` allocates a fresh `Uint8Array(totalBytes)` plus one `ArrayBuffer` per 60 KB sub-chunk. At line rate this creates heavy GC pressure on mobile.

**Design**:
- Pool `Uint8Array` buffers at `SUB_CHUNK_SIZE` granularity (a simple free-list; `ArrayBuffer` is not transferable to workers here, so pool in place).
- Pool full-chunk assembly buffers at `metadata.chunkSize`, released after `chunkCallback` consumes them (the receiver's sink copies into its write path today, so lifetime is bounded).
- Guard total pool size (e.g., 4 × chunkSize) to keep memory bounded.

**Files**: `frontend/src/transport/WebRTCTransport.ts`, new `frontend/src/transport/bufferPool.ts` with unit tests.

**Effort**: 1.5 days. **Acceptance**: long-transfer CPU profile shows reduced minor-GC frequency; existing transport tests green.

### A5. Session Map Sharding

**Problem**: All session operations serialize on `manager.mu`. At 2000+ connections with chunk-level `Get` calls (`PrepareChunk`, `RecordChunk` each take RLock), the lock becomes a throughput ceiling.

**Design**:
- Shard `sessions` into 16 maps keyed by `id[0]` (hex ID gives uniform distribution), each with its own RWMutex.
- `Manager` facade keeps the same API; `Get`, `Delete`, `Create`, `Cleanup`, `Shutdown`, and `Metrics` iterate/fan-out across shards.
- Per-shard cleanup allows finer-grained expiry sweeps.

**Files**: `backend/internal/transfer/manager.go`, `manager_test.go` (add a concurrent hammer test: N goroutines creating/transferring/deleting).

**Effort**: 2 days. **Risks**: subtle deadlocks if shard locks nest — rule: never hold two shard locks simultaneously (fan-out collects then acts). **Acceptance**: `go test -race` green; benchmark shows lock contention drop at 2000 connections.

### A6. Structured Logging

**Problem**: `log.Printf` only; no request IDs, no levels, no context. Production debugging is guesswork.

**Design**:
- Adopt `log/slog` (stdlib, Go 1.22): JSON handler behind `LOG_FORMAT=json|text`, `LOG_LEVEL=debug|info|warn|error`.
- Log join/accept/complete/cancel/expiry events with `transferId` (IDs are already non-sensitive), duration, byte counts, and outcome. Never log tokens, file names, or IPs (IPs only in rate-limit rejections, behind a `LOG_PII` opt-in).
- Add a `requestId` (random 8-hex) minted per WS connection and included in all its log lines.

**Files**: `cmd/server/main.go`, `websocket/handler.go`, `httpapi/handlers.go`, `config.go` (new vars).

**Effort**: 1.5 days. **Acceptance**: a full transfer produces a traceable, redaction-safe event sequence in logs.

### A7. Prometheus Metrics Exporter

**Problem**: `/metrics` JSON is process-local and reset-on-restart; no history, no alerting integration.

**Design**:
- Add `METRICS_FORMAT=json|prometheus` (default `json` to preserve compatibility).
- Prometheus format exposes the same nine counters as `relay_{}_total` / gauges; document a starter alert set (failedTransfers rate, queueSaturated > 0, activeConnections near MAX_CONNECTIONS).
- Keep bearer-token gating for both formats.

**Files**: `httpapi/handlers.go`, `manager.go` (Metrics struct unchanged), `README.md`.

**Effort**: 1 day. **Acceptance**: `prometheus/prometheus` scraper parses the endpoint; alerts documented.

### A8. Consolidate Documentation

**Problem**: `README.md` and `HOW_TO_RUN.md` overlap heavily (env vars, deployment, testing in both); drift is inevitable.

**Design**: Single `README.md` with Overview → Quick Start → Configuration → Deployment → Security → Limitations. Delete `HOW_TO_RUN.md`; add redirects via a one-line stub pointing at README anchors. Cross-link `FEATURES_EXPLAINER.md` and this plan.

**Effort**: 0.5 day.

### A9. Console Log Gating & Error Boundaries (frontend)

**Problem**: Transport and UI code carries development `console.log` calls that ship in production; a render error anywhere white-screens the app.

**Design**:
- Introduce `src/services/logger.ts` exporting `log.debug/info/warn/error` that no-ops below `VITE_LOG_LEVEL` (default `warn` in production builds). Replace all direct console calls.
- Add an `ErrorBoundary` component wrapping route elements: renders the Shell with a friendly "Something went wrong" card plus a "Start over" link, and reports via `logger.error`.

**Files**: `frontend/src/services/logger.ts` (new), all transport/UI files, `App.tsx`.

**Effort**: 1 day. **Acceptance**: production bundle contains no stray debug logs; a thrown render error shows the boundary card.

---

## Track B — Protocol & Performance

### B1. Adaptive Chunk Sizing

**Problem**: Fixed 2 MB chunks are wrong at both extremes: on high-RTT links a lost 2 MB chunk costs a full 15 s timeout + 2 MB retransmit; on fast LAN links, 2 MB + window 4 (~8 MB in flight) underutilizes the pipe.

**Design**:
- Sender measures ACK latency (already observable in `WebSocketRelayTransport`'s pending-ack timing) and goodput over a rolling window (last ~16 ACKs).
- Sender computes a target chunk size between `MIN_CHUNK_SIZE` (256 KB) and `MAX_CHUNK_SIZE` (server-configured): simple AIMD (additive increase ×1.25 on clean ACKs, multiplicative decrease ×0.5 on timeout/retry).
- Size changes take effect only at chunk boundaries the server can validate: sender sends a `chunk_size_change` control message *before* using the new size; server updates `session.Metadata.ChunkSize` under the session lock (still clamped to `MAX_CHUNK_SIZE`). `PrepareChunk`'s frame-size check then validates against the updated value.
- WebRTC path: sub-chunking already adapts implicitly; only relay mode needs this.
- Resume compatibility: `nextChunk`-based resume is size-agnostic from the server's view (it validates per-frame), but the receiver's `expectedChunk` accounting is index-based — however, byte-offset math in the sender loop (`f.slice(i * chunkSize, ...)`) must use the size that was in effect per index. Mitigation: sender records `(index → size)` in memory for the live session; on resume, sizes restart from the negotiated default and re-negotiate upward. Document this as a known boundary: a resumed transfer may use different chunk sizes than the original attempt — harmless because indices and the hash are byte-exact.

**Files**: `WebSocketRelayTransport.ts`, `websocket/handler.go` (new control case), `manager.go` (size update under lock), `session.go`, tests both sides.

**Effort**: 3 days. **Risks**: server-side size validation racing concurrent frames — enforce via the existing session mutex and the "size changes apply to indices after the change message" rule. **Acceptance**: throughput benchmark on simulated lossy link (Clumsy/toxiproxy) shows ≥ 30% improvement vs fixed 2 MB; all existing protocol tests green.

### B2. Parallel (Out-of-Order) Chunks

**Problem**: Strict in-order chunk acceptance caps throughput at `1 × RTT` per window on lossy links — a single dropped chunk stalls the entire window.

**Design**:
- **Receiver**: replace the single `expectedChunk` cursor with a bounded reorder buffer: accept any index in `[expected, expected + window)`. Buffer out-of-order chunks (bounded: window × chunkSize ≈ 8 MB), release to the sink in order as the low-water mark advances, drop duplicates below `expected`.
- **Server**: `PrepareChunk` admits indices in `[NextChunk, NextChunk + window)`; per-index acceptance tracked with a small bitmap/interval set on the session; `NextChunk` advances over contiguous accepted indices. The last-chunk dedup (A1) generalizes to per-slot hashes only for the un-acked boundary — keep it simple: retain only the highest un-acked frame hash.
- **Sender**: window already exists (4); with out-of-order acceptance it stops head-of-line blocking. Retries remain per-chunk.
- **Negotiation**: `POST /api/transfers` gains `features: ["parallel-v1"]`; server echoes capability in `transfer_offer`. Old clients (no field) get the legacy strict-order path. Both paths coexist per session — no global flag.
- **Completion rule**: sender may send `transfer_complete` only when all indices ≤ total are acknowledged; receiver completes when the reorder buffer drains and `receivedBytes === fileSize`.

**Files**: `manager.go`, `session.go`, `protocol.go` (unchanged wire format), `websocket/handler.go`, `WebSocketRelayTransport.ts`, `WebRTCTransport.ts` (already ordered per channel; unaffected), `App.tsx` receiver handler, extensive tests.

**Effort**: 5 days. **Risks**: highest-complexity item in this plan; reorder-buffer bugs corrupt files silently — the SHA-256 gate is the backstop, and the acceptance criteria require a lossy-link E2E test with byte-exact verification. **Acceptance**: toxiproxy test with 2% loss completes correctly and ≥ 2× faster than B0 baseline; legacy clients pass unchanged.

### B3. Dynamic Window Scaling

**Problem**: Fixed window of 4 is arbitrary; B2 makes the window the primary throughput knob, and it should track bandwidth-delay product.

**Design**: Sender ramps `windowSize` between 2 and 16 using the same AIMD signals as B1 (clean ACKs grow, retries shrink). Purely client-side after B2's server acceptance range is set to the maximum (16). Server-side acceptance bound becomes `min(clientWindow, 16)`.

**Files**: `WebSocketRelayTransport.ts` only (post-B2).

**Effort**: 1 day. **Acceptance**: throughput on low-loss LAN approaches link saturation; lossy-link tests show window collapse and recovery.

### B4. TURN Server Deployment Path

**Problem**: Strict NATs fail WebRTC and silently fall back to relay; P2P success rate is the product's headline differentiator and currently depends on two free Google STUN servers.

**Design**:
- Document and support a coturn deployment: Terraform/module for a TURN instance (or managed equivalent), `TURNS` credentials provisioned per deployment.
- Frontend already accepts `VITE_WEBRTC_ICE_SERVERS` — add a documented example with `turns:` entries and short-lived REST-auth credentials (`TURN_REST_SECRET` fetched by the Go server via a new `GET /api/turn-credentials` endpoint, cached for its TTL, so TURN secrets never ship in the client bundle).
- Metrics: add a `webrtcDirect` vs `webrtcFallback` counter split to the existing counters so P2P success rate is measurable.

**Files**: `websocket/handler.go` (control already supports `webrtc_connected`/`webrtc_fallback` — just count), `handlers.go` (new endpoint), `config.go`, `WebRTCTransport.ts` (fetch credentials when env var set), `README.md`, new `deploy/turn/` module.

**Effort**: 3 days (mostly infra + docs). **Acceptance**: behind a NAT that blocks direct candidates, transfer connects P2P via TURN; success-rate counters visible in `/metrics`.

### B5. Web Worker Hashing

**Problem**: The sender's pre-transfer SHA-256 (`hashFile`) runs on the main thread; on a 10 GB file this blocks the UI for tens of seconds, and the receiver's per-chunk `hash.update` competes with rendering on slow devices.

**Design**:
- Move `hashFile` into a dedicated worker (`src/workers/hash.worker.ts`), streaming `File.slice` reads from the worker (workers can use `file.slice()` via structured-clone of the handle).
- Receiver: batch-feed the worker (post chunk buffers, receive digest at completion). Ordering is preserved by posting sequentially.
- `@noble/hashes` is worker-safe; `integrity.ts` keeps its sync API for tests and gains async wrappers.

**Files**: `frontend/src/workers/hash.worker.ts` (new), `integrity.ts`, `App.tsx` (both flows), vite config (worker bundling is native to Vite).

**Effort**: 1.5 days. **Acceptance**: hashing a large file leaves the main thread responsive (frame budget test); digest matches the non-worker path for the same file.

### B6. Client-Side Size Pre-Validation

**Problem**: The sender hashes the entire file before discovering the server rejects it for size — minutes wasted on a doomed transfer.

**Design**: On file select, the Home component fetches server limits once (`GET /api/limits` — new public endpoint returning `{ maxFileSize, maxChunkSize }`, no auth needed, cached in-module) and blocks the "Create transfer link" button with a specific message when `file.size > maxFileSize`.

**Files**: `handlers.go` (endpoint), `api.ts`, `App.tsx`.

**Effort**: 0.5 day. **Acceptance**: oversized file shows the error before hashing begins.

---

## Track C — Major New Features

### C1. Password-Protected Transfers (End-to-End Encryption)

**The headline feature.** Today, relay mode means the server sees plaintext bytes; only WebRTC mode is E2E encrypted. Password mode makes *both* paths end-to-end encrypted and closes the product's biggest honest limitation (FEATURES_EXPLAINER §30: "Relay is not E2E").

**Threat model**: an untrusted relay (and any network observer). Out of scope: endpoint compromise, phishing of the password out-of-band.

**Design**:

*Key derivation (sender)*:
1. UI gains an optional password field in the transport explainer area. Password stays client-side only — it is never sent to the server, never in the URL, never in metadata.
2. `crypto.subtle.deriveBits(PBKDF2, password, salt[16 random], 250_000 iterations, SHA-256)` → 256-bit AES-GCM key. Salt is generated per transfer and travels in plaintext metadata (`encryption: { scheme: "aes-gcm-pbkdf2", salt, iter }`) — salt is not secret.
3. File chunks are encrypted individually: `AES-GCM(key, iv = 96-bit = transferNonce[32 random] ‖ chunkIndex[64-bit BE])`. Per-chunk unique IVs from a counter are the standard GCM requirement. The transfer nonce travels in the metadata.
4. AAD (additional authenticated data) = the 16-byte chunk header (index + length), binding ciphertext to its position and size — replaying chunk N's ciphertext as chunk M fails authentication.

*Integrity (the subtle part)*:
- The existing plaintext SHA-256 must **not** travel in metadata anymore — a malicious relay holding ciphertext + plaintext-hash gains a verification oracle (brute-force small files, confirm guesses). 
- Instead: metadata carries `sha256Ciphertext`. The receiver verifies ciphertext integrity against it *before* decryption (catches relay corruption/tampering immediately, preserving today's failure UX), decrypts, then verifies a `sha256Plaintext` that is itself encrypted as an extra final "manifest chunk" (index = totalChunks, encrypted like any other). Verdict: only a holder of the password can confirm plaintext integrity.
- Server-side validation: `manager.create` accepts the new optional `sha256Ciphertext` (64-hex rule as today) and, when `encryption` is present, ignores/forbids the legacy `sha256` field.

*Receiver flow*:
1. `transfer_offer` includes `encryption` metadata → UI shows a password prompt instead of the immediate accept gate.
2. On accept: derive key (worker, B5 — PBKDF2 at 250k iterations is ~100 ms+ and must not jank), verify first-chunk GCM auth to confirm the password (wrong password = auth failure on first chunk; surface "Incorrect password").
3. Decrypt per chunk between the transport callback and the sink write; the hash pipeline hashes plaintext post-decrypt.

*Protocol impact*: none on framing — ciphertext is opaque bytes to the wire format, chunk sizes, windows, and the dedup logic (hash-of-frame in A1 operates on ciphertext frames, which is correct: retransmission detection is byte-identity of the frame). `transfer_complete` semantics unchanged.

*WebRTC mode*: encryption applies there too (cheap, and uniform — "password means E2E everywhere" is a simpler promise than "password means E2E except when already E2E").

*UX details*:
- Sender: password field with show/hide toggle and a "copy password hint" is explicitly *not* built (hints leak).
- Link + password must travel on different channels — the UI copy says so: "Send the link and password through different channels."
- Optional QR encodes link only; password is typed.

**Files**: `types.ts` (metadata), `App.tsx` (both flows + prompt), new `frontend/src/services/crypto.ts` (+ unit tests against WebCrypto test vectors), `integrity.ts` (dual hash), `manager.go` (metadata validation), `handlers.go`, `websocket/handler.go` (no change), `README.md` (security model rewrite for this mode).

**Effort**: 6 days. **Risks**: crypto misuse — mitigated by using only WebCrypto primitives (no hand-rolled math), published construction (AES-GCM + PBKDF2, standard IV construction), test vectors, and an explicit security-review gate before release. **Acceptance**: E2E test completes a password transfer with the relay observing only ciphertext (assert via a server-side byte-capture fixture); wrong-password UX works; tampered ciphertext fails with the hash-mismatch UX; existing non-password flows byte-identical.

### C2. Multiple Files & Folders

**Problem**: One file per transfer forces users to create N links for N files — the most common real-world friction.

**Design — manifest model**:
1. Sender selects multiple files (and/or directories via `webkitdirectory` / the FSA directory picker). UI lists them with per-file size and a remove button; total size is the sum.
2. `POST /api/transfers` metadata becomes a **manifest**: `files: [{ fileName, fileSize, mimeType, sha256 }[]]`; server validates each entry with today's per-file rules, and `fileSize` limit applies per file and in total (new `MAX_TOTAL_SIZE`, default 50 GB).
3. Transfer session becomes a sequence of *file slots*: chunks carry `(fileIndex, chunkIndex)`. **Wire format**: extend the 16-byte header to 24 bytes — `fileIndex (uint32)`, reserved (uint32), `chunkIndex (uint64)`, `payloadLength (uint64)`. The server's per-slot `NextChunk` tracking becomes a small array. Protocol version negotiated at creation (`framing: "v2"`); server keeps full v1 support.
4. Receiver: `transfer_offer` shows the file list; accept downloads into either (a) one save-per-file (sequential pickers — simplest), or (b) a **directory picker** (FSA `showDirectoryPicker`) writing all files into a chosen folder — the better UX where supported.
5. Progress becomes two-level: overall (bytes across manifest) + current file name in the caption.
6. Completion: sender sends `transfer_complete` after the final chunk of the final file; receiver verifies each file's hash as its slot finishes (a per-file verdict list in the final state, so one bad file doesn't discard nine good ones).

**Folders**: flattened into the manifest with relative paths (`photos/2024/a.jpg`); server path validation extends to reject `..` and absolute components per segment; receiver recreates the tree under the chosen directory.

**Files**: protocol (both sides), `manager.go`/`session.go` (per-slot state), `receiverStorage.ts` (multi-sink), `App.tsx` (both UIs), `types.ts`, tests everywhere.

**Effort**: 8 days (largest item; do after B2 so the reorder buffer design accounts for multi-slot indices). **Risks**: UI complexity creep — mitigate by shipping 1.0 of this feature as files-only, folders as a fast-follow. **Acceptance**: E2E test transfers a 3-file manifest (mixed sizes, one nested path) with per-file hash verification; v1 single-file clients still complete against a v2 server.

### C3. Durable Resumption (Client-Side Persistence)

**Problem**: Today's resume survives only within a live tab: a refresh loses the sender's `File` handle, and the receiver loses its sink. The server cannot help (by design, it has nothing stored). TTL expiry (15 min) also kills mid-transfer sessions.

**Design — everything durable lives in the browser**:

*Sender*:
- Persist the `FileSystemFileHandle` (when the file came from a picker) into **IndexedDB** alongside the transfer metadata. On return to `/transfer/:id` after a refresh, the component re-offers "Resume sending {file}" — one click re-grants permission (`handle.queryPermission/requestPermission`) and re-obtains the `File`.
- Persist the last-acknowledged chunk index; on rejoin, the server's `nextChunk` remains authoritative (it already is), so local persistence is only a progress-restore nicety.
- Drag-dropped files (no handle): keep today's "choose the original file to resume" flow — document that drop-originated files can't be auto-resumed.

*Receiver*:
- Persist received chunks to IndexedDB (keyed `transferId:fileIndex:chunkIndex`) as they arrive, *in addition to* the live sink when one exists. Cost: a doubled write per chunk (sink + IDB) — make it an opt-in "Make this transfer resumable" toggle on the accept gate, defaulting on for transfers > 100 MB.
- On return: offer "Resume receiving" — re-derive sink (FSA re-permission or fresh blob assembly), replay buffered chunks from IDB in order to catch the sink up, then continue live. Server `nextChunk` does **not** rewind for the receiver (it tracks sender-side acknowledgment) — instead, the receiver requests a rewind: new control message `rewind { fromChunk }` sent on join; server (and sender, if still live) resend from that index. Simplest correct implementation: the *sender* honors `rewind` by resetting its loop cursor (it holds all bytes), server relaxes its next-index check to `index ∈ {NextChunk, rewoundFrom…}` for a bounded re-range.

*TTL extension*: active transfers get heartbeat-based TTL extension — server refreshes `ExpiresAt` on each accepted chunk while `Transferring` (bounded: `now + max(TTL, 10m)`), so a 3-hour 200 GB transfer doesn't expire mid-flight, while abandoned sessions still die on schedule. Config: `ACTIVE_TTL_EXTENSION=true|false` (default true).

*Storage hygiene*: IDB entries are deleted on completion/cancel/verify-failure; a startup sweep drops entries older than 24 h; total IDB budget surfaced in the UI when > 500 MB ("Resume data: 812 MB — Clear").

**Files**: new `frontend/src/services/resumeStore.ts` (+ tests), `App.tsx` (both flows), `WebSocketRelayTransport.ts`/`WebRTCTransport.ts` (rewind support), `websocket/handler.go` (rewind + TTL extension), `manager.go`, `session.go`.

**Effort**: 7 days. **Risks**: rewind protocol complexity interacting with B2's parallel chunks — sequence C3 *after* B2 and design rewind against the reorder buffer; IDB quota errors on huge transfers (mitigated: the toggle + budget surfacing). **Acceptance**: Playwright test refreshes both pages mid-transfer and completes byte-exact; TTL extension tested with a slow drip-feed transfer outliving the base TTL.

### C4. Device Pairing (Remembered Devices)

**Problem**: Repeat usage (my laptop ↔ my phone) still requires QR/link exchange every time.

**Design**:
- Opt-in pairing: on a successful transfer, both sides are offered "Trust this device pair". Consent on *both* ends generates an **Ed25519 keypair per device** (WebCrypto, keys in non-extractable IndexedDB-stored CryptoKey form) and exchanges public keys over the completed (authenticated-by-tokens) channel.
- New "Paired devices" home screen section: shows paired device labels (user-named, e.g., "Pixel 7"). Selecting a paired device + dropping a file opens a **push transfer**: the sender's browser signals via the server (`POST /api/transfers` with the receiver's public key fingerprint as the routing hint), and the paired device — if it has the app open — gets a live notification card ("Laptop wants to send report.pdf — Accept / Decline").
- **Delivery is presence-based, no push infrastructure**: pairing only helps when the receiver has a tab open (or a PWA with a service worker + Push API — Phase 4 follow-up). The MVP value is removing the QR step, not offline delivery.
- Security: pairing keys sign join messages (`device_sig` field) so a stolen link alone can't join as a paired device; unpairing is local (delete key) + a revocation list message on next contact.
- Privacy constraint honored: the server learns only fingerprint hashes, never file data.

**Files**: new `frontend/src/services/pairing.ts`, `App.tsx` (home + receive notification), `websocket/handler.go` (signed join verification, presence tracking), `manager.go` (presence map), tests.

**Effort**: 5 days. **Risks**: WebCrypto key persistence quirks across browsers; scope creep toward "accounts" — the design deliberately stops at pairwise trust with no server-side identity. **Acceptance**: two browser profiles pair once, then complete a second transfer with no link exchange; unpaired devices still work exactly as today.

### C5. Sender-Side Abort & Receiver Rerequest Controls

**Problem**: The only mid-transfer controls today are implicit (close tab). A stalled transfer forces waiting out timeout chains.

**Design**:
- **Sender "Cancel transfer"** button on the transfer page: sends `transfer_cancelled` (server already handles: transitions to Cancelled, notifies both peers, closes connections). Today only the receiver's Reject path and disconnects exercise this cleanly.
- **Receiver "Pause"**: closes the socket gracefully with a `pause` control first; server transitions to `Paused` (already exists for disconnects); sender UI shows "Receiver paused — waiting…" and holds its cursor rather than erroring.
- **Receiver "Restart transfer"** on failure: composes with C3's rewind to re-request from chunk 0 (or from the failure point) when the sender is still attached.

**Files**: `App.tsx` (buttons + states), transports (pause message), `websocket/handler.go` (explicit `pause` case — mostly wiring).

**Effort**: 1.5 days. **Acceptance**: E2E test pauses mid-transfer and resumes byte-exact; cancel from either side lands both UIs in a clean terminal state.

---

## Track D — UX & Product Polish

### D1. Expiry Countdown & Link Regeneration

The sender page shows a live "Link expires in 12:34" countdown (from `expiresAt`, already returned by the API) with color escalation in the final 2 minutes, plus a "Extend link" action — `POST /api/transfers/:id/extend` (sender-token-authenticated, rate-limited, adds one TTL, capped at 2× original) so a receiver who's walking across a building doesn't find a dead link. *Effort: 1.5 days.*

### D2. Transfer History (Local-Only)

A "Recent transfers" section on the home page, sourced from `localStorage` (deliberately not sessionStorage): {name, size, date, outcome, transferId}. No links or tokens persisted — history is a memory aid, not a credential store. Clear-history button. *Effort: 1 day.*

### D3. Accessibility Pass

- Keyboard operability audit of the drop zone (the hidden input already helps; add `role="button"`, `tabIndex`, Enter/Space activation) and transport cards (real `<input type="radio">` under the custom styling, or `role="radiogroup"` + arrow-key handling).
- ARIA live regions for progress and state changes (`role="status"` on the waiting/status line) so screen readers hear transfer progress.
- Focus-visible styles on all interactive elements; contrast audit of the muted sage text (#94a7a0 on #101918 is ~4.6:1 — passes AA for body text; verify all badge/background pairs).
- Respect `prefers-reduced-motion` (disable the dropzone lift and radio-fill animations).
*Effort: 2 days. Acceptance: axe-core scan clean; keyboard-only full transfer completes in a manual test.*

### D4. Internationalization Foundation

Extract all user-facing strings (≈120) into `src/i18n/en.ts` with a thin `t()` helper; add `de`, `es`, `fr` translations; language auto-detected from `navigator.language` with a manual override persisted to localStorage. No framework dependency needed at this string count. *Effort: 3 days (mostly translation).*

### D5. PWA (Installable + Share Target)

- `manifest.webmanifest` (name, icons, `display: standalone`, `share_target` with `POST /share` service-worker handler) so the app appears in the OS share sheet on mobile: share a file *into* relay directly.
- Minimal service worker: app-shell precache with the existing no-cache-per-asset discipline (hashed assets are cacheable; `index.html` never), offline fallback page ("Relay needs a connection").
- Home-screen install prompt after the second successful transfer.
*Effort: 3 days. Risks: SW caching staleness bugs — keep the SW intentionally boring (shell-only).*

### D6. "Send Text" Mode

Alongside the file dropzone: a paste-box for text/URLs (≤ 64 KB). Creates a text transfer (same machinery, `mimeType: text/plain`, chunking trivially single-chunk); the receiver page renders the text with a copy button *and* an optional save-as-file. This makes relay a day-to-day clipboard-between-devices tool and dramatically increases usage frequency for the same protocol investment. *Effort: 1.5 days.*

### D7. Dark/Light Theme Toggle

The current design is dark-first and excellent; add a light variant driven by `prefers-color-scheme` + manual toggle (persisted), by tokenizing the CSS custom properties (`--bg`, `--card`, `--accent`, `--text`…) that `styles.css` currently hardcodes. *Effort: 1 day.*

---

## Track E — Scale & Operations

### E1. E2E Job in CI

**Problem**: The Playwright suite (`e2e/transfer.spec.ts`) proves the full stack but runs only on a developer's machine; CI has no end-to-end signal.

**Design**: New `e2e` job in `.github/workflows/ci.yml` (needs: backend, frontend — runs after both pass): install Playwright browsers with caching (`~/.cache/ms-playwright`), start the Go server and Vite preview (not dev — build once, `npm run preview`) via the existing `webServer` config, run `npm run test:e2e`. Upload `test-results/` as artifacts on failure. Keep the job on `pull_request` + `main` but allow it to be the slowest gate (~3 min).

**Files**: `ci.yml`, `playwright.config.ts` (preview command variant).

**Effort**: 1 day. **Acceptance**: green pipeline on a PR that breaks E2E intentionally (sabotage test), artifact uploaded.

### E2. Load Testing Harness

**Problem**: Success criteria in the original plan promised "1000 concurrent transfers" — never measured.

**Design**:
- `backend/loadtest/` Go harness: N simulated sender/receiver pairs using the real client protocol (join, accept, chunk stream with realistic 2 MB frames from `/dev/urandom` pools), parameterized transfer size/duration.
- Scenarios: 1000 idle sessions (memory ceiling), 100 active transfers at 20 Mbps each (CPU + queue saturation), reconnect storms (500 joins/s), the abuse case (rate-limit + connection-limit verification).
- Runbook documenting the target numbers per instance size (512 Mi / 1 Gi) and the p95 latency thresholds.
- Wire into CI as a nightly optional job (not per-PR — too slow), posting a summary artifact.

**Effort**: 3 days. **Acceptance**: documented capacity numbers replace guesses; `queueSaturated` behavior observed and tuned (`peerQueueSize` may warrant raising to 8 with A5 in place).

### E3. Horizontal Scaling Path (Opt-In, Redis Signaling)

**Problem**: `maxScale: 1` is a hard ceiling. The honest fix is not "add Redis everywhere" but a scoped, optional fan-out that preserves the in-memory product for operators who want it small.

**Design**:
- **Phase 1 — sticky sessions (no code)**: document GCLB/Cloud Run session affinity configuration; sessions already live on one instance if the LB routes both peers of a transfer to the same backend. This ships as *documentation + a `cloudrun-ha.yaml`* variant (session affinity on, `maxScale: 3`) and works today because both peers share the transfer ID.
- **Phase 2 — Redis pub/sub signaling relay (code)**: `SESSION_BACKEND=memory|redis` config. In Redis mode, per-instance local state shrinks to connection objects; session state and chunk-forwarding decisions move to a Redis hash + pub/sub channels per transfer ID. `PrepareChunk` becomes a Lua script (atomic check-and-advance of `NextChunk`). Chunk bytes still flow instance-to-instance over the peers' existing sockets via pub/sub fan-out — bandwidth doubles (in + out per instance), which is the accepted cost of scale.
- Feature-gated, defaulted off; the memory backend remains first-class and tested.

**Effort**: Phase 1 = 0.5 day; Phase 2 = 8 days (Lua atomicity, reconnect semantics, dual-backend test matrix). **Risks**: highest architectural risk in this plan — the entire point of `relay`'s simplicity is at stake; the plan explicitly treats Phase 2 as a separate go/no-go decision after Phase 1's sticky-session numbers (E2) are in. **Acceptance (Phase 2)**: 3-instance deployment passes the full E2E suite with affinity *disabled*.

### E4. Dependency & Supply-Chain Hygiene

- Pin all `"latest"` versions in `frontend/package.json` to caret ranges; `npm audit` gate in CI (fail on high).
- Enable Dependabot (ecosystem: npm, gomod, docker, github-actions) with grouped minor/patch PRs.
- Add `golangci-lint` to the backend CI job (config: errcheck, govet, staticcheck, gosec) — replaces bare `go vet` with a superset.
- SBOM generation for the container image (`docker build ... --sbom`) attached to releases.

**Effort**: 1.5 days. **Acceptance**: Dependabot opens its first grouped PR; CI fails on an injected high-severity audit finding.

### E5. Release Engineering

- Tag-driven releases: `main` → tag `vX.Y.Z` triggers image build + push to Artifact Registry + a GitHub Release with notes generated from conventional commits.
- `CHANGELOG.md` maintained per release.
- Staging Cloud Run service (separate project/env vars) auto-deployed from `main`; production deploy remains manual `gcloud run services replace` per current doctrine.

**Effort**: 2 days. **Acceptance**: tagging `v1.1.0` produces a registry image, a GitHub release, and a staging deployment without manual steps.

### E6. Free-Tier GCP Deployment Automation (mirror of `support-master`)

**Goal**: a one-command, environment-driven deployment to Google Cloud Run on the **free tier** (`min-instances 0`, small CPU/memory, artifact-tagged immutable images) — the same developer-experience pattern used by the `support-master` repo (`scripts/deploy.ps1` + `deploy.sh` + `deploy-cloudrun.ps1` + `docs/gcp-deployment.md` + per-deploy benchmark reports).

**Deliverables**:
1. **`scripts/deploy.ps1` and `scripts/deploy.sh`** — numbered, benchmarked, idempotent, `$ErrorActionPreference="Stop"`/`set -e` guarded. Steps (mirroring the reference repo):
   1. Env validation: `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_REGION`, optional `METRICS_TOKEN` (no hard secrets required otherwise).
   2. Pre-flight: `gcloud` present + authenticated, git commit SHA captured (fallback `manual`).
   3. `gcloud services enable` (run, cloudbuild, artifactregistry, secretmanager).
   4. Artifact Registry repo `relay-backend` (create-if-missing).
   5. Secret Manager container `relay-metrics-token`; payload injected out-of-band via `gcloud secrets versions add --data-file=-` — never in state or image. Secret optional (metrics disabled when absent).
   6. GCS state bucket bootstrap only for the Terraform variant; the default simple path skips Terraform and uses direct `gcloud run deploy` (matching `deploy-cloudrun.ps1`).
   7. `gcloud builds submit --tag {region}-docker.pkg.dev/{project}/relay-backend/relay-backend:{COMMIT_SHA}`.
   8. IAM: grant the Cloud Run runtime SA `roles/secretmanager.secretAccessor` on the metrics secret.
   9. `gcloud run deploy relay-backend` — free-tier friendly flags: `--min-instances 0 --max-instances 1` (relay is in-memory/single-instance by design), `--cpu 1 --memory 512Mi`, `--port 8080`, `--allow-unauthenticated` (public transfer links), env vars `PORT`, `PUBLIC_BASE_URL={service URL}`, `ALLOWED_ORIGINS={frontend origin}`, `TRANSFER_TTL`, limits, `TRUST_PROXY=true` (behind Cloud Run), `METRICS_FORMAT=prometheus`, `--set-secrets METRICS_TOKEN=relay-metrics-token:latest`.
   10. **Live verification**: poll `${SERVICE_URL}/healthz` for HTTP 204 (up to ~60s warm-up), then `curl /metrics` with the token to prove secret injection.
   11. Write a **`deploy-history/deploy_{timestamp}_{sha}.md`** report: per-step durations, service URL, image tag, verdict — mirroring the reference repo's benchmark report.
2. **`docs/gcp-free-deployment.md`** — architecture diagram, one-time prerequisites, cost notes (Cloud Run free tier quotas, min-instances 0 cold-start implication for live links), and the session-affinity caveat (`maxScale` must stay 1).
3. **Optional Terraform variant** (`infra/terraform/`, gated by `-UseTerraform`) for those who want declarative infra; default path stays script-only so a contributor needs just `gcloud`.

**Files**: `scripts/deploy.ps1`, `scripts/deploy.sh`, `scripts/deploy-cloudrun.ps1`, `docs/gcp-free-deployment.md`, `infra/terraform/` (optional), `.gitignore` (`deploy-history/` optional to keep, actually **committed** as evidence per reference repo).

**Effort**: 2 days. **Risks**: free-tier cold starts make an idle `min-instances 0` service slow on first hit after inactivity — documented; production traffic can set `--min-instances 1` at small cost. **Acceptance**: fresh project deploys with one exported env var + one script run, `healthz` returns 204, `/metrics` returns Prometheus text with the injected token, and a benchmark report is written to `deploy-history/`.

---

## Phased Roadmap

Sequencing logic: Track A first (cheap, de-risks everything), protocol work before features that depend on it (B2 → C2/C3), crypto (C1) independent and parallelizable, UX items interleaved as breathers, scale work last and gated on measurements.

### Phase 1 — Debt Zero (Weeks 1–2) · ~9 dev-days
| Item | Effort |
|------|--------|
| A1 LastChunk hash | 0.5d |
| A2 Rate-limit headers | 0.5d |
| A3 Typed API errors | 1d |
| B6 Size pre-validation | 0.5d |
| A9 Logger + error boundaries | 1d |
| A8 Docs consolidation | 0.5d |
| E1 E2E in CI | 1d |
| D1 Expiry countdown + extend | 1.5d |
| D2 Local history | 1d |
| A4 Buffer pooling | 1.5d |

**Exit criteria**: all P2 debt closed; CI runs E2E; memory profile flat; zero stray console logs.

### Phase 2 — Protocol Performance (Weeks 3–5) · ~10 dev-days
| Item | Effort |
|------|--------|
| B1 Adaptive chunk size | 3d |
| B2 Parallel chunks | 5d |
| B3 Dynamic window | 1d |
| E2 Load harness (start; finishes into Phase 3) | 1d+ |

**Exit criteria**: lossy-link throughput ≥ 2× baseline with byte-exact E2E; legacy client compatibility proven; load harness produces first capacity numbers.

### Phase 3 — Feature Era (Weeks 6–11) · ~29 dev-days
| Item | Effort |
|------|--------|
| C1 Password / E2E encryption | 6d |
| B5 Worker hashing (prereq polish for C1 UX) | 1.5d |
| B4 TURN deployment + counters | 3d |
| C5 Cancel/pause controls | 1.5d |
| D6 Text mode | 1.5d |
| D7 Theme toggle | 1d |
| D3 Accessibility pass | 2d |
| C2 Multi-file (folders as follow-up) | 8d |
| C3 Durable resume | 7d (overlaps C2 completion) |

**Exit criteria**: password transfers verifiably opaque to the relay; multi-file E2E green; refresh-mid-transfer resumes byte-exact; a11y audit clean.

### Phase 4 — Reach & Scale (Weeks 12–15) · ~21 dev-days
| Item | Effort |
|------|--------|
| D4 i18n foundation | 3d |
| D5 PWA + share target | 3d |
| C4 Device pairing | 5d |
| E3 Phase 1 sticky sessions | 0.5d |
| E4 Supply-chain hygiene | 1.5d |
| E5 Release engineering | 2d |
| E3 Phase 2 Redis (go/no-go gate) | 8d |
| E6 Free-tier GCP deployment automation | 2d |
| E2 Load harness completion + runbook | 2d |

**Exit criteria**: installable PWA with share-sheet entry; paired devices transfer without links; release pipeline one-tag automated; scale decision made on data.

---

## Effort Summary

| Track | Items | Total effort |
|-------|-------|--------------|
| A — Hardening | 9 | ~10 dev-days |
| B — Protocol & perf | 6 | ~14 dev-days |
| C — Major features | 5 | ~27.5 dev-days |
| D — UX polish | 7 | ~13 dev-days |
| E — Scale & ops | 6 | ~18 dev-days |
| **Total** | **33** | **~82 dev-days** (≈ 16 weeks for one developer, with the phase overlaps above compressing calendar time to ~15 weeks) |

A two-person team (one backend-leaning, one frontend-leaning) compresses this to roughly 8–9 calendar weeks, since C1/C2/C3 split cleanly along that seam.

---

## Success Metrics

Tie each phase to measurable outcomes, instrumented via the existing metrics endpoint (extended by B4/E2):

| Metric | Baseline (today) | Target |
|--------|------------------|--------|
| P2P connection success rate (auto mode) | unmeasured | ≥ 85% with TURN (B4); displayed in ops dashboard |
| Lossy-link throughput (2% loss, 100 ms RTT) | ~1× (fixed 2 MB, window 4, in-order) | ≥ 2× (B1+B2+B3), byte-exact |
| Worst-case server memory per session | ~2 MB (LastChunk) | ≤ 64 KB (A1) |
| Refresh survival (both sides, mid-transfer) | 0% | 100% for picker-originated files, receiver-opt-in (C3) |
| Time-to-first-byte after link open (receiver) | 2 clicks + consent | same, but with countdown & extend preventing expiry deaths (D1) |
| CI signal | unit + build only | unit + build + container + **E2E** (E1) |
| Documented capacity per 512 Mi instance | none | published numbers (E2) |
| Deployment | manual | tag → staging automated, prod one command (E5) |

---

## Items Explicitly Deferred

Considered and deliberately excluded from this plan, with reasons on record:

- **User accounts / server-side identity** — violates the no-persistence contract; C4's pairwise trust covers the legitimate need without identity.
- **Server-side file storage or "download later" links** — the antithesis of the product; if demand appears, it belongs in a differently-named product.
- **Arbitrary/multiple recipients (broadcast)** — multiplies abuse surface (one sender, N receivers) with token-design implications not worth pre-solving; revisit after C2.
- **Chunk-level compression** — most transfer payloads are already-compressed media; AES-GCM (C1) adds ~no expansion; net win is marginal except for text-mode (D6) where the payload is small anyway.
- **Custom TURN implementation** — B4 deploys coturn; writing a TURN server is a product unto itself.
- **IPv6-specific optimizations, HTTP/3 WebTransport** — WebTransport is compelling long-term (better congestion control than WebSocket-over-HTTP/1.1) but browser support is still uneven; revisit when Safari ships it stable.
- **Mobile native apps** — D5's PWA covers the mobile use case at a fraction of the maintenance cost.

---

*This plan should be read alongside `FEATURES_EXPLAINER.md` (current-state detail per feature) and `PRODUCTION_READINESS_PLAN.md` (completed hardening). Revision cadence: revisit at each phase exit, or on any material change to the threat model.*


