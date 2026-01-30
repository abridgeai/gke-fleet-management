# Abridge Fork Setup Guide

## Step 1: Create Fork on GitHub

```bash
# Go to: https://github.com/GoogleCloudPlatform/gke-fleet-management
# Click "Fork" → Select "abridgeai" organization

# Then update remote:
cd /Users/prashanth/Documents/claude-workspace/gke-fleet-management
git remote rename origin upstream
git remote add origin git@github.com:abridgeai/gke-fleet-management.git
git fetch origin
```

## Step 2: Create Feature Branch

```bash
git checkout -b abridge/fleet-api-protection
```

## Step 3: Apply Protection Changes

Files to create:
1. `fleet-argocd-plugin/protection/detector.go` - Oscillation detection
2. `fleet-argocd-plugin/protection/cache.go` - Response caching
3. Modify: `fleet-argocd-plugin/fleetclient/fleetclient.go` - Add protection logic
4. Modify: `fleet-argocd-plugin/main.go` - Add configuration

## Step 4: Build and Test

```bash
cd fleet-argocd-plugin

# Build Docker image
docker build -t us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1 .

# Push to registry
docker push us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/fleet-argocd-plugin:v1.0.0-abridge.1
```

## Step 5: Update Deployment

Update infrastructure repo:
`infrastructure/configsync/base/argocd/fleet-sync/install.yaml`

Change image to your fork's image.

## Step 6: Commit and Push

```bash
git add .
git commit -m "Add Fleet API protection logic

- Oscillation detection (alerts on rapid count changes)
- Response caching (1 hour TTL)
- Retry logic with exponential backoff
- Transient issue detection

Prevents application deletions from transient Fleet API issues.
Incident reference: 2026-01-30"

git push origin abridge/fleet-api-protection
```

## Step 7: Submit PR to Google (Optional)

After testing in production, submit PR to upstream:
https://github.com/GoogleCloudPlatform/gke-fleet-management/pulls
