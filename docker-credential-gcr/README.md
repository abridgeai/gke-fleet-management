# docker-credential-gcr

Minimal image carrying the standalone [`docker-credential-gcr`](https://github.com/GoogleCloudPlatform/docker-credential-gcr)
binary. Used as an `initContainer` by `argocd-repo-server` to authenticate OCI
Helm chart pulls from Google Artifact Registry via Workload Identity — no static
credentials. The helper talks to the GKE metadata server directly, so it needs
no gcloud at runtime.

## Build

Manual, matching the other plugins in this repo:

```bash
gcloud builds submit --region=us-central1 --config=cloudbuild.yaml \
  --project=abridge-artifact-registry
```

Publishes `us-central1-docker.pkg.dev/abridge-artifact-registry/infrastructure/docker-credential-gcr:{2.1.22,latest}`.

Rebuild only to bump `GCR_VERSION` (Dockerfile arg + the tags in `cloudbuild.yaml`).

## Consumer

`argocd-repo-server` mounts this binary onto a shared volume and points
`DOCKER_CONFIG` at a `config.json` with
`{"credHelpers":{"us-docker.pkg.dev":"gcr"}}`. See the repo-server patch in
`infrastructure` (configsync argocd overlay).
