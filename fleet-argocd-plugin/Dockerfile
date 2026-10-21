# Use the offical Go image to create a build artifact.
FROM golang:1.23.2 as builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /fleet-argocd-sync
# eMudhra CA advisory (SECRES-271): the distroless images have not been rebuilt with the fixed
# ca-certificates yet, so take the bundle from Debian (bookworm-security ships 20250419~deb12u2).
# Drop this stage and the COPY below once distroless publishes rebuilt images.
FROM debian:bookworm-slim AS certs
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

FROM gcr.io/distroless/base-debian11
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
WORKDIR /
COPY --from=builder /fleet-argocd-sync /fleet-argocd-sync
# Run the web service on container startup.
USER nonroot:nonroot
ENTRYPOINT ["/fleet-argocd-sync"]

