# relay: Free-Tier Google Cloud Deployment

`relay` ships with one-command, environment-driven deployment to **Google Cloud Run on the free tier**, following the same operator pattern as the `support-master` repo (numbered steps, commit-SHA-tagged immutable images, out-of-band secrets, live health verification, and a written benchmark report).

```
 [ Your machine / CI ] ---> Env Vars (GOOGLE_CLOUD_PROJECT, GOOGLE_CLOUD_REGION[, METRICS_TOKEN])
                              |
        +---------------------+----------------------+
        | scripts/deploy.ps1 / scripts/deploy.sh     |
        |  1. Pre-flight (gcloud + git commit SHA)   |
        |  2. Enable APIs (run/cloudbuild/artifacts) |
        |  3. Artifact Registry repo (create-if-missing)
        |  4. Cloud Build -> {region}-docker.pkg.dev/.../{sha}
        |  5. Secret Manager (optional METRICS_TOKEN)|
        |  6. Cloud Run deploy (free-tier instance)  |
        |  7. Secret binding + PUBLIC_BASE_URL wiring|
        |  8. /healthz 204 + tokenized /metrics check|
        |  9. deploy-history/ benchmark report        |
        +---------------------------------------------+
```

## Cost profile (free tier)

- **Cloud Run free tier**: 2 million requests/month, 360,000 GB-seconds, 180,000 vCPU-seconds — enough for small ephemeral sharing. `min-instances 0` means the service scales to zero when idle.
- **Other services**: Cloud Build (first ~120 build-minutes/day free), Artifact Registry (small storage), Secret Manager (first secret free) — all effectively free at this scale.

## Prerequisites (one-time)

1. A Google Cloud project with billing enabled.
2. `gcloud` CLI installed and authenticated (`gcloud auth login`).

## Usage

PowerShell:

```powershell
$env:GOOGLE_CLOUD_PROJECT = "my-project-id"
$env:GOOGLE_CLOUD_REGION  = "us-central1"
$env:METRICS_TOKEN        = "super-secret-metrics-token"   # optional
.\scripts\deploy.ps1
```

Bash / Zsh / Cloud Shell:

```bash
export GOOGLE_CLOUD_PROJECT="my-project-id"
export GOOGLE_CLOUD_REGION="us-central1"
export METRICS_TOKEN="super-secret-metrics-token"          # optional
./scripts/deploy.sh
```

The script is idempotent: re-running on a later commit builds a new commit-SHA image and rolls the service forward.

## What the script does

1. Validates env vars and the authenticated `gcloud` account; captures the git commit SHA.
2. Enables `run`, `cloudbuild`, `artifactregistry`, and `secretmanager`.
3. Creates the `relay-backend` Artifact Registry repository if missing.
4. Builds and pushes an **immutable commit-SHA-tagged image** via Cloud Build (no `:latest` drift).
5. If `METRICS_TOKEN` is set, stores it in Secret Manager **out-of-band** (`gcloud secrets versions add --data-file=-`) — the value never appears in Terraform state, build logs, or the image.
6. Deploys `relay-backend` to Cloud Run with free-tier settings: `--min-instances 0 --max-instances 1`, 1 CPU / 512 MiB, port 8080, `--allow-unauthenticated` (transfer links must be public).
7. Grants the Cloud Run runtime service account `secretAccessor` on the secret, binds `METRICS_TOKEN`, and sets `PUBLIC_BASE_URL`/`ALLOWED_ORIGINS` to the live service URL.
8. Verifies `/healthz` returns HTTP 204 (polling up to ~60 s for cold start) and, when a token is set, that `/metrics` returns Prometheus text.
9. Writes `deploy-history/deploy_{timestamp}_{sha}.md` with per-step durations and the verification verdict (committed as deployment evidence).

## Important caveats

- **Single instance by design**: relay sessions live in process memory, so `max-instances` must stay `1`. If you later adopt sticky sessions or the Redis backend (see `IMPROVEMENT_PLAN.md` Track E), raise it.
- **Cold starts**: with `min-instances 0`, the first hit after idle can take a few seconds to warm up. For a sustained free-tier usage pattern this is fine; for latency-sensitive use set `--min-instances 1` at small cost.
- **`TRUST_PROXY=true`** is set because Cloud Run terminates TLS and forwards client IPs — this is required for correct rate-limit keying behind the platform.
- **Frontend origin**: set `ALLOWED_ORIGINS` to your Firebase Hosting origin if you host the SPA separately; when hosting the app on Cloud Run itself the origin equals the service URL (the default the script configures).
