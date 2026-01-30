# Next Steps Checklist

## ✅ Already Done

- [x] Cloned Google's repository to `/Users/prashanth/Documents/claude-workspace/gke-fleet-management`
- [x] Created protection logic files:
  - `fleet-argocd-plugin/protection/detector.go` ✅
  - `fleet-argocd-plugin/protection/cache.go` ✅
- [x] Created documentation:
  - `ABRIDGE_README.md` ✅
  - `FORK_SETUP.md` ✅
  - `fleet-argocd-plugin/PROTECTION_PATCH.md` ✅

## 🔲 TODO: Create GitHub Fork

```bash
# 1. Go to GitHub and create fork
open https://github.com/GoogleCloudPlatform/gke-fleet-management

# 2. Click "Fork" → Select "abridgeai" organization

# 3. Update git remotes
cd /Users/prashanth/Documents/claude-workspace/gke-fleet-management
git remote rename origin upstream
git remote add origin git@github.com:abridgeai/gke-fleet-management.git
git fetch origin

# 4. Create feature branch
git checkout -b abridge/fleet-api-protection
```

## 🔲 TODO: Apply Code Changes

```bash
cd /Users/prashanth/Documents/claude-workspace/gke-fleet-management/fleet-argocd-plugin

# Follow the instructions in PROTECTION_PATCH.md to modify:
# 1. fleetclient/fleetclient.go
# 2. main.go

# The patch file has detailed line-by-line instructions
cat PROTECTION_PATCH.md
```

## 🔲 TODO: Test Locally

```bash
cd /Users/prashanth/Documents/claude-workspace/gke-fleet-management/fleet-argocd-plugin

# 1. Build
go mod tidy
go build -o fleet-sync .

# 2. Run basic test
./fleet-sync
# (Will fail without proper env vars, but should compile successfully)
```

## 🔲 TODO: Build Docker Image

```bash
cd /Users/prashanth/Documents/claude-workspace/gke-fleet-management/fleet-argocd-plugin

# Build
docker build -t us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1 .

# Push
docker push us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1
```

## 🔲 TODO: Update Infrastructure Repo

```bash
cd /Users/prashanth/Documents/claude-workspace/infrastructure

# Edit: configsync/base/argocd/fleet-sync/install.yaml
# Line 75: Change image to v1.0.0-abridge.1
# Add environment variables (see ABRIDGE_README.md for details)
```

## 🔲 TODO: Deploy to Staging

```bash
# Apply changes
kubectl apply -f /Users/prashanth/Documents/claude-workspace/infrastructure/configsync/base/argocd/fleet-sync/install.yaml \
  --context stg-fleet-us-central1-ml-triton-cluster

# Watch deployment
kubectl rollout status deployment/argocd-fleet-sync -n argocd --context stg-fleet-us-central1-ml-triton-cluster

# Check logs
kubectl logs -n argocd -l app.kubernetes.io/name=argocd-fleet-sync -f --context stg-fleet-us-central1-ml-triton-cluster
```

## 🔲 TODO: Monitor for 48 Hours

```bash
# Watch for protection events
kubectl logs -n argocd -l app.kubernetes.io/name=argocd-fleet-sync -f --context stg-fleet-us-central1-ml-triton-cluster | grep -E "✅|⚠️|🔥|🔄|📦"

# Run monitoring script
/Users/prashanth/Documents/claude-workspace/infrastructure/scripts/monitor-fleet-sync.sh stg-fleet-us-central1-ml-triton-cluster
```

## 🔲 TODO: Deploy to Production

After 48 hours of stable staging operation:

```bash
# Build production image
docker build -t us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1-prod .
docker push us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1-prod

# Update production fleet-sync
kubectl apply -f configsync/production/argocd/fleet-sync/install.yaml --context prod-fleet-us-central1-ml-triton-cluster
```

## 🔲 TODO: Submit PR to Google

After successful production deployment:

```bash
cd /Users/prashanth/Documents/claude-workspace/gke-fleet-management

# Push feature branch
git push origin abridge/fleet-api-protection

# Create PR at GitHub:
# https://github.com/GoogleCloudPlatform/gke-fleet-management/compare/main...abridgeai:gke-fleet-management:abridge/fleet-api-protection

# Include in PR:
# - Link to /Users/prashanth/Documents/claude-workspace/infrastructure/docs/incident-evidence.md
# - GCP audit log evidence
# - Production test results
```

## Quick Reference

### Key Files
- **Protection code**: `/Users/prashanth/Documents/claude-workspace/gke-fleet-management/fleet-argocd-plugin/protection/`
- **Patch instructions**: `/Users/prashanth/Documents/claude-workspace/gke-fleet-management/fleet-argocd-plugin/PROTECTION_PATCH.md`
- **Setup guide**: `/Users/prashanth/Documents/claude-workspace/gke-fleet-management/ABRIDGE_README.md`
- **Evidence**: `/Users/prashanth/Documents/claude-workspace/infrastructure/docs/incident-evidence.md`
- **Monitoring**: `/Users/prashanth/Documents/claude-workspace/infrastructure/scripts/monitor-fleet-sync.sh`

### Helpful Commands

```bash
# Check current Fleet API status
kubectl logs -n argocd -l app.kubernetes.io/name=argocd-fleet-sync --tail=100 --context stg-fleet-us-central1-ml-triton-cluster | grep "NameShort" | wc -l

# Run pre-maintenance check
/Users/prashanth/Documents/claude-workspace/infrastructure/scripts/pre-maintenance-check.sh stg-fleet-us-central1-ml-triton-cluster

# View GitHub issue template
cat /tmp/google-fleet-plugin-github-issue-revised.md
```

## Estimated Timeline

- Fork setup: 15 minutes
- Code changes: 30 minutes
- Build and test: 15 minutes
- Deploy to staging: 15 minutes
- Monitor staging: 48 hours
- Deploy to production: 15 minutes
- Submit PR to Google: 30 minutes

**Total active time**: ~2 hours
**Total calendar time**: ~3 days (including monitoring)

## Need Help?

Read the detailed guides:
1. `FORK_SETUP.md` - Fork and git setup
2. `PROTECTION_PATCH.md` - Code modification instructions
3. `ABRIDGE_README.md` - Complete usage guide
4. `docs/incident-evidence.md` - Incident analysis

## Success Criteria

✅ Protection code compiles without errors
✅ Docker image builds successfully
✅ Pod starts and stays healthy
✅ Logs show "Protection config: ..." on startup
✅ No application deletions during next Redis restart
✅ Protection activates during simulated transient issue
