# syntax=docker/dockerfile:1.4

# Pinned by digest, not just by tag: `golang:1.27.3-alpine` is republished whenever
# its Alpine base is rebuilt, so the tag alone does not describe what was built. The
# tag is kept alongside for readability - update both together, with
# `docker buildx imagetools inspect golang:<tag> --format '{{.Manifest.Digest}}'`.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder

WORKDIR /code

ENV CGO_ENABLED=0
ENV GOPATH=/go
ENV GOCACHE=/go-build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod/cache \
    go mod download

COPY . .

# The builder stage runs on the build host (BUILDPLATFORM) for speed, so the target
# has to come from the ARGs buildx sets - otherwise a cross-build ships the builder's
# architecture inside a target-arch image and dies with "exec format error".
# Build the package, not one file: a second file in package main would be dropped.
#
# -trimpath strips /code and the module cache path from the binary. Without it every
# panic dump and every stack trace in a log carries the build machine's directory
# layout, and two builds of the same commit from different checkouts differ.
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod/cache \
    --mount=type=cache,target=/go-build \
    GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -o bin/backend ./cmd/server

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS certs
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 65532 -H -s /sbin/nologin nonroot \
    && mkdir -p /data/ssh \
    && chown -R 65532:65532 /data

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=certs /etc/passwd /etc/passwd
# Ship /data/ssh owned by the non-root user so a fresh named volume inherits
# that ownership; otherwise the mount is root-owned and keygen cannot write it.
COPY --from=certs --chown=65532:65532 /data /data
COPY --from=builder /code/bin/backend /usr/local/bin/backend
USER nonroot
CMD ["/usr/local/bin/backend"]
