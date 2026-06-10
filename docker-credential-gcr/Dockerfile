# Minimal image carrying the standalone docker-credential-gcr binary so the
# argocd-repo-server initContainer can copy it onto a shared volume and use it
# as a Docker credential helper for Google Artifact Registry (Workload Identity,
# no static credentials). The binary talks to the GKE metadata server directly,
# so it needs no gcloud at runtime.
#
# Build (manual, matching the other plugins in this repo):
#   gcloud builds submit --region=us-central1 --config=cloudbuild.yaml \
#     --project=abridge-artifact-registry
ARG GCR_VERSION=2.1.22

FROM alpine:3.20 AS fetch
ARG GCR_VERSION
# Throwaway fetch stage; pinning alpine pkg versions just ages out of the repo.
# hadolint ignore=DL3018
RUN apk add --no-cache curl tar \
 && curl -fsSL -o /tmp/gcr.tgz \
    "https://github.com/GoogleCloudPlatform/docker-credential-gcr/releases/download/v${GCR_VERSION}/docker-credential-gcr_linux_amd64-${GCR_VERSION}.tar.gz" \
 && tar -xzf /tmp/gcr.tgz -C /usr/local/bin docker-credential-gcr \
 && chmod 0755 /usr/local/bin/docker-credential-gcr

# busybox final so the initContainer has sh + cp to stage the binary and write
# the credHelpers config onto the shared volume.
FROM busybox:1.36.0-uclibc
COPY --from=fetch /usr/local/bin/docker-credential-gcr /usr/local/bin/docker-credential-gcr
