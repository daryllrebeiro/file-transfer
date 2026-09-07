# Architectural Review & Roadmap

## Executive Summary

The file-transfer application implements a modern, dual-transport file sharing system with WebRTC peer-to-peer and server relay modes. The architecture is cleanly layered but currently optimized for feature completeness rather than production-scale resilience. This document reviews the current state and proposes a phased roadmap toward a production-grade system.

---

## Current Architecture

### Backend (Go)

**Process Model**: Single Go binary serving HTTP API, WebSocket signaling, and TURN credentials on one port.

**Transfer Manager** (`internal/transfer/manager.go`):
- 16-shard in-memory session store with per-shard RWMutex
- Token-based authentication using SHA-256 hashed tokens with constant-time comparison
- State machine: `WaitingForReceiver` → `WaitingForAccept` → `Transferring` → `Completing` → `Completed` (with `Paused` and `Cancelled` branches)
- Adaptive chunk sizing with parallel window support
- Atomic metrics counters for observability
- TTL-based expiration with background cleanup ticker

**WebSocket Handler** (`internal/websocket/handler.go`):
- Gorilla WebSocket with per-peer outbound queue (size 4)
- Control message protocol for signaling; binary forwarding for relay chunks
- Role-based routing (sender/receiver)
- WebRTC signaling pass-through (offer/answer/ICE)
- Connection limit enforcement

**Scaling Layer** (`internal/signaling/redis.go`):
- Redis pub/sub on a single `signaling` channel
- Per-transfer handler registry for local dispatch
- Enables multi-instance deployments where signaling crosses server boundaries

**TURN Service** (`internal/turn/handler.go`):
- Time-limited credential generation (timestamp-based username, random password)
- Origin validation with wildcard subdomain support

### Frontend (React)

**Transport Abstraction** (`frontend/src/App.tsx`):
- `TransportFactory` pattern supporting `auto`, `webrtc`, and `relay` modes
- Auto-fallback from WebRTC to relay with diagnostic reporting
- Chunk-based streaming with SHA-256 integrity verification
- IndexedDB-backed durable resumption
- Device pairing via QR code and deep links
- Text mode (64 KB) and multi-file support

---

## Strengths

1. **Clean separation of concerns**: HTTP API, WebSocket signaling, transfer state, and transport logic are decoupled.
2. **Concurrency-safe design**: Sharded locking minimizes contention in the transfer manager.
3. **Transport flexibility**: Auto-fallback between WebRTC and relay provides robust connectivity.
4. **Security foundations**: Token hashing, constant-time comparison, origin validation, and encryption metadata validation are in place.
5. **Frontend resilience**: Durable resumption, integrity checking, and pause/cancel controls provide a solid user experience.

---

## Identified Bottlenecks & Weaknesses

### 1. In-Memory State Without Persistence
The `Manager` stores all session state in process memory. A server restart or crash loses all active transfers, and there is no mechanism to reconstruct sessions from persistent storage.

**Impact**: Users mid-transfer must restart. Horizontal scaling requires sticky sessions or shared-nothing designs that don't survive restarts.

### 2. Single Redis Signaling Channel
All pub/sub messages flow through one Redis channel (`signaling`). At high scale, this becomes a serialization point and increases latency for unrelated transfers.

**Impact**: Throughput ceiling determined by single-channel Redis performance. Cross-instance signaling adds unnecessary overhead for same-instance transfers.

### 3. Synchronous Chunk Forwarding
`Handler.forward()` processes chunks synchronously within the WebSocket read loop. Under high throughput, this blocks message processing and can cause read deadline misses.

**Impact**: Latency spikes and potential connection drops under load.

### 4. No Backpressure in Relay Path
The relay path accepts chunks as fast as the sender produces them, with only a 4-message outbound queue. A slow receiver can cause queue saturation and dropped chunks without graceful degradation.

**Impact**: Transfer failures under receiver-side slowdowns.

### 5. Predictable TURN Credentials
TURN usernames embed a Unix timestamp, making them predictable and vulnerable to credential guessing if the endpoint is exposed.

**Impact**: Potential unauthorized TURN usage if the credential endpoint is discovered.

### 6. In-Memory Metrics
Metrics are held in atomic counters and exposed via `/metrics` but are lost on restart. There is no historical monitoring or alerting integration.

**Impact**: No capacity planning data or incident forensics across restarts.

### 7. Frontend Bundle Cohesion
`App.tsx` is 846 lines with multiple responsibilities. As features grow, the bundle size and cognitive load increase without code splitting or state management abstraction.

**Impact**: Slower initial load and harder maintenance.

### 8. Error Swallowing
`defer redisSignaling.Close()` in `main.go` ignores close errors, and some WebSocket write operations silently drop errors after logging.

**Impact**: Resource leaks and silent failures in production.

---

## Proposed Roadmap

### Phase 6: Resilience & Observability

**Goal**: Make the system survive partial failures and provide operational insight.

| Item | Description | Priority |
|------|-------------|----------|
| **Session Persistence** | Add Redis-backed session store as primary or write-through cache. Enable crash recovery and horizontal scaling without sticky sessions. | High |
| **Health Checks** | Implement `/healthz` with subsystem checks (Redis ping, TURN reachability, manager stats). Add readiness/liveness probes. | High |
| **Structured Metrics** | Export Prometheus metrics with labeled histograms (transfer size, duration, mode). Persist to long-term storage. | Medium |
| **Distributed Tracing** | Instrument request lifecycle with OpenTelemetry spans across API → WebSocket → manager. | Medium |
| **Graceful Restart** | Implement draining mode: stop accepting new transfers, wait for active to complete or pause, then restart. | Medium |

### Phase 7: Performance & Scale

**Goal**: Remove throughput ceilings and reduce per-transfer overhead.

| Item | Description | Priority |
|------|-------------|----------|
| **Redis Channel Sharding** | Route signaling messages to transfer-specific channels or use Redis Streams. Reduce contention and latency. | High |
| **Async Chunk Pipeline** | Decouple WebSocket read loop from chunk forwarding using a worker pool. Prevents blocking on slow receivers. | High |
| **Backpressure Protocol** | Implement window-based flow control in relay mode. Sender blocks or slows when receiver queue is deep. | Medium |
| **Connection Pooling** | Use Redis connection pool with min/max bounds. Monitor pool saturation. | Medium |
| **Frontend Code Splitting** | Split `App.tsx` into route-based chunks. Lazy-load diagnostics and history panels. | Low |

### Phase 8: Security Hardening

**Goal**: Harden the system against adversarial use and data exposure.

| Item | Description | Priority |
|------|-------------|----------|
| **HMAC TURN Credentials** | Replace timestamp-based usernames with HMAC-signed tokens using a shared secret. Prevent credential prediction. | High |
| **WebSocket Rate Limiting** | Add per-IP and per-transfer rate limiting on join attempts. Prevent DoS via connection flooding. | High |
| **Content Security Policy** | Add CSP headers to prevent XSS in the frontend. Implement nonce-based script allowances. | Medium |
| **Audit Logging** | Log transfer creation, completion, and cancellation with actor context. Ship to immutable storage. | Medium |
| **Encrypted Metadata at Rest** | If session persistence is added, encrypt file names and sizes in the session store. | Low |

### Phase 9: Advanced Features

**Goal**: Improve user experience and expand use cases.

| Item | Description | Priority |
|------|-------------|----------|
| **Browser Notifications** | Notify on transfer completion, pause, and expiration using the Notifications API. | Medium |
| **Batch Transfer Management** | Group multiple files into a single transfer with manifest download (ZIP). | Medium |
| **Transfer Presets** | Allow users to save common configurations (TTL, transport mode, encryption). | Low |
| **WebRTC Stats Export** | Expose ICE candidate pairs, bytes sent/received, and RTT via diagnostics panel. | Low |
| **Mobile Companion** | Explore React Native or PWA enhancements for mobile share sheet integration. | Low |

---

## Migration Strategy

1. **Session Persistence**: Introduce Redis session store alongside in-memory manager. Use feature flag to switch. Keep in-memory as fallback.
2. **Redis Sharding**: Update `RedisSignaling` to use stream-based channels. Deploy with dual-write during transition.
3. **Async Pipeline**: Add worker pool to `Handler` without changing protocol. Backward compatible.
4. **TURN HMAC**: Version the credential endpoint (`/v1/turn-creds`). Support both legacy and HMAC during rollout.

---

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| In-memory state loss during scaling | High | High | Implement session persistence first |
| Redis single channel saturation | Medium | Medium | Implement channel sharding before hitting scale |
| TURN credential exposure | Low | High | Rotate shared secrets; implement HMAC |
| Frontend bundle bloat | Medium | Low | Code splitting and lazy loading |

---

## Conclusion

The application has a solid architectural foundation with clean abstractions and security-conscious design. The primary gaps are in operational resilience (persistence, observability) and high-scale performance (async processing, signaling efficiency). The proposed roadmap addresses these gaps in dependency order: persist state before optimizing scale, harden security before expanding features.
