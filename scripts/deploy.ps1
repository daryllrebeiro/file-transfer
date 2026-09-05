# relay: one-command, env-driven Google Cloud Run deployment (free tier).
# Mirrors the support-master deployment pattern.
#
# Usage:
#   $env:GOOGLE_CLOUD_PROJECT = "my-project-id"
#   $env:GOOGLE_CLOUD_REGION  = "us-central1"
#   $env:METRICS_TOKEN        = "optional-secret"   # omit to disable /metrics
#   .\scripts\deploy.ps1

param(
    [string]$ProjectId = $env:GOOGLE_CLOUD_PROJECT,
    [string]$Region = $(if ($env:GOOGLE_CLOUD_REGION) { $env:GOOGLE_CLOUD_REGION } else { "us-central1" }),
    [string]$MetricsToken = $env:METRICS_TOKEN,
    [string]$ServiceName = "relay-backend",
    [string]$Repository = "relay-backend",
    [string]$SecretName = "relay-metrics-token"
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$historyDir = Join-Path $repoRoot "deploy-history"
if (-not (Test-Path $historyDir)) { New-Item -ItemType Directory -Path $historyDir | Out-Null }

$deployStart = Get-Date
$commitSha = git rev-parse --short HEAD 2>$null
if (-not $commitSha) { $commitSha = "manual" }
$reportFile = Join-Path $historyDir "deploy_$($deployStart.ToString('yyyyMMdd_HHmmss'))_${commitSha}.md"
$steps = [System.Collections.Generic.List[string]]::new()

function Step([string]$Name, [scriptblock]$Body) {
    Write-Host "`n==> $Name..." -ForegroundColor Cyan
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    & $Body
    $sw.Stop()
    $steps.Add("| $Name | $([Math]::Round($sw.Elapsed.TotalSeconds,1))s |")
    Write-Host "    OK ($([Math]::Round($sw.Elapsed.TotalSeconds,1))s)" -ForegroundColor Green
}

Write-Host "== relay Cloud Run deployment ==" -ForegroundColor Cyan
Write-Host "Project: $ProjectId | Region: $Region | Service: $ServiceName"

Step "Validate environment" {
    if (-not $ProjectId) { throw "GOOGLE_CLOUD_PROJECT is not set (or pass -ProjectId)." }
}

Step "Pre-flight (gcloud + git)" {
    if (-not (Get-Command gcloud -ErrorAction SilentlyContinue)) { throw "gcloud CLI not found." }
    gcloud config set project $ProjectId --quiet | Out-Null
    if (-not (gcloud config get-value account 2>$null)) { throw "No authenticated gcloud account. Run 'gcloud auth login'." }
    Write-Host "Authenticated: $(gcloud config get-value account)"
}

Step "Enable required Google Cloud APIs" {
    gcloud services enable run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com --quiet
    if ($LASTEXITCODE -ne 0) { throw "gcloud services enable failed." }
}

Step "Ensure Artifact Registry repository '$Repository'" {
    $exists = gcloud artifacts repositories describe $Repository --location=$Region 2>$null
    if (-not $exists) {
        gcloud artifacts repositories create $Repository --repository-format=docker --location=$Region --description="relay backend images" --quiet
    }
}

$image = "${Region}-docker.pkg.dev/${ProjectId}/${Repository}/${Repository}:${commitSha}"

Step "Build and push image via Cloud Build" {
    gcloud builds submit --tag $image $repoRoot --quiet
    if ($LASTEXITCODE -ne 0) { throw "Cloud Build failed." }
}

if ($MetricsToken) {
    Step "Store METRICS_TOKEN in Secret Manager (out-of-band)" {
        $exists = gcloud secrets describe $SecretName 2>$null
        if (-not $exists) {
            gcloud secrets create $SecretName --replication-policy=automatic --quiet
        }
        $MetricsToken | gcloud secrets versions add $SecretName --data-file=- --quiet | Out-Null
    }
}

Step "Deploy service to Cloud Run (free-tier instance settings)" {
    $envArgs = @(
        "PORT=8080",
        "TRANSFER_TTL=15m",
        "MAX_FILE_SIZE=10737418240",
        "MAX_CHUNK_SIZE=2097152",
        "MAX_ACTIVE_TRANSFERS=1000",
        "MAX_CONNECTIONS=2000",
        "CREATE_RATE_PER_MINUTE=20",
        "TRUST_PROXY=true",
        "WRITE_TIMEOUT=5m",
        "METRICS_FORMAT=prometheus"
    )
    gcloud run deploy $ServiceName `
        --image $image `
        --region $Region `
        --allow-unauthenticated `
        --cpu 1 --memory 512Mi `
        --min-instances 0 --max-instances 1 `
        --port 8080 `
        --set-env-vars ($envArgs -join ",") `
        --quiet
    if ($LASTEXITCODE -ne 0) { throw "Cloud Run deploy failed." }
}

Step "Grant runtime service account secret access" {
    if ($MetricsToken) {
        $projectNumber = gcloud projects describe $ProjectId --format="value(projectNumber)"
        $runtimeSa = "service-$projectNumber@serverless-robot-prod.iam.gserviceaccount.com"
        gcloud secrets add-iam-policy-binding $SecretName `
            --member="serviceAccount:$runtimeSa" --role="roles/secretmanager.secretAccessor" --quiet | Out-Null
    }
}

$serviceUrl = gcloud run services describe $ServiceName --region $Region --format="value(status.url)" 2>$null

if ($MetricsToken) {
    Step "Bind METRICS_TOKEN secret and configure public URLs" {
        gcloud run services update $ServiceName --region $Region `
            --set-secrets "METRICS_TOKEN=${SecretName}:latest" `
            --set-env-vars "PUBLIC_BASE_URL=$serviceUrl,ALLOWED_ORIGINS=$serviceUrl" `
            --quiet
    }
} else {
    Step "Configure public URLs" {
        gcloud run services update $ServiceName --region $Region `
            --set-env-vars "PUBLIC_BASE_URL=$serviceUrl,ALLOWED_ORIGINS=$serviceUrl" `
            --quiet
    }
}

Step "Verify health and metrics" {
    $healthOk = $false
    for ($attempt = 1; $attempt -le 12; $attempt++) {
        try {
            $response = Invoke-WebRequest -Uri "${serviceUrl}/healthz" -TimeoutSec 5 -ErrorAction SilentlyContinue
            if ($response.StatusCode -eq 204) { $healthOk = $true; break }
        } catch { }
        Start-Sleep -Seconds 5
    }
    if (-not $healthOk) { throw "/healthz did not return 204 after 12 attempts." }
    if ($MetricsToken) {
        $metrics = Invoke-WebRequest -Uri "${serviceUrl}/metrics" -Headers @{ Authorization = "Bearer $MetricsToken" } -TimeoutSec 10
        if ($metrics.StatusCode -ne 200 -or $metrics.Content -notmatch "relay_active_transfers") { throw "/metrics verification failed." }
    }
}

$elapsed = (Get-Date) - $deployStart
$report = @(
    "# relay Deployment Benchmark Report",
    "- **Commit SHA:** ``$commitSha``",
    "- **Project:** ``$ProjectId`` | **Region:** ``$Region``",
    "- **Service URL:** $serviceUrl",
    "- **Container Image:** ``$image``",
    "- **Total Duration:** $([Math]::Round($elapsed.TotalSeconds,1))s",
    "",
    "## Steps", "",
    "| Step | Duration |",
    "|---|---|",
    $steps,
    "",
    "## Verification",
    "- `/healthz` returned HTTP 204.",
    $(if ($MetricsToken) { "- `/metrics` returned Prometheus text with the injected token." } else { "- Metrics disabled (no METRICS_TOKEN)." })
)
$report | Out-File -FilePath $reportFile -Encoding utf8

Write-Host ""
Write-Host "Deployment complete!" -ForegroundColor Green
Write-Host "Hosted URL: $serviceUrl"
Write-Host "Report:     $reportFile"
Write-Host ""
