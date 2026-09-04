# Next Batch Plan — Phase 1 Completion

Execution plan for the natural next batch of improvements, closing out the remaining Phase 1 work from `IMPROVEMENT_PLAN.md`. Items are sized for ~2 weeks of focused work and ordered by dependency.

| # | Item | Source item | Effort |
|---|------|-------------|--------|
| N1 | Logger migration (finish A9) | A9 (partial) | 1d |
| N2 | Documentation consolidation | A8 | 0.5d |
| N3 | Accessibility pass | D3 | 2d |
| N4 | Expiry countdown & link extension | D1 | 2d |
| N5 | E2E job in CI | E1 | 1d |
| | **Total** | | **~6.5 dev-days** |

---

## N1. Logger Migration (finish A9)

**Goal**: Remove all direct `console.*` calls from application code so production bundles carry no stray debug output, while preserving today's diagnostics under the dev log level.

**Current state** (verified): 42 direct `console.*` calls remain across:
- `frontend/src/App.tsx` — 16 (sender loop, receiver chunk/verify/status paths)
- `frontend/src/transport/WebRTCTransport.ts` — 13
- `frontend/src/transport/AutoTransport.ts` — 9
- `frontend/src/transport/WebSocketRelayTransport.ts` — 1

`frontend/src/services/logger.ts` already exists (committed): `logger.debug/info/warn/error`, threshold from `VITE_LOG_LEVEL`, defaulting to `debug` in dev and `warn` in production.

**Approach**:
1. Mechanically replace every `console.log(...)` → `logger.debug(...)`, `console.error(...)` → `logger.error(...)` in the four files above. Add `import { logger } from '../services/logger'` (transports use `../`, App.tsx uses `./`).
2. Classify each call honestly:
   - Per-chunk progress spam (`[Sender] Sending chunk:`, `[Receiver] Chunk index:`) → `logger.debug` (hidden in prod).
   - Signaling relay traces (ICE state, offer/answer) → `logger.debug`.
   - Genuine failures (loop errors, hash mismatch, transport errors, connect failures) → `logger.error` (visible in prod).
   - Out-of-order/invalid chunk, size/hash mismatch messages → `logger.error`, since these are user-visible failure paths with diagnostic value.
3. Keep at least one structured `logger.debug` breadcrumb per lifecycle phase (join → offer → accepted → transferring → complete/cancel) so a dev-mode console still tells the full story, matching today's output.
4. Do **not** gate console output in `e2e/transfer.spec.ts` (Playwright's `page.on('console', ...)` harness intentionally reads browser console) — that is test code, not app code.

**Files**: `App.tsx`, `transport/WebRTCTransport.ts`, `transport/AutoTransport.ts`, `transport/WebSocketRelayTransport.ts`.

**Acceptance criteria**:
- `grep -c "console\." src/ --include=*.ts --include=*.tsx` returns 0 (excluding `logger.ts` itself and test files).
- `npm run build` output contains no `console.log`/`console.error` string literals from app code (spot-check the bundle).
- Dev-mode transfer still produces a readable event trail; prod build produces only warnings/errors.
- Existing transport unit tests (`transport.test.ts`) still pass.

---

## N2. Documentation Consolidation (A8)

**Goal**: End the `README.md` vs `HOW_TO_RUN.md` drift by making one canonical README and retiring the duplicate.

**Current state**: `README.md` (7.4 KB) and `HOW_TO_RUN.md` (4.0 KB) overlap heavily: both document env vars, local setup, testing commands, and transport modes; neither is authoritative. Three other docs exist: `FEATURES_EXPLAINER.md`, `IMPROVEMENT_PLAN.md`, `PRODUCTION_READINESS_PLAN.md`.

**Approach**:
1. Build the merged `README.md` with the anchor structure already established in `IMPROVEMENT_PLAN.md` A8:
   - Overview & structure (from current README)
   - Quick start (local dev: backend + frontend commands)
   - Configuration (single env-var tables — one for backend, one for frontend build vars)
   - Protocol summary (link to FEATURES_EXPLAINER for depth)
   - Transports
   - Testing (unit + E2E commands)
   - Deployment (Firebase + Cloud Run paths, `maxScale: 1` rationale)
   - Security model & limitations
2. Harvest anything unique in `HOW_TO_RUN.md` before deleting it:
   - The PowerShell/Linux dual syntax examples for `PUBLIC_BASE_URL`.
   - The explicit WebRTC vs relay mode descriptions and TURN notes.
   - The exact env var spellings with defaults.
3. Replace `HOW_TO_RUN.md` with a 6-line stub pointing at README anchors (so old links/`#` references don't 404) — or delete outright if repo hygiene is preferred. Recommend: delete, since it is untracked from git history already retrievable and the stub itself is future drift.
4. Add a "Related documents" section linking `FEATURES_EXPLAINER.md`, `IMPROVEMENT_PLAN.md`, and `PRODUCTION_READINESS_PLAN.md`.
5. Update `PRODUCTION_READINESS_PLAN.md` and `IMPROVEMENT_PLAN.md` references to `HOW_TO_RUN.md` if any exist.

**Files**: `README.md` (rewrite), `HOW_TO_RUN.md` (delete).

**Acceptance criteria**: `HOW_TO_RUN.md` gone; README contains every env var + command that previously lived in either file; no broken internal references; rendered README reads top-to-bottom without duplication.

---

## N3. Accessibility Pass (D3)

**Goal**: Make the app usable by keyboard and screen reader, with no WCAG 2.1 AA violations on the core flows (choose file → share → receive).

**Current state** (verified):
- The dropzone is a `<div className="card dropzone" onDragOver onDrop>` — not keyboard-operable, no ARIA role.
- Transport mode options are `<div className="transport-option" onClick>` — not focusable, no radio semantics.
- Progress/status text is not announced (`role="status"`/`aria-live` absent).
- Muted text (#94a7a0) on page background (#101918) — contrast is marginal for small text.
- Hover-only affordances (dropzone lift, radio fill, button brightness) have no `:focus-visible` equivalents.

**Approach**:
1. **Dropzone**: give the visible drop card a keyboard path. The cleanest pattern given the hidden input: wrap the drop zone so the existing "Choose a file" button (real `<button>`) remains the primary keyboard route, and add `role="button"` + `tabIndex={0}` + Enter/Space activation + an explicit `aria-label="Drop a file here or activate to choose one"` to the card for parity between mouse drop and keyboard. Guard Enter/Space so it does not double-fire when focus is on the inner button.
2. **Transport selector**: convert the three option cards to a proper `role="radiogroup"` with focusable radio semantics:
   - `role="radio"`, `aria-checked`, and keyboard arrow-key navigation between options, or — simpler and more robust — replace the visual-only cards' click surface with real `<label>` + hidden native `<input type="radio">` styled by the existing CSS (radio circles already exist). Native radios give free keyboard/AT support; CSS `:checked` can drive the `.selected` look.
   - Ensure disabled states (`!supportsWebRTC`) set `disabled`/`aria-disabled` appropriately.
3. **Live regions**: add `role="status"` (polite) to the sender's status line and receiver's state line so progress/outcome text is announced; add `aria-live="polite"` to the progress percentage.
4. **Diagnostics toggle**: ensure it is a real `<button>` (it already is) with an `aria-expanded` bound to the open state.
5. **Contrast & focus**: bump `.muted`/`.privacy`/`.transport-desc` color from `#94a7a0` to ≥ `#a8bab3` (target ≥ 4.5:1 on `#101918`); add visible `:focus-visible` outlines (2px lime) for all interactive elements; add `@media (prefers-reduced-motion: reduce)` to disable the dropzone lift and radio-fill transitions.
6. **Tooling**: add an `axe-core` scan step to the Playwright E2E config (or a new `e2e/a11y.spec.ts`) asserting no violations on `/`, `/transfer/:id`, and `/receive/:id` states that are reachable without a live peer.

**Files**: `App.tsx`, `styles.css`, `e2e/a11y.spec.ts` (new, optional), `package.json` (add `axe-core` + `@axe-core/playwright` dev deps).

**Acceptance criteria**: axe-core scan clean on the three routes; full sender flow completable keyboard-only (Tab to file input → Enter to open picker → create → share); screen reader announces file-selected state and transfer completion; no visual regression to the current aesthetic.

---

## N4. Expiry Countdown & Link Extension (D1)

**Goal**: Show the sender exactly how long their link remains valid, escalate urgency as expiry nears, and let them extend an about-to-expire link so a walking receiver doesn't hit a dead link.

**Current state** (verified): The backend `POST /api/transfers` response already includes `expiresAt`, and `GET /api/transfers/:id` already returns the session snapshot including `ExpiresAt`. The sender's `create()` in `App.tsx` **does not persist `expiresAt`** to `sessionStorage`, so the sender page cannot render a countdown from saved state after a refresh. There is no extension endpoint.

**Approach**:
1. **Persist expiry**: in `Home.create()`, add `expiresAt: result.expiresAt` to the `sessionStorage.setItem("sender:${id}", ...)` payload and to the `navigate` state object. Type the saved-session shape accordingly (add `expiresAt?: string`).
2. **Countdown UI** (Sender page): a small mono line under the link box — `Link expires in 12:34`. Tick every second from a `useEffect` interval, deriving remaining from `Date.parse(expiresAt) - Date.now()`. Behavior:
   - `< 2 min`: color shifts to amber/red and the line reads `Link expires in 01:23 — extend?` with an inline action.
   - `≤ 0`: show `Link expired — create a new transfer`, and (since the backend would now reject joins) stop any active transport and surface the expired state.
   - If `expiresAt` is absent (legacy saved session), fall back to today's behavior (no countdown) — do not break old sessions.
3. **Extend endpoint** (backend): `POST /api/transfers/{id}/extend`, sender-token-authenticated, rate-limited (reuse `CreateLimiter` keyed by IP, or a lighter per-transfer limiter):
   - Only while non-terminal (reject `Completed`/`Cancelled`/`Failed`/`Expired`).
   - New `ExpiresAt = min(now + TTL, ExpiresAt + TTL)` — extends by at most one TTL per call, capped so it can't grow unbounded.
   - Returns the new `expiresAt` snapshot.
   - Route it under the existing `/api/transfers/` mux with method + token checks (mirror `server.get`'s pattern but require `Authorization: Bearer {senderToken}` or an `X-Sender-Token` header; note the current app sends the sender token only in the WS join, so choose a header and update the client).
   - Add `httpapi` tests: extend succeeds while `waiting_for_receiver`, rejected when terminal, capped at +1 TTL, wrong token rejected.
4. **Client wiring**: add `extendTransfer(id, senderToken)` to `api.ts`; on the Sender page, the "Extend" action calls it, updates the saved `expiresAt`, and restarts the countdown. Disable after one use until the cap logic allows another (or simply rely on the backend cap and surface errors).

**Protocol note**: the WebSocket relay is unaffected — `expiresAt` refresh is purely an HTTP/state concern; existing sessions that expire mid-transfer still follow today's pause/fail path.

**Files**: `App.tsx`, `api.ts`, `types.ts` (saved-session shape), `websocket/handler.go` (no change), `httpapi/handlers.go` (new handler + route), `httpapi/handlers_test.go`, `manager.go` (extend method on Manager/Session), `session.go`.

**Acceptance criteria**: E2E-style test: create a transfer with a short TTL (config override) → countdown visible and ticking → extend → `expiresAt` in the UI and session advances by TTL → after expiry the page shows the expired state and further joins fail with "This transfer has expired."; unit tests cover the extend cap and terminal-state rejection.

---

## N5. E2E Job in CI (E1)

**Goal**: Make the existing Playwright suite (`e2e/transfer.spec.ts`) part of CI so the full-stack byte-exact proof runs on every push/PR, not just locally.

**Current state** (verified): `.github/workflows/ci.yml` has `backend`, `frontend`, and `container` jobs. The Playwright config (`playwright.config.ts`) already defines `webServer` entries that start the Vite dev server (port 5173) and Go backend (`go run ./cmd/server`, port 8080) with `reuseExistingServer: true`. E2E currently runs only via `npm run test:e2e` locally.

**Approach**:
1. **New `e2e` job** in `ci.yml`, `needs: [backend, frontend]` so a broken build never wastes browser time:
   ```yaml
   e2e:
     runs-on: ubuntu-latest
     needs: [backend, frontend]
     defaults:
       run:
         working-directory: frontend
     steps:
       - uses: actions/checkout@v4
       - uses: actions/setup-node@v4
         with:
           node-version: 20
           cache: npm
           cache-dependency-path: frontend/package-lock.json
       - uses: actions/setup-go@v5
         with:
           go-version: '1.22.x'
       - run: npm ci
       - run: npx playwright install --with-deps chromium
       - run: npm run build          # production bundle for `vite preview`
       - run: npm run test:e2e
       - uses: actions/upload-artifact@v4
         if: failure()
         with:
           name: playwright-results
           path: frontend/test-results/
           retention-days: 7
   ```
2. **Switch the webServer command from dev to preview**: production-like serving catches build/runtime mismatches. Update `playwright.config.ts` `webServer[0].command` to `npm run preview -- --host 127.0.0.1 --port 5173`; keep `reuseExistingServer: true` so local dev workflow is unchanged (dev server on 5173 is reused when present).
3. **Playwright browser install is the slow step** — cache it (`~/.cache/ms-playwright`) with the standard Playwright actions/cache snippet to keep the job ≈ 2–3 min.
4. **Coverage note**: the existing spec covers the critical single-file path. Add a second spec later (out of scope here) for password/E2E and multi-file once those land.

**Files**: `.github/workflows/ci.yml`, `playwright.config.ts`.

**Acceptance criteria**: a PR that breaks the transfer flow fails the `e2e` job with an uploaded `test-results/` artifact; a clean PR is fully green across all four jobs; local `npm run test:e2e` still works with a running dev server.

---

## Execution Order

| Day | Work |
|-----|------|
| 1 | N1 logger migration (mechanical + classification), run unit tests + build |
| 1–2 | N2 docs consolidation |
| 2–4 | N3 accessibility (DOM/semantics → CSS → axe scan) |
| 4–6 | N4 expiry countdown + extend endpoint (backend first, then client) |
| 6 | N5 E2E CI job + playwright preview switch |

N2 and N5 are independent and can be parallelized; N3's axe tooling and N4's client work both touch `App.tsx`, so sequence them to avoid merge churn (N3 then N4, or N4 then N3 — do N4's sessionStorage shape change first to minimize re-touching the countdown UI after N3 semantics land).

## Validation Gate
After each item: `go test ./...`, `go test -race ./...`, `go vet ./...`, `npm test`, `npm run build`, and for N3/N5 the Playwright run. Commit per item with a focused message; push when the batch (or a coherent subset) is green.
