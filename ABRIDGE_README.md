# Abridge Fork - GKE Fleet ArgoCD Plugin with Protection Logic

This is Abridge's fork of Google's fleet-argocd-plugin with added protection against transient Fleet API issues.

## What We Changed

### Problem
During normal operations (Redis restarts, node maintenance), the GKE Fleet API occasionally returns incomplete responses. The plugin doesn't validate or retry, causing ArgoCD to delete applications.

**Incident (2026-01-30):**
- Fleet API returned 6 membership bindings instead of 12
- 3 applications deleted and recreated
- 2-12 minutes downtime per application

### Solution
Added three-layer protection:
1. **Oscillation Detection**: Detects when response count changes rapidly (transient issue pattern)
2. **Retry Logic**: Retries API calls with exponential backoff (3 attempts)
3. **Response Caching**: Uses cached response when retries fail (1 hour TTL)

## Quick Start

### 1. Fork Setup (One-time)

```bash
# If not already done, create fork on GitHub:
# https://github.com/GoogleCloudPlatform/gke-fleet-management
# Click "Fork" → Select "abridgeai" organization

# Update remotes
cd /Users/prashanth/Documents/claude-workspace/gke-fleet-management
git remote rename origin upstream
git remote add origin git@github.com:abridgeai/gke-fleet-management.git

# Create feature branch
git checkout -b abridge/fleet-api-protection
```

### 2. Apply Protection Logic

```bash
cd fleet-argocd-plugin

# Protection code is already created in:
# - protection/detector.go (oscillation detection)
# - protection/cache.go (response caching)
# - PROTECTION_PATCH.md (instructions for modifying existing files)

# Follow PROTECTION_PATCH.md to modify:
# - fleetclient/fleetclient.go
# - main.go
```

### 3. Build and Test

```bash
# Build locally
go mod tidy
go build -o fleet-sync .

# Test (create unit tests)
go test ./protection/...

# Build Docker image
docker build -t us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1 .

# Push to registry
docker push us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1
```

### 4. Deploy to Staging

```bash
# Update infrastructure repo
cd /Users/prashanth/Documents/claude-workspace/infrastructure
vi configsync/base/argocd/fleet-sync/install.yaml

# Change image line (around line 75):
# FROM:
#   image: us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:latest
# TO:
#   image: us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1

# Add environment variables (after envFrom section):
env:
  - name: MAX_API_RETRIES
    value: "3"
  - name: RETRY_BASE_DELAY_SECONDS
    value: "2"
  - name: CACHE_MAX_AGE_MINUTES
    value: "60"
  - name: DETECTION_WINDOW_MINUTES
    value: "10"
  - name: OSCILLATION_THRESHOLD
    value: "2"
  - name: DROP_THRESHOLD_PERCENT
    value: "30"

# Apply
kubectl apply -f configsync/base/argocd/fleet-sync/install.yaml --context stg-fleet-us-central1-ml-triton-cluster

# Watch logs
kubectl logs -n argocd -l app.kubernetes.io/name=argocd-fleet-sync -f | grep -E "✅|⚠️|🔥|🔄|📦"
```

### 5. Monitor for Protection Events

```bash
# Watch for transient issue detection
kubectl logs -n argocd -l app.kubernetes.io/name=argocd-fleet-sync -f | grep "TRANSIENT ISSUE"

# Expected output when protection activates:
# ⚠️  TRANSIENT ISSUE: Oscillation detected: 2 count changes in last 10 minutes...
# 🔄 Retrying Fleet API (attempt 2/3) in 4s
# 📦 Cache hit: returning 12 items (age: 5m)
# ✅ Fleet API success: 12 membership bindings
```

## Configuration Options

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| MAX_API_RETRIES | 3 | Number of retry attempts |
| RETRY_BASE_DELAY_SECONDS | 2 | Base delay for exponential backoff (2s, 4s, 8s) |
| CACHE_MAX_AGE_MINUTES | 60 | How long cached data is valid |
| DETECTION_WINDOW_MINUTES | 10 | Time window for oscillation detection |
| OSCILLATION_THRESHOLD | 2 | Number of count changes indicating issue |
| DROP_THRESHOLD_PERCENT | 30 | Percentage drop indicating issue |

## How It Works

### Normal Flow (No Issues)
```
Fleet API → Returns 12 items → ✅ Cache → Return to ArgoCD
```

### Transient Issue Flow
```
Fleet API → Returns 6 items → ⚠️ Detect oscillation → 🔄 Retry → Still 6 items → 📦 Use cache → Return 12 items to ArgoCD
```

### Example: Incident Prevention

**Without Protection** (What happened 2026-01-30):
```
16:30:51 - Fleet API returns 6 items
         → Plugin returns 6 items to ArgoCD
         → ArgoCD deletes 6 applications
         → Services down 2-12 minutes
```

**With Protection** (What would happen now):
```
16:30:51 - Fleet API returns 6 items
         → Detector: "Oscillation detected" (12→6)
         → Retry #1: Still 6 items
         → Retry #2: Still 6 items
         → Retry #3: Still 6 items
         → Cache: Return 12 items (age: 5 minutes)
         → ArgoCD receives 12 items
         → No applications deleted ✅
```

## Testing Protection Logic

### Simulate Transient Issue (Dev Only)

```bash
# Temporarily block Fleet API for 30 seconds
kubectl exec -n argocd $(kubectl get pod -n argocd -l app.kubernetes.io/name=argocd-fleet-sync -o name) -- \
  sh -c 'iptables -A OUTPUT -d gkehub.googleapis.com -j DROP; sleep 30; iptables -D OUTPUT -d gkehub.googleapis.com -j DROP'

# Watch logs - should see cache usage
kubectl logs -n argocd -l app.kubernetes.io/name=argocd-fleet-sync -f
```

## Maintenance

### Sync with Upstream

```bash
# Periodically pull upstream changes
git fetch upstream
git checkout main
git merge upstream/main
git push origin main

# Rebase feature branch
git checkout abridge/fleet-api-protection
git rebase main

# Rebuild and deploy
docker build -t ...fleet-argocd-plugin:v1.1.0-abridge.1 .
docker push ...fleet-argocd-plugin:v1.1.0-abridge.1
```

### Version Tags

```bash
# Tag format: v<upstream-version>-abridge.<patch-number>
git tag -a v1.0.0-abridge.1 -m "Initial protection logic

- Oscillation detection
- Response caching
- Retry with exponential backoff
- Configurable thresholds

Prevents incident from 2026-01-30"

git push origin v1.0.0-abridge.1
```

## Rollback Plan

If issues occur:

```bash
# Option 1: Rollback to previous version
kubectl set image deployment/argocd-fleet-sync \
  argocd-fleet-sync=us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:previous-version \
  -n argocd

# Option 2: Revert to Google's official image
kubectl set image deployment/argocd-fleet-sync \
  argocd-fleet-sync=us-docker.pkg.dev/gke-fleet-management/fleet-argocd-plugin:latest \
  -n argocd
```

## Submit to Google (Future)

Once tested in production, submit PR:

```bash
# Push branch
git push origin abridge/fleet-api-protection

# Create PR at:
# https://github.com/GoogleCloudPlatform/gke-fleet-management/compare/main...abridgeai:gke-fleet-management:abridge/fleet-api-protection

# Include in PR description:
# - Link to incident postmortem
# - GCP audit log evidence
# - Test results from production
```

## Files Changed

- ✅ `fleet-argocd-plugin/protection/detector.go` (NEW)
- ✅ `fleet-argocd-plugin/protection/cache.go` (NEW)
- ⚠️ `fleet-argocd-plugin/fleetclient/fleetclient.go` (MODIFIED - see PROTECTION_PATCH.md)
- ⚠️ `fleet-argocd-plugin/main.go` (MODIFIED - see PROTECTION_PATCH.md)
- 📄 `ABRIDGE_README.md` (NEW - this file)
- 📄 `FORK_SETUP.md` (NEW)
- 📄 `fleet-argocd-plugin/PROTECTION_PATCH.md` (NEW)

## Monitoring

### Prometheus Metrics (Future Enhancement)

```go
// TODO: Add metrics
var (
    fleetAPICallsTotal = prometheus.NewCounterVec(...)
    fleetAPICacheHitsTotal = prometheus.NewCounter(...)
    fleetAPITransientIssuesTotal = prometheus.NewCounter(...)
)
```

### Logs to Watch

```bash
# Protection activated
grep "TRANSIENT ISSUE" fleet-sync.log

# Cache usage
grep "Cache hit" fleet-sync.log

# Retry attempts
grep "Retrying Fleet API" fleet-sync.log
```

## Support

- **Internal docs**: https://your-docs/fleet-api-protection
- **Incident postmortem**: /Users/prashanth/Documents/claude-workspace/infrastructure/docs/incident-evidence.md
- **GitHub issue**: https://github.com/GoogleCloudPlatform/gke-fleet-management/issues/[TBD]
- **Owner**: Platform Team

## License

This fork maintains Google's original Apache 2.0 license.
Abridge-specific additions are also licensed under Apache 2.0.
