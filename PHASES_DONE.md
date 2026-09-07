# Phases Completed Log

## Phase 1: Debt Zero ✅
- A4: Buffer Pooling for WebRTC sub-chunk assembly
- P2-13: CreateLimiter boundary tests
- P2-17: Integration tests for full protocol flow
- E4: Dependency hygiene (golangci-lint, npm audit, dependabot, Docker SBOM)
- E5: Release engineering (tag-driven workflow, changelog)
- E6: Free-tier GCP deployment automation scripts
- C5: Cancel/pause controls (sender cancel, receiver pause/restart)
- B5: Web Worker hashing (move SHA-256 to worker)

## Phase 2: Protocol Performance ✅
- B1: Adaptive Chunk Sizing (AIMD based on ACK latency)
- B2: Parallel (Out-of-Order) Chunks
- B3: Dynamic Window Scaling
- E2: Load Testing Harness

## Phase 3: UI/UX Features ✅
- D6: Send Text Mode (64 KB text as message.txt)
- D7: Dark/Light Theme Toggle
- D3: Accessibility Pass (native radio, live regions, focus/reduced-motion, axe-core)

## Phase 4: Core Features ✅
- C1: Password / E2E Encryption ✅
- C2: Multi-file / Folders Support (backend types ✅)
- C3: Durable Resumption (IndexedDB) ✅
- C4: Device Pairing ✅

## Phase 5: Scaling & Infrastructure ✅
- B4: TURN Deployment Path ✅
- E3: Horizontal Scaling (Redis pub/sub) ✅