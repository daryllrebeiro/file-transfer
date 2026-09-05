# Changelog

All notable changes to this project are documented in this file. Releases are tagged `vX.Y.Z`; the workflow in `.github/workflows/release.yml` builds, pushes, deploys staging, and publishes GitHub releases automatically.

## [Unreleased]

### Added
- i18n foundation with `en`/`de`/`es`/`fr` and a header language selector.
- PWA shell (`manifest.webmanifest`, service worker) and OS text share target.
- Send-text mode on the home page (64 KB text as `message.txt`).
- Local "Recent transfers" history with resume affordance.
- Light theme toggle.
- Prometheus exposition format for `/metrics` (`METRICS_FORMAT`).
- Structured `log/slog` logging with connection IDs and lifecycle events.
- Session map sharding (16 shards) for lock scalability.
- Adaptive relay chunk sizing, parallel out-of-order chunk admission, and dynamic in-flight window tuning.
- Backend load-testing harness (`backend/loadtest`).
- Accessibility pass (native radio transport selector, live regions, focus/reduced-motion, axe-core scans).
- Link expiry countdown with authenticated extend endpoint.
- WebSocket close ordering fixes and relay-sender start gating (wait for acceptance).
- E2E coverage for server-relay multi-chunk transfers.

### Changed
- Consolidated `README.md`; removed `HOW_TO_RUN.md`.
- Dependency versions pinned; `npm audit` and `golangci-lint` gates in CI; Dependabot enabled.

## [0.1.0] — MVP baseline
- WebRTC peer-to-peer and server relay transports with automatic fallback.
- SHA-256 integrity verification, chunked streaming with ACK window, capability-token security, QR sharing.
- Docker/Cloud Run deployment assets.
