#!/usr/bin/env bash
# relay: one-command, env-driven Google Cloud Run deployment (free tier).
# Mirrors scripts/deploy.ps1 for Bash / Zsh / Cloud Shell.
#
# Usage:
#   export GOOGLE_CLOUD_PROJECT="my-project-id"
#   export GOOGLE_CLOUD_REGION="us-central1"
#   export METRICS_TOKEN="optional-secret"
#   ./scripts/deploy.sh
set -euo pipefail

PROJECT_ID="${GOOGLE_CLOUD_PROJECT:?GOOGLE_CLOUD_PROJECT is not set}"
REGION="${GOOGLE_CLOUD_REGION:-us-central1}"
METRICS_TOKEN="${METRICS_TOKEN:-}"
SERVICE_NAME="${SERVICE_NAME:-relay-backend}"
REPOSITORY="${REPOSITORY:-relay-backend}"
SECRET_NAME="${SECRET_NAME:-relay-metrics-token}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HISTORY_DIR="$ROOT/deploy-history"
mkdir -p "$HISTORY_DIR"

COMMIT_SHA="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo manual)"
REPORT_FILE="$HISTORY_DIR/deploy_$(date +%Y%m%d_%H%M%S)_${COMMIT_SHA}.md"
IMAGE="$REGION-docker.pkg.dev/$PROJECT_ID/$REPOSITORY/$REPOSITORY:$COMMIT_SHA"

echo "== relay Cloud Run deployment =="
echo "Project: $PROJECT_ID | Region: $REGION | Service: $SERVICE_NAME"
START=$(date +%s)
declare -a STEPS=()

step() {
  local name="$1"; shift
  echo ""
  echo "==> $name..."
  local s=$(date +%s)
  "$@"
  local d=$(( $(date +%s) - s ))
  STEPS+=("| $name | ${d}s |")
  echo "    OK (${d}s)"
}

step "Pre-flight (gcloud + git)" bash -c "
  command -v gcloud >/dev/null || { echo 'gcloud CLI not found' >&2; exit 1; }
  gcloud config set project '$PROJECT_ID' --quiet >/dev/null
"

step "Enable required Google Cloud APIs" gcloud services enable run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com --quiet

step "Ensure Artifact Registry repository" bash -c "
  gcloud artifacts repositories describe '$REPOSITORY' --location='$REGION' >/dev/null 2>&1 || \
    gcloud artifacts repositories create '$REPOSITORY' --repository-format=docker --location='$REGION' --quiet
"

step "Build and push image via Cloud Build" gcloud builds submit --tag "$IMAGE" "$ROOT" --quiet

if [ -n "$METRICS_TOKEN" ]; then
  step "Store METRICS_TOKEN in Secret Manager (out-of-band)" bash -c "
    gcloud secrets describe '$SECRET_NAME' >/dev/null 2>&1 || \
      gcloud secrets create '$SECRET_NAME' --replication-policy=automatic --quiet
    printf '%s' '$METRICS_TOKEN' | gcloud secrets versions add '$SECRET_NAME' --data-file=- --quiet
  "
fi

step "Deploy service to Cloud Run (free-tier instance settings)" gcloud run deploy "$SERVICE_NAME" \
  --image "$IMAGE" --region "$REGION" --allow-unauthenticated \
  --cpu 1 --memory 512Mi --min-instances 0 --max-instances 1 --port 8080 \
  --set-env-vars "PORT=8080,TRANSFER_TTL=15m,MAX_FILE_SIZE=10737418240,MAX_CHUNK_SIZE=2097152,MAX_ACTIVE_TRANSFERS=1000,MAX_CONNECTIONS=2000,CREATE_RATE_PER_MINUTE=20,TRUST_PROXY=true,WRITE_TIMEOUT=5m,METRICS_FORMAT=prometheus" \
  --quiet

if [ -n "$METRICS_TOKEN" ]; then
  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
  RUNTIME_SA="service-$PROJECT_NUMBER@serverless-robot-prod.iam.gserviceaccount.com"
  step "Grant runtime service account secret access" gcloud secrets add-iam-policy-binding "$SECRET_NAME" \
    --member="serviceAccount:$RUNTIME_SA" --role="roles/secretmanager.secretAccessor" --quiet
fi

SERVICE_URL="$(gcloud run services describe "$SERVICE_NAME" --region "$REGION" --format='value(status.url)')"

if [ -n "$METRICS_TOKEN" ]; then
  step "Bind secret and configure public URLs" gcloud run services update "$SERVICE_NAME" --region "$REGION" \
    --set-secrets "METRICS_TOKEN=${SECRET_NAME}:latest" \
    --set-env-vars "PUBLIC_BASE_URL=$SERVICE_URL,ALLOWED_ORIGINS=$SERVICE_URL" --quiet
else
  step "Configure public URLs" gcloud run services update "$SERVICE_NAME" --region "$REGION" \
    --set-env-vars "PUBLIC_BASE_URL=$SERVICE_URL,ALLOWED_ORIGINS=$SERVICE_URL" --quiet
fi

step "Verify health and metrics" bash -c "
  ok=0
  for i in \$(seq 1 12); do
    if curl -fsS -o /dev/null \"$SERVICE_URL/healthz\"; then ok=1; break; fi
    sleep 5
  done
  [ \"\$ok\" = 1 ] || { echo '/healthz did not return 204' >&2; exit 1; }
  if [ -n \"$METRICS_TOKEN\" ]; then
    curl -fsS -H \"Authorization: Bearer $METRICS_TOKEN\" \"$SERVICE_URL/metrics\" | grep -q relay_active_transfers || exit 1
  fi
"

ELAPSED=$(( $(date +%s) - START ))
{
  echo "# relay Deployment Benchmark Report"
  echo "- **Commit SHA:** \`$COMMIT_SHA\`"
  echo "- **Project:** \`$PROJECT_ID\` | **Region:** \`$REGION\`"
  echo "- **Service URL:** $SERVICE_URL"
  echo "- **Container Image:** \`$IMAGE\`"
  echo "- **Total Duration:** ${ELAPSED}s"
  echo ""
  echo "## Steps"
  echo ""
  echo "| Step | Duration |"
  echo "|---|---|"
  for s in "${STEPS[@]}"; do echo "$s"; done
  echo ""
  echo "## Verification"
  echo "- \`/healthz\` returned HTTP 204."
  if [ -n "$METRICS_TOKEN" ]; then
    echo "- \`/metrics\` returned Prometheus text with the injected token."
  else
    echo "- Metrics disabled (no METRICS_TOKEN)."
  fi
} > "$REPORT_FILE"

echo ""
echo "Deployment complete!"
echo "Hosted URL: $SERVICE_URL"
echo "Report:     $REPORT_FILE"
