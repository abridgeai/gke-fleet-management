# Use the offical Go image to create a build artifact.
FROM golang:1.26.10 as builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /fleet-argocd-sync
# eMudhra CA advisory (SECRES-271 / SEC-1762): Debian 13 distroless ships the fixed ca-certificates 20250419+deb13u1 (148 certs).
FROM gcr.io/distroless/base-debian13
WORKDIR /
COPY --from=builder /fleet-argocd-sync /fleet-argocd-sync
# Run the web service on container startup.
USER nonroot:nonroot
ENTRYPOINT ["/fleet-argocd-sync"]

