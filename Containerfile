# syntax=docker/dockerfile:1
# Multi-stage build: compile in a Go toolchain image, run from scratch.
# Compatible with Docker, Podman, Buildah, and Apple's container framework.
#
# Build:
#   container build -t pluto .
#
# Run (Apple container framework):
#   container run --rm -it \
#     -e PLUTO_EMAIL=you@example.com \
#     -e PLUTO_PASSWORD=secret \
#     -p 8080:8080 \
#     -v pluto-data:/data \
#     pluto
#
# Device IDs are persisted in /data/pluto-devices.conf inside the container.
# Mount a named volume (-v pluto-data:/data) so they survive restarts.

FROM golang:1.22-alpine AS builder
WORKDIR /src

# Copy module files first so layer is cached when only source changes.
COPY go.mod ./
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /pluto ./cmd/pluto

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /pluto /pluto

# /data is the writable directory for persistent state (device ID file).
VOLUME ["/data"]
EXPOSE 8080

# Override the SmartOS default path to something writable inside the container.
ENV DEVICE_ID_FILE=/data/pluto-devices.conf \
    PORT=8080 \
    START_CHANNEL=10000 \
    TUNER_COUNT=12

# PLUTO_EMAIL and PLUTO_PASSWORD must be provided at runtime.
ENTRYPOINT ["/pluto"]
