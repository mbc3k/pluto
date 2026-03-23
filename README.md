# pluto

A bridge between [Pluto TV](https://pluto.tv) and [Channels DVR](https://getchannels.com). Serves authenticated M3U playlists and an XMLTV EPG over HTTP so Channels DVR can treat Pluto TV as a source of custom channels.

Inspired by [maddox/pluto-for-channels](https://github.com/maddox/pluto-for-channels). Rewritten in Go. No Node, no Docker, no runtime dependencies — a single statically-compiled binary.

## How it works

- Authenticates up to 12 independent Pluto TV sessions (one per tuner), each with a stable UUID device ID persisted across restarts
- Fetches the full channel list and 24 hours of EPG data from the Pluto TV API every 3 hours
- Serves per-tuner M3U playlists at `/tunerN/channels.m3u` and a shared XMLTV EPG at `/epg.xml`
- Never goes dark: a failed refresh keeps the previous cache until the next successful one
- On startup, if the initial fetch fails it retries at 1 min → 5 min → 30 min before settling into the 3-hour cycle

## Configuration

All configuration is via environment variables (or a flat `KEY=value` file at `/opt/local/etc/pluto.conf` on SmartOS).

| Variable | Default | Description |
|---|---|---|
| `PLUTO_EMAIL` | **required** | Pluto TV account email |
| `PLUTO_PASSWORD` | **required** | Pluto TV account password |
| `PORT` | `8080` | HTTP listen port |
| `START_CHANNEL` | `10000` | First channel number assigned in the M3U |
| `TUNER_COUNT` | `12` | Number of independent tuner sessions (1–12) |
| `DEVICE_ID_FILE` | `/opt/local/etc/pluto-devices.conf` | Where stable per-tuner UUIDs are persisted |

## Endpoints

| Path | Description |
|---|---|
| `/tuner-N-playlist.m3u` | M3U playlist for tuner N (primary format) |
| `/tunerN/channels.m3u` | M3U playlist for tuner N (legacy alias) |
| `/tunerN/m3u` | M3U playlist for tuner N (legacy alias) |
| `/epg.xml` | XMLTV EPG (also `/xmltv.xml`) |
| `/health` | `200 ok` when cache is populated, `503` while warming up |
| `/status` | JSON: version, last refresh time, tuner URLs, EPG URL |
| `/` | Status page with links to all tuner playlists |

---

## Deployment

### Apple container framework

Requires [Apple's container](https://github.com/apple/container) CLI. The binary is statically compiled, so the image runs from `scratch` with no OS layer.

**Build the image:**

```sh
container build -t pluto .
```

**Run:**

```sh
container run --rm \
  -e PLUTO_EMAIL=you@example.com \
  -e PLUTO_PASSWORD=secret \
  -p 8080:8080 \
  -v pluto-data:/data \
  pluto
```

Replace `--rm` with `--detach` to deploy long-term.
The `-v pluto-data:/data` volume persists the device ID file across restarts. Without it, Pluto TV sees a new device on every run and may rate-limit you.

**Verify:**

```sh
curl http://localhost:8080/health
curl http://localhost:8080/status
```

**Add to Channels DVR:**

In Channels DVR → Settings → Sources → Add Source → M3U Playlist:

- **Playlist URL:** `http://<your-mac-ip>:8080/tuner-1-playlist.m3u` (repeat for tuner-2 through tuner-N if you want multiple concurrent streams)
- **EPG URL:** `http://<your-mac-ip>:8080/epg.xml`

---

### SmartOS / illumos (native, via SMF)

The intended production target. A single binary managed by the Service Management Facility — no container runtime, no process supervisor, no cron.

**Prerequisites:** A SmartOS zone with network access and `/opt/local/bin` on `PATH` (standard pkgsrc layout).

**Build on your Mac:**

```sh
make build
# produces ./pluto (ELF 64-bit, illumos/amd64)
```

**Copy to the zone:**

```sh
scp ./pluto root@<zone-ip>:/tmp/
scp smf/pluto.xml root@<zone-ip>:/tmp/
```

**On the SmartOS zone (as root):**

```sh
# Install the binary
install -m 0755 /tmp/pluto /opt/local/bin/pluto

# Install and import the SMF manifest
install -m 0444 /tmp/pluto.xml /opt/local/lib/svc/manifest/network/pluto.xml
svccfg import /opt/local/lib/svc/manifest/network/pluto.xml

# Set credentials (the manifest is world-readable; credentials are not stored in it)
svccfg -s svc:/network/pluto:default setenv PLUTO_EMAIL you@example.com
svccfg -s svc:/network/pluto:default setenv PLUTO_PASSWORD secret

# Enable the service
svcadm enable svc:/network/pluto:default
```

**Check status and logs:**

```sh
svcs svc:/network/pluto:default       # online / offline / maintenance
svcs -l svc:/network/pluto:default    # detailed status
svcs -L svc:/network/pluto:default    # view log file
```

**Update the binary:**

```sh
scp ./pluto root@<zone-ip>:/tmp/
svcadm disable -s svc:/network/pluto:default
install -m 0755 /tmp/pluto /opt/local/bin/pluto
svcadm enable svc:/network/pluto:default
```

SMF automatically restarts the service if it crashes, waits for the network before starting, and handles SIGTERM for graceful shutdown.

---

### Docker

If you must. Same image, same volume semantics — Docker just insists on being involved.

```sh
docker build -t pluto .

docker run -d --name pluto --restart unless-stopped \
  -e PLUTO_EMAIL=you@example.com \
  -e PLUTO_PASSWORD=secret \
  -p 8080:8080 \
  -v pluto-data:/data \
  pluto
```

```sh
docker logs -f pluto
```

---

## Building from source

Requires Go 1.22+.

```sh
# For local testing (macOS or Linux)
make build-local

# Cross-compile for SmartOS (illumos/amd64)
make build

# Build OCI container image
make container

# Lint and test
go vet ./...
go test ./...
```

When running the local binary on macOS, override `DEVICE_ID_FILE` since the default path (`/opt/local/etc/pluto-devices.conf`) doesn't exist outside SmartOS:

```sh
PLUTO_EMAIL=you@example.com \
PLUTO_PASSWORD=secret \
DEVICE_ID_FILE=/tmp/pluto-devices.conf \
TUNER_COUNT=1 \
./pluto
```
