# TO_DO_PLANS.md — Remaining Work & Implementation Details

Status snapshot as of the end of Track D (D1–D7 all delivered). This file is the single source of truth for every remaining todo, with the design detail needed to implement each one. Source refs point into `IMPROVEMENT_PLAN.md` sections.

**Completed so far** (for context): B1 adaptive chunks · B2 parallel/out-of-order chunks · B3 dynamic window · E2 load harness · A4–A7 (buffer path, sharding, slog, Prometheus) · D1 expiry+extend · D2 history · D3 a11y · D6 text mode · D7 light theme · D4 i18n · D5 PWA+share · plus all earlier hardening (P0/P1, N1–N5).

---

## Execution order (recommended)

| Phase | Item | Source | Effort | Blocking concerns |
|---|---|---|---|---|
| 1 | E4 Dependency hygiene | IMPROVEMENT E4 | 1.5d | none (ops) |
| 2 | E5 Release engineering | IMPROVEMENT E5 | 2d | needs E4's gates |
| 3 | E6 Free-tier GCP deployment automation | IMPROVEMENT E6 | 2d | none (docs/scripts) |
| 4 | C5 Cancel/pause controls | IMPROVEMENT C5 | 1.5d | none |
| 5 | B5 Web Worker hashing | IMPROVEMENT B5 | 1.5d | prerequisite polish for C1 UX |
| 6 | C1 Password / end-to-end encryption | IMPROVEMENT C1 | 6d | crypto review gate |
| 7 | C2 Multi-file / folders | IMPROVEMENT C2 | 8d | wire-format v2 negotiation |
| 8 | C3 Durable resumption | IMPROVEMENT C3 | 7d | sequence after C2 |
| 9 | C4 Device pairing | IMPROVEMENT C4 | 5d | presence-based, no push |
| 10 | B4 TURN deployment path | IMPROVEMENT B4 | 3d | infra + credentials |
| 11 | E3 Horizontal scaling | IMPROVEMENT E3 | 0.5d + 8d | go/no-go gate on load data |

---

## 1. E4 — Dependency & supply-chain hygiene (1.5d)

**Goal**: Pin the supply chain and fail CI on known-vulnerable or sloppy code.

- `frontend/package.json`: replace loose ranges (`"latest"`, `^`) with explicit caret-pinned ranges for all deps.
- CI (`frontend` job): add `npm audit --audit-level=high` gate (fail on high+).
- Backend: replace the bare `go vet` gate with `golangci-lint` (errcheck, govet, staticcheck, gosec) in the `backend` job.
- Dependabot: `.github/dependabot.yml` for `npm`, `gomod`, `docker`, `github-actions`, grouped minor/patch PRs (weekly).
- Container: build with SBOM/attestation (`docker buildx` `--provenance`/`--sbom`) and attach to releases (overlaps E5).

**Files**: `frontend/package.json` + lockfile, `.github/workflows/ci.yml`, `.github/dependabot.yml` (new), `backend/Dockerfile`, `.golangci.yml` (new).
**Acceptance**: `npm audit` clean at high; an injected high-severity dev-dep fails CI; Dependabot opens its first grouped PR; golangci-lint green.

---

## 2. E5 — Release engineering (2d)

**Goal**: One tag → image + release + staging deploy, zero manual steps.

- Tag-driven workflow `.github/workflows/release.yml` on `v*` tags:
  1. Build + push container to Artifact Registry (reuse `container` job steps + auth).
  2. Auto-deploy the **staging** Cloud Run service (separate project/region env vars).
  3. Generate a GitHub Release from conventional commits since the previous tag.
- `CHANGELOG.md` updated per release.
- Production deploy remains an explicit `gcloud run services replace` (documented doctrine) — do not auto-promote prod.

**Files**: `.github/workflows/release.yml` (new), `CHANGELOG.md`, README deployment section.
**Acceptance**: tagging `vX.Y.Z` produces a registry image, GitHub release, and staging deploy with no manual steps.

---

## 3. E6 — Free-tier GCP deployment automation (2d)

**Goal**: One-command, env-driven deployment to Cloud Run on the free tier, mirroring the `support-master` repo pattern (`scripts/deploy.ps1` / `deploy.sh` / `deploy-cloudrun.ps1`, commit-SHA-tagged Cloud Build, out-of-band Secret Manager, live health verification, `deploy-history/` benchmark reports).

**Script steps** (`scripts/deploy.ps1` and `scripts/deploy.sh`):
1. Validate env: `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_REGION`, optional `METRICS_TOKEN`.
2. Pre-flight: `gcloud` present + authenticated; capture git `COMMIT_SHA` (fallback `manual`).
3. `gcloud services enable run cloudbuild artifactregistry secretmanager`.
4. Ensure Artifact Registry repo `relay-backend`.
5. Ensure Secret Manager secret `relay-metrics-token`; inject payload out-of-band via `gcloud secrets versions add --data-file=-` (never in state/image). Optional — metrics disabled when absent.
6. `gcloud builds submit --tag {region}-docker.pkg.dev/{project}/relay-backend/relay-backend:{COMMIT_SHA} ./backend`.
7. Grant Cloud Run runtime SA `roles/secretmanager.secretAccessor` on the secret.
8. `gcloud run deploy relay-backend`: `--min-instances 0 --max-instances 1` (single-instance by design), `--cpu 1 --memory 512Mi`, `--port 8080`, `--allow-unauthenticated`, env vars `PORT`, `PUBLIC_BASE_URL={url}`, `ALLOWED_ORIGINS={frontend origin}`, `TRUST_PROXY=true`, `WRITE_TIMEOUT=5m`, `METRICS_FORMAT=prometheus`, `--set-secrets METRICS_TOKEN=relay-metrics-token:latest`.
9. **Verify**: poll `${url}/healthz` → 204 (up to ~60s warm-up), then `curl -H "Authorization: Bearer $METRICS_TOKEN" ${url}/metrics` contains `relay_active_transfers`.
10. Write `deploy-history/deploy_{ts}_{sha}.md` benchmark report (per-step durations, URL, image tag, verdict).

Optional Terraform variant (`infra/terraform/`, behind `-UseTerraform`) for declarative infra; the default path stays pure `gcloud`.

**Files**: `scripts/deploy.ps1`, `scripts/deploy.sh`, `docs/gcp-free-deployment.md`, `infra/terraform/` (optional), `.gitignore` entry only for local credential files (keep `deploy-history/` committed as evidence).
**Acceptance**: fresh project deploys with env vars + one script run; healthz 204; `/metrics` Prometheus text with injected token; report written.
**Caveat to document**: free-tier `min-instances 0` cold start makes idle services slow on first hit; set `--min-instances 1` for traffic (small cost). `maxScale` must remain 1 (in-memory sessions).

---

## 4. C5 — Sender-side cancel & pause controls (1.5d)

**Goal**: explicit mid-transfer controls instead of implicit "close the tab".

- **Sender "Cancel transfer"** button (visible once a receiver is attached): sends existing `transfer_cancelled` control → server transitions `Cancelled`, notifies both peers, closes connections (all server machinery already exists).
- **Receiver "Pause"**: graceful socket close preceded by a `pause` control message (new); server transitions `Paused` (state already exists); sender UI shows "Receiver paused — waiting…" and holds its cursor instead of erroring.
- **Receiver "Restart transfer"** on failure: composes with C3's rewind later; in isolation, re-request from chunk 0 while the sender is still attached via a `rewind {fromChunk}` control (server relaxes its window floor for a bounded re-range).

**Files**: `App.tsx` (buttons/states), `WebSocketRelayTransport.ts` (pause message + hold), `websocket/handler.go` (explicit `pause`/`rewind` cases), `types.ts`.
**Acceptance**: E2E pauses mid-transfer and resumes byte-exact; cancel from either side lands both UIs in a clean terminal state.

---

## 5. B5 — Web Worker hashing (1.5d)

**Goal**: keep the main thread responsive during the sender's full-file SHA-256 pass and the receiver's per-chunk hashing.

- `src/workers/hash.worker.ts`: streams `file.slice()` reads inside the worker (structured-clone the File handle), runs `@noble/hashes` sha256, posts the hex digest.
- `integrity.ts` gains async worker-backed `hashFile` (keeps a sync path for tests).
- Receiver: batch-feed the worker per chunk (sequential post ordering) and receive the digest at completion; hash result unchanged (byte-identical to the sync path — tests verify).
- Vite handles worker bundling natively.

**Files**: `src/workers/hash.worker.ts` (new), `services/integrity.ts`, `App.tsx` (sender + receiver), `integrity.test.ts` (async + parity test).
**Acceptance**: large-file hashing keeps the frame budget; digest matches the sync implementation for the same file.

---

## 6. C1 — Password-protected transfers / end-to-end encryption (6d)

**The headline feature** — closes "relay is not E2E" for both transports.

**Key derivation**: sender UI password field (never sent to server, never in URL). `PBKDF2(password, salt[16 random], 250_000, SHA-256)` → 256-bit AES-GCM key. Salt + transfer nonce (32 random bytes) travel in metadata (`encryption: {scheme, salt, iter, nonce}`).

**Chunk encryption**: AES-GCM per chunk with `iv = nonce ‖ chunkIndex[64-bit]`; AAD = the 16-byte chunk header (index+length). Ciphertext is opaque to framing/windows/dedup.

**Integrity (the subtle part)**:
- `sha256Plaintext` must NOT travel in metadata (a relay holding ciphertext + plaintext hash gains a verification oracle).
- Metadata carries `sha256Ciphertext` (server validates 64-hex; legacy `sha256` forbidden when `encryption` present).
- Plaintext hash is delivered as an extra encrypted "manifest chunk" (index = totalChunks); only a password holder can confirm plaintext integrity.

**Receiver flow**: `transfer_offer` with `encryption` → password prompt instead of immediate accept; wrong password = GCM auth failure on first chunk → "Incorrect password". Decrypt between transport callback and sink; hash plaintext post-decrypt.

**WebRTC mode**: encryption applies there too — "password means E2E everywhere" is simpler than a mode-split promise.

**UX**: password field with show/hide; copy explains "send the link and password through different channels"; QR encodes link only.

**Files**: `types.ts` (metadata), `App.tsx` (both flows + prompt), `services/crypto.ts` (new, WebCrypto only), `integrity.ts` (dual hash), `manager.go`/`handlers.go` (metadata validation), README security section rewrite.
**Acceptance**: E2E password transfer completes with the relay observing only ciphertext (server-side byte-capture fixture asserts); wrong-password UX; tampered ciphertext fails; non-password flows byte-identical. **Security-review gate before release.**

---

## 7. C2 — Multiple files & folders (8d)

**Goal**: one link, N files.

- Sender multi-select (+ directories via `webkitdirectory`/FSA directory picker); list with per-file size/remove; total shown.
- `POST /api/transfers` metadata becomes a **manifest**: `files: [{fileName, fileSize, mimeType, sha256}]`; per-file validation reuses current rules; `fileSize` applies per file and in total (new `MAX_TOTAL_SIZE`, default 50 GB).
- **Wire format v2**: extend the 16-byte chunk header to 24 bytes — `fileIndex(uint32)`, reserved(uint32), `chunkIndex(uint64)`, `payloadLength(uint64)`. Server tracks per-slot `NextChunk`; negotiated at creation (`framing: "v2"`); full v1 support kept so old clients keep working.
- Receiver: accept shows the file list; download per file (sequential pickers) or into a chosen **directory** via `showDirectoryPicker` (better UX where supported). Folders flatten with relative paths (server rejects `..`/absolute segments; receiver recreates the tree).
- Progress two-level (overall bytes + current file name). Completion after the final chunk of the final file; **per-file** hash verdicts so one bad file doesn't discard the rest.
- 1.0 ships files-only; folders fast-follow.

**Files**: protocol (both sides), `manager.go`/`session.go` (per-slot state), `receiverStorage.ts` (multi-sink), `App.tsx` (both UIs), `types.ts`, tests everywhere.
**Acceptance**: E2E 3-file manifest (mixed sizes, one nested path) with per-file hash verification; v1 clients complete against a v2 server.

---

## 8. C3 — Durable resumption, client-side persistence (7d)

**Goal**: refresh-safe resume on both ends without server storage. **Design**: everything durable lives in the browser.

- **Sender**: persist `FileSystemFileHandle` (picker-originated files) into IndexedDB with transfer metadata. Returning to `/transfer/:id` offers "Resume sending {file}" — one click re-grants permission and resumes from the server's authoritative `nextChunk`. Drop-originated files keep today's "choose the original file" flow.
- **Receiver**: opt-in "Make this transfer resumable" toggle (default on for >100 MB). Received chunks are written to IndexedDB (`transferId:fileIndex:chunkIndex`) in addition to the live sink. On return: re-derive sink (FSA re-permission or fresh blob assembly), replay buffered chunks to catch up, then continue live. Server `nextChunk` does not rewind for the receiver — the receiver sends `rewind {fromChunk}`; the **sender** honors it (it holds all bytes) and the server relaxes the admission floor for a bounded re-range. Sequence after C2 so rewind accounts for the reorder buffer and multi-slot indices.
- **TTL extension while active**: server refreshes `ExpiresAt` on each accepted chunk while `Transferring` (bounded `now + max(TTL, 10m)`), so long transfers don't die mid-flight; abandoned sessions still expire. Config: `ACTIVE_TTL_EXTENSION` (default true).
- **Storage hygiene**: delete IDB entries on completion/cancel/failure; startup sweep clears entries older than 24 h; surface budget when >500 MB with a Clear action.

**Files**: `services/resumeStore.ts` (new, IndexedDB), `App.tsx` (both flows), transports (rewind), `websocket/handler.go`, `manager.go`, `session.go`.
**Acceptance**: Playwright refreshes both pages mid-transfer and completes byte-exact; slow-drip transfer outlives the base TTL.

---

## 9. C4 — Device pairing (remembered devices) (5d)

**Goal**: drop the QR/link step for repeat pairs. **Presence-based only** — no push infrastructure in MVP (a paired device must have a tab/PWA open).

- Opt-in after a successful transfer ("Trust this device pair", consent on both ends): per-device **Ed25519** keypair (WebCrypto, non-extractable keys in IndexedDB), public keys exchanged over the completed, token-authenticated channel.
- Home gains "Paired devices" (user-named, e.g. "Pixel 7"). Selecting a paired device + file opens a **push transfer**: `POST /api/transfers` with the receiver public-key fingerprint as routing hint; the paired, open device sees a live notification card ("Accept / Decline").
- Security: pairing keys **sign join messages** (`device_sig` field) so a stolen link alone can't join as a paired device; unpair = local key delete + revocation message on next contact. Server learns only fingerprint hashes.
- No accounts/server identity (deliberate — pairwise trust only).

**Files**: `services/pairing.ts` (new), `App.tsx`, `websocket/handler.go` (signed join verification, presence map), `manager.go`.
**Acceptance**: two profiles pair once then complete a transfer with no link exchange; unpaired flow unchanged.

---

## 10. B4 — TURN deployment path (3d)

**Goal**: raise P2P success behind strict NATs, where WebRTC silently falls back to relay today.

- Document + support coturn (Terraform module or managed service); `VITE_WEBRTC_ICE_SERVERS` already accepts TURN entries.
- New `GET /api/turn-credentials` (server-provisioned short-lived TURN REST credentials from `TURN_REST_SECRET`, cached for TTL) so TURN secrets never ship in the client bundle; client fetches when configured.
- Counters: split `webrtcDirect` vs fallback so P2P success rate is measurable (wire into the existing `webrtc_connected`/`webrtc_fallback` handling + metrics).

**Files**: `handlers.go`, `config.go`, `WebRTCTransport.ts`, `metrics` counters, `deploy/turn/` module, README.
**Acceptance**: behind a NAT that blocks direct candidates, transfer connects P2P via TURN; success-rate counters visible in `/metrics`.

---

## 11. E3 — Horizontal scaling (opt-in) (0.5d + 8d)

**Goal**: break the `maxScale: 1` ceiling while preserving the in-memory product by default.

- **Phase 1 — sticky sessions (no code)**: document GCLB/Cloud Run session affinity (route both peers of a transfer to the same instance by transfer ID); ship `cloudrun-ha.yaml` variant (affinity on, `maxScale: 3`). Works today.
- **Phase 2 — Redis pub/sub signaling (code, default off)**: `SESSION_BACKEND=memory|redis`. In Redis mode per-instance state shrinks to connection objects; session state + chunk admission (`PrepareChunk`'s next-index/acceptance check) move to a Redis hash + a **Lua script** (atomic check-and-advance); chunk bytes fan out instance-to-instance over peers' existing sockets via pub/sub per transfer ID (bandwidth doubles — the accepted cost of scale).
- Both backends tested; memory backend remains first-class and default.
- **Go/no-go gate**: only proceed once E2 load data + sticky-session numbers justify it.

**Files**: `config.go`, `manager.go` (backend abstraction), new `redis` store, `cloudrun-ha.yaml`, docs.
**Acceptance (Phase 2)**: 3-instance deployment passes the full E2E suite with affinity disabled.

---

## Cross-cutting notes

- **Sequencing rules**: crypto (C1) before pairing (C4) so "password = E2E everywhere" is settled first; wire-format v2 (C2) before durable resume (C3) and before any parallel multi-file rewind; B5 before C1 (hashing UX); E4/E5 before E6 (gates + release cadence); E3 last and gated on data.
- **Per-phase validation gate**: `go test ./...` + `go test -race ./...` + `go vet`/`golangci-lint`, `npm test`, `npm run build`, and the full Playwright suite (4 specs) for UI-affecting items; commit per item, push when a coherent set is green.
- **Full remaining scope** (sum of items above): roughly **47 dev-days** of focused work on top of everything already delivered.
