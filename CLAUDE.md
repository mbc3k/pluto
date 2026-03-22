# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go rewrite of [maddox/pluto-for-channels](https://github.com/maddox/pluto-for-channels), targeting deployment as a SmartOS zone (illumos kernel). It bridges Pluto TV's free streaming service with Channels DVR by serving authenticated M3U playlists and an XMLTV EPG over HTTP. Zero external dependencies — stdlib only.

## Commands

```sh
go vet ./...                        # lint
go test ./...                       # tests
make build-local                    # build for current OS
make build                          # cross-compile for SmartOS (illumos/amd64)
make container                      # build OCI image (Apple container / Docker / Podman)
```

Cross-compilation uses `CGO_ENABLED=0 GOOS=illumos GOARCH=amd64`. The resulting ELF binary links only against the base illumos `ld.so.1`, which is always present on SmartOS.

## Local testing (macOS)

`DEVICE_ID_FILE` defaults to a SmartOS path that doesn't exist on macOS — always override it. Use `TUNER_COUNT=1` for faster startup:

```sh
make build-local
PLUTO_EMAIL=you@example.com \
PLUTO_PASSWORD=secret \
DEVICE_ID_FILE=/tmp/pluto-devices.conf \
TUNER_COUNT=1 \
./pluto
```

Verify:
```sh
curl http://localhost:8080/health
curl http://localhost:8080/status
curl http://localhost:8080/tuner1/channels.m3u | head -20
```

## OCI container (Apple container framework / Docker / Podman)

The `Containerfile` at the repo root does a multi-stage build: compiles in `golang:1.22-alpine`, runs from `scratch`. Device IDs are stored in `/data/pluto-devices.conf` inside the container — mount a named volume so they survive restarts.

```sh
# Build
make container                      # uses `container build`
# or: docker build -t pluto .

# Run (Apple container framework)
container run --rm -it \
  -e PLUTO_EMAIL=you@example.com \
  -e PLUTO_PASSWORD=secret \
  -p 8080:8080 \
  -v pluto-data:/data \
  pluto

# Run (Docker)
docker run --rm -it \
  -e PLUTO_EMAIL=you@example.com \
  -e PLUTO_PASSWORD=secret \
  -p 8080:8080 \
  -v pluto-data:/data \
  pluto
```

## Architecture

```
cmd/pluto/main.go          Entry point: load config → init sessions → start HTTP + scheduler
internal/config/           Env var + flat key=value file loading (/opt/local/etc/pluto.conf)
internal/auth/session.go   One Session per tuner: boot API auth, stable UUID device IDs, JWT refresh
internal/pluto/            Pluto TV API types, retry HTTP client, FetchChannels()
internal/playlist/m3u.go   Stateless M3U generation per tuner (embeds device ID + JWT in stream URLs)
internal/epg/xmltv.go      Stateless XMLTV generation (shared across all tuners)
internal/cache/cache.go    RWMutex-protected in-memory store; never clears on refresh failure
internal/scheduler/        Background refresh loop (3h ticker, startup retry at 1m/5m/30m)
internal/server/server.go  HTTP server: /tunerN/channels.m3u, /epg.xml, /health, /status
smf/pluto.xml              SMF service manifest for SmartOS svcadm/svccfg management
```

## Key design points

**12 independent sessions**: Pluto TV limits one stream per session. Each tuner gets a unique UUID device ID (persisted to `/opt/local/etc/pluto-devices.conf` across restarts) and authenticates independently via `GET https://boot.pluto.tv/v4/start`.

**Stream URL construction**: The boot response includes `sessionToken` (JWT) and `stitcherParams` (a pre-built query string). Stream URLs are `{stitcherBase}/v2/stitch/hls/channel/{id}/master.m3u8?{stitcherParams}&jwt={token}&masterJWTPassthrough=true&includeExtendedEvents=true`.

**EPG fetch**: 4 calls to `https://api.pluto.tv/v2/channels` in 6-hour windows covering the current 24-hour period. Timelines are merged per channel. Channels without `isStitched: true` are excluded.

**Never go dark**: `cache.SetAll()` is only called on success. A failed refresh logs an error and serves the previous cache until the next retry.

**Goroutine topology**: main context → HTTP server goroutine + scheduler goroutine. SIGTERM cancels the context, stops the scheduler, then drains the HTTP server with a 30s timeout.

## SmartOS deployment

```sh
# Set credentials (not in the manifest — it's world-readable)
svccfg -s svc:/network/pluto:default setenv PLUTO_EMAIL user@example.com
svccfg -s svc:/network/pluto:default setenv PLUTO_PASSWORD secret
svcadm enable svc:/network/pluto:default
svcs -L svc:/network/pluto:default   # view logs
```

Config can also live in `/opt/local/etc/pluto.conf` as `KEY=value` pairs.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `PLUTO_EMAIL` | required | Pluto TV account email |
| `PLUTO_PASSWORD` | required | Pluto TV account password |
| `PORT` | 8080 | HTTP listen port |
| `START_CHANNEL` | 10000 | First channel number in M3U |
| `TUNER_COUNT` | 12 | Number of independent tuners (1–12) |
| `DEVICE_ID_FILE` | `/opt/local/etc/pluto-devices.conf` | Persistent device UUID storage |
