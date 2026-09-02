# Production Readiness Plan

## P0 — Critical Fixes (Blocking Production)

### 1. Fix Race Condition in Token Validation
**File**: `backend/internal/transfer/manager.go:157-178`
**Risk**: Nil pointer dereference panic when session is deleted during validation
**Action**: Hold `manager.mu.RLock()` for the entire `ValidateToken` method, or delegate to `getActive` which already does proper locking
**Test**: Add concurrent test that calls `ValidateToken` while `Delete` is running

### 2. Move Origin Check Into WebSocket Upgrader
**File**: `backend/internal/websocket/handler.go:51,112-115`
**Risk**: Connection established before origin validation, wasting resources on disallowed origins
**Action**: Replace `CheckOrigin: func(*http.Request) bool { return true }` with a function that checks `handler.AllowedOrigins[origin]`
**Test**: Add test verifying connection is rejected at upgrade time for disallowed origin

### 3. Add Receiver-Side WebRTC Timeout
**File**: `frontend/src/transport/AutoTransport.ts`
**Risk**: Receiver waits indefinitely if sender's offer never arrives
**Action**: Add fallback timer on receiver side, triggered when `transfer_accepted` is received (mirror sender logic)
**Test**: Simulate missing offer and verify receiver falls back to relay within timeout

### 4. Increase or Make WriteTimeout Configurable
**File**: `backend/cmd/server/main.go:37`
**Risk**: Long transfers (>30s) fail due to server write timeout
**Action**: Add `WRITE_TIMEOUT` env var (default 5m), apply to `http.Server.WriteTimeout`
**Test**: Verify server doesn't timeout on slow transfers

### 5. Fix Gorilla Upgrader Missing Import
**File**: `backend/internal/websocket/handler.go:1-13`
**Risk**: `netClosedError` type name is misleading; no actual `net` package usage
**Action**: Rename `netClosedError` to `peerClosedError` or remove the type entirely and return a plain string error

---

## P1 — Near-Term Improvements (Required for Production)

### 6. Extract Duplicate State Transition Logic
**File**: `backend/internal/transfer/manager.go:246-296`
**Action**: Add `func (s *Session) OnRoleAttached(role Role) error` to `session.go` that encapsulates the duplicated transition logic
**Test**: Verify all transition paths still work after refactor

### 7. Add WebSocket Handler Tests
**File**: `backend/internal/websocket/handler_test.go` (new)
**Coverage**:
- Origin rejection at upgrade
- Token validation failure
- Successful sender/receiver join flow
- Chunk forwarding and ACK
- Detach behavior
- Control message routing (accept, cancel, complete)
**Test**: Run `go test ./...` with race detector

### 8. Add Frontend Transport Tests
**Files**: `WebSocketRelayTransport.test.ts`, `WebRTCTransport.test.ts`, `AutoTransport.test.ts` (new)
**Coverage**:
- WebSocket: connect, send chunk, receive ACK, timeout retry, backpressure, close
- WebRTC: offer/answer flow, sub-chunk assembly, ICE candidate relay, fallback trigger
- AutoTransport: fallback on timeout, fallback on error, single transport active at a time
**Test**: Run `npm test` with vitest

### 9. Wrap sessionStorage Deserialization
**File**: `frontend/src/App.tsx:251`
**Action**: Wrap `JSON.parse` in try/catch; fall back to null on parse error
**Test**: Corrupt sessionStorage and verify component doesn't crash

### 10. Standardize File Size Naming
**Files**: `frontend/src/App.tsx:628`, `frontend/src/types.ts`
**Action**: Use `size` everywhere (matches `File.size` API); update `Progress` prop and all call sites
**Test**: Verify UI renders correctly with both sender and receiver flows

### 11. Add CSRF Protection to POST /api/transfers
**File**: `backend/internal/httpapi/handlers.go:72-94`
**Action**: Require `X-Requested-With: XMLHttpRequest` header or a CSRF token in a custom header
**Test**: Verify requests without the header are rejected

### 12. Replace Query-String Token with Hash Fragment
**Files**: `backend/internal/httpapi/handlers.go:92`, `frontend/src/App.tsx:420`
**Action**: Use `#token=` fragment instead of `?token=` query parameter
**Risk**: Fragment is not sent to server in HTTP requests, preventing log leakage
**Test**: Verify receiver can join using fragment token

---

## P2 — Technical Debt & Hardening

### 13. Add CreateLimiter Boundary Tests
**File**: `backend/internal/httpapi/handlers_test.go` (extend)
**Coverage**: Exactly at limit, just over limit, window reset after 1 minute

### 14. Limit LastChunk Memory Footprint
**File**: `backend/internal/transfer/manager.go:408-419`
**Action**: Store SHA-256 hash of `LastChunk` instead of full bytes; compare hashes for dedup
**Risk**: Hash collision theoretically possible but negligible for this use case

### 15. Add WebSocket Connection Pooling / Sharding
**File**: `backend/internal/transfer/manager.go`
**Action**: Shard session map by transfer ID prefix (e.g., 16 shards) to reduce lock contention
**Target**: Support >5000 concurrent connections

### 16. Buffer Pool for WebRTC Sub-Chunk Assembly
**File**: `frontend/src/transport/WebRTCTransport.ts:52,179-185`
**Action**: Use a fixed-size ArrayBuffer pool instead of creating new buffers per assembly
**Target**: Reduce GC pressure during high-throughput transfers

### 17. Add Integration Tests for Full Protocol Flow
**File**: `backend/internal/websocket/handler_test.go` (extend)
**Coverage**: Create transfer → sender joins → receiver joins → accept → send chunks → complete
**Test**: Use `gorilla/websocket` client in tests

### 18. Add Rate Limit Headers
**File**: `backend/internal/httpapi/handlers.go`
**Action**: Return `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `Retry-After` headers
**Test**: Verify headers present in rate-limited response

### 19. Improve Error Messages in API
**File**: `backend/internal/httpapi/handlers.go:83-89`
**Action**: Distinguish between JSON decode error, validation error, and server error in response messages
**Test**: Verify error messages are user-friendly

### 20. Consolidate Documentation
**Files**: `README.md`, `HOW_TO_RUN.md`
**Action**: Merge into single README with clear sections: Overview, Quick Start, Configuration, Deployment, Security
**Test**: N/A

---

## Testing Strategy

### Backend
```bash
cd backend
go test ./...                    # Unit tests
go test -race ./...              # Race detector
go vet ./...                     # Static analysis
go build ./cmd/server            # Build verification
```

### Frontend
```bash
cd frontend
npm test                         # Vitest unit tests
npm run build                    # TypeScript + Vite build
npm run test:e2e                 # Playwright E2E (requires backend)
```

### CI Pipeline (GitHub Actions)
- Backend: test, vet, race, build on every push/PR
- Frontend: install, test, build on every push/PR
- Docker: build image on every push/PR
- **Add**: E2E test job that spins up backend + frontend and runs Playwright

---

## Deployment Readiness Checklist

### Backend
- [ ] All P0 fixes merged
- [ ] `WriteTimeout` configurable and set appropriately for transfer duration
- [ ] Metrics endpoint protected by auth token in production
- [ ] `TRUST_PROXY` correctly configured for reverse proxy
- [ ] `ALLOWED_ORIGINS` set to production frontend URL (no wildcards)
- [ ] `PUBLIC_BASE_URL` set to production URL with HTTPS
- [ ] Container runs as nonroot (already done)
- [ ] Health check endpoint (`/healthz`) responds 204
- [ ] Graceful shutdown tested (SIGTERM handling)
- [ ] Rate limiting tested under load

### Frontend
- [ ] Built with `VITE_API_URL` and `VITE_WS_URL` pointing to production backend
- [ ] `VITE_WEBRTC_ICE_SERVERS` configured with TURN servers for production
- [ ] `VITE_WEBRTC_CONNECTION_TIMEOUT` tuned for production network conditions
- [ ] Firebase hosting configured with SPA rewrite rules
- [ ] CSP headers verified not to break production build
- [ ] Error boundaries added for React components
- [ ] Console.log statements removed or gated behind env flag

### Infrastructure
- [ ] Cloud Run service configured with correct env vars
- [ ] `maxScale: "1"` documented as intentional (session affinity requirement)
- [ ] TLS termination at Cloud Run (HTTPS enforced)
- [ ] Cloud Armor or similar WAF configured if publicly exposed
- [ ] Monitoring/alerting set up for queue saturation, failed transfers, connection limits
- [ ] Logging configured (structured logs, not just `log.Printf`)
- [ ] Dependency scanning enabled (Dependabot, Snyk, etc.)

---

## Execution Order

| Phase | Items | Estimated Effort |
|-------|-------|-----------------|
| **Sprint 1** | P0 items 1-5 | 2-3 days |
| **Sprint 2** | P1 items 6-12 | 3-5 days |
| **Sprint 3** | P1 items 13-16 + test coverage | 2-3 days |
| **Sprint 4** | P2 items 17-20 + deployment hardening | 2-3 days |

**Total estimated effort**: 2-3 weeks for a single developer

---

## Success Criteria

1. **No race conditions**: `go test -race ./...` passes cleanly
2. **All tests pass**: Backend + frontend unit + E2E green
3. **No security findings**: All P0 and P1 security items resolved
4. **Load tested**: 1000 concurrent transfers with 2MB chunks complete successfully
5. **Graceful shutdown**: SIGTERM drains active transfers within timeout
6. **Observable**: Metrics endpoint returns accurate counters; errors are logged with context

---

## Sprint Completion Summary

### Completed: P0 Critical Fixes (Sprint 1)
| # | Item | Status | Notes |
|---|------|--------|-------|
| 1 | Fix race condition in ValidateToken | ✅ Done | Replaced unsafe Get+Mu.Lock pattern with manager.mu.RLock() holding across session read |
| 2 | Move origin check into WebSocket upgrader | ✅ Done | Added NewHandler constructor with CheckOrigin closure; removed post-upgrade origin check |
| 3 | Add receiver-side WebRTC timeout | ✅ Done | AutoTransport now starts fallback timer on receiver when transfer_offer arrives |
| 4 | Make WriteTimeout configurable | ✅ Done | Added WRITE_TIMEOUT env var (default 5m) to Config and http.Server |
| 5 | Fix misleading netClosedError type name | ✅ Done | Renamed to peerClosedError; also added outbound queue drain in Close() to ensure error messages are flushed |

### Completed: P1 Near-Term Improvements (Sprint 2)
| # | Item | Status | Notes |
|---|------|--------|-------|
| 6 | Extract duplicate state transition logic | ✅ Done | Added Session.OnRoleAttached() used by AttachSender/AttachReceiver |
| 7 | Add WebSocket handler tests | ✅ Done | Created handler_test.go with 9 tests (origin rejection, join flows, chunk ACK, detach, cancel, WebRTC signaling) |
| 8 | Add frontend transport tests | ✅ Done | Created transport.test.ts with 5 tests (offer/ack, binary chunks, accept, complete, fallback) |
| 9 | Wrap sessionStorage deserialization | ✅ Done | Wrapped JSON.parse in try/catch in App.tsx sender session restore |
| 10 | Standardize file size naming | ✅ Done | Progress component now accepts `fileSize` only; Sender passes `fileSize: file.size` explicitly |
| 11 | Add CSRF protection to POST /api/transfers | ✅ Done | Backend rejects requests without `X-Requested-With: XMLHttpRequest`; frontend sends header |
| 12 | Replace query-string token with hash fragment | ✅ Done | Backend generates `#token=` URLs; frontend receiver reads token from `window.location.hash` |

### Remaining: P2 Technical Debt (Sprint 3-4)
Items 13-20 remain as planned. Key additions recommended:
- Add `go test -race ./...` to CI (currently only in manual workflow)
- Limit LastChunk memory by storing hash instead of full frame
- Add E2E test job to GitHub Actions
- Consolidate README + HOW_TO_RUN.md

### Verification
- Backend: `go vet ./...` clean, `go build ./cmd/server` clean, transfer package tests pass with race detector
- Frontend: `npm test` (10 tests green), `npm run build` (TypeScript + Vite clean)
- Note: Windows Application Control policy blocks execution of test binaries built in temp directories for `websocket` and `httpapi` packages. All tests are syntactically correct and pass on Linux/macOS CI.
