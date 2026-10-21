# Pluto for Channels

A small Go program that signs in to [Pluto TV](https://pluto.tv) and serves M3U playlists and an XMLTV guide over HTTP. [Channels DVR](https://getchannels.com) can add those URLs as custom channel sources.

It keeps the playlist URLs from [maddox/pluto-for-channels](https://github.com/maddox/pluto-for-channels): `/tuner-N-playlist.m3u` and `/epg.xml`. That project was archived on 2 July 2026 and is read-only. Its README recommends [FastChannels](https://github.com/kineticman/FastChannels) for a new install that should use one Pluto login and one Channels source, and for other FAST services. This program is the per-tuner bridge: up to 12 independent Pluto sessions, one playlist URL each.

The module is stdlib-only Go 1.22 (`github.com/mbc3k/pluto`). A failed guide refresh keeps the last good snapshot. A failed sign-in does not exit the process.

## Quick start

```sh
go build -o pluto ./cmd/pluto

PLUTO_EMAIL=you@example.com \
PLUTO_PASSWORD=secret \
DEVICE_ID_FILE=/tmp/pluto-devices.conf \
TUNER_COUNT=1 \
BIND_ADDR=127.0.0.1 \
PORT=8080 \
./pluto
```

`DEVICE_ID_FILE` has to be writable. The default path, `/opt/local/etc/pluto-devices.conf`, is the SmartOS location and does not exist on a laptop.

Then:

```sh
curl -sS -D- http://127.0.0.1:8080/health
curl -sS http://127.0.0.1:8080/status
curl -sS http://127.0.0.1:8080/metrics
```

`/health` stays `503` until the first sign-in attempt has finished and a guide is cached. Open `http://127.0.0.1:8080/` for the playlist and EPG URLs to paste into Channels.

Stop the process with SIGINT or SIGTERM. It cancels in-flight sign-in and guide fetches, then drains HTTP for up to 30 seconds.

## Configuration

Settings come from the environment. A `KEY=value` file fills in anything the environment does not set. An empty environment variable counts as unset, so a blank `export` does not hide the file.

Search order when `PLUTO_CONFIG` is unset: `/opt/local/etc/pluto.conf`, then `/usr/local/etc/pluto.conf`, then `/etc/pluto.conf`. The first regular file wins. `PLUTO_CONFIG=/path` reads that file and fails if it is missing. Lines may use one pair of quotes, an optional `export ` prefix, and `#` comments. Double quotes follow Go string escapes. The same keys work as `svccfg setenv`, a systemd `EnvironmentFile`, or container `-e` flags.

`PLUTO_EMAIL` is trimmed. `PLUTO_PASSWORD` is kept as written, including surrounding spaces.

| Variable | Default | Allowed |
|---|---|---|
| `PLUTO_EMAIL` | required | Pluto TV account email |
| `PLUTO_PASSWORD` | required | Pluto TV account password |
| `PORT` | `8080` | Integer 1–65535 |
| `BIND_ADDR` | empty | Host or IP, no port. Empty listens on all interfaces (`:PORT`). `127.0.0.1` is loopback. `::1` and `[::1]` both listen on `[::1]:PORT`. A value that already contains a port is rejected. |
| `ACCESS_TOKEN` | empty | Empty leaves playlist and EPG URLs open. Otherwise 1–128 characters, with no whitespace and no `/ ? # & %`. See [Access](#access). |
| `START_CHANNEL` | `10000` | Integer ≥ 0. First `tvg-chno` in each playlist. |
| `TUNER_COUNT` | `12` | Integer 1–12. One Pluto session and one playlist URL per tuner. |
| `REFRESH_EVERY` | `3h` | Go duration, minimum `1m` (`90m`, `3h`). No configured maximum. |
| `GUIDE_HOURS` | `24` | Integer 6–72. Rounded up to a multiple of 6. `25` becomes `30`. `70` becomes `72`. Below 6 or above 72 is an error. The process logs the rounding. |
| `DEVICE_ID_FILE` | `/opt/local/etc/pluto-devices.conf` | One UUID per tuner. The parent directory is created. Rewritten atomically, mode `0600`. |
| `PLUTO_CONFIG` | the search path above | Optional path to the key file. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` (or `warning`), `error`. Anything else stays at `info` and the unrecognized value is logged. |

Example `/etc/pluto.conf`:

```
PLUTO_EMAIL=you@example.com
PLUTO_PASSWORD=secret
BIND_ADDR=127.0.0.1
PORT=8080
TUNER_COUNT=2
REFRESH_EVERY=3h
GUIDE_HOURS=24
DEVICE_ID_FILE=/var/lib/pluto/devices.conf
LOG_LEVEL=info
# ACCESS_TOKEN=choose-a-long-secret
```

There is no Docker Compose file in this repo. Pass the same variables with `-e` or `EnvironmentFile`.

The guide is fetched as consecutive 6-hour Pluto windows, at most four at a time. One failed window fails that refresh and the previous cache stays. On startup a failed fetch is retried after 1 minute, 5 minutes, and 30 minutes, then on `REFRESH_EVERY`.

## Access

Leave `ACCESS_TOKEN` unset and existing Channels URLs keep working. Set it when the port is reachable past your own network. Channels cannot send an `Authorization` header, so the secret is in the URL. Either form is enough. A valid `/t/<token>/` prefix is stripped once.

- `http://host:8080/tuner-1-playlist.m3u?token=YOUR_SECRET`
- `http://host:8080/epg.xml?token=YOUR_SECRET`
- `http://host:8080/t/YOUR_SECRET/tuner-1-playlist.m3u`
- `http://host:8080/t/YOUR_SECRET/epg.xml`

These paths require the token when it is set: `/tuner-N-playlist.m3u`, `/tunerN/channels.m3u`, `/tunerN/m3u`, `/epg.xml`, and `/xmltv.xml`. A wrong or missing token is `401` with body `unauthorized`. A `/t/` prefix that does not match is `401` even for a path that would otherwise be open, including `/health`.

`/health`, `/metrics`, `/`, `/status`, and `/style.css` do not require the token. `/` and `/status` embed it in the playlist and EPG URLs they display. Pair the token with `BIND_ADDR` so those pages are not reachable off-box. The process logs whether the gate is on, not the token. A rejected token is not copied into the error text. Every response sets `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.

`X-Forwarded-Proto` (`http` or `https`) and `X-Forwarded-Host` change the origin shown on `/` and `/status`. Only the first comma-separated value is used.

## HTTP

Tuner numbers are 1 through `TUNER_COUNT`. Only those playlist routes are registered.

| Path | Response |
|---|---|
| `/tuner-N-playlist.m3u` | Primary M3U. Also `/tunerN/channels.m3u` and `/tunerN/m3u`. |
| `/epg.xml`, `/xmltv.xml` | Shared XMLTV guide. |
| `/` | HTML status page with playlist and EPG URLs, and the Channels setup steps. Any other unmatched path is `404`. |
| `/style.css` | Stylesheet for that page. `Cache-Control: public, max-age=300`. |
| `/health` | Readiness probe. `Cache-Control: no-store`. |
| `/status` | JSON snapshot. `Cache-Control: no-store`. The token value is not a field. |
| `/metrics` | Prometheus text `0.0.4`. `Content-Type: text/plain; version=0.0.4; charset=utf-8`. `Cache-Control: no-store`. |
| `/t/<token>/...` | The paths above, after one prefix strip, when `ACCESS_TOKEN` is set. |

Playlist and guide bodies:

| Condition | Status | Body |
|---|---|---|
| `ACCESS_TOKEN` set and the token does not match | `401` | `unauthorized` |
| First sign-in still running (playlists only; the guide does not wait) | `503` | `tuners still authenticating` |
| Sign-in finished, guide not cached yet | `503` | `playlist not yet available` or `EPG not yet available` |
| Guide cached | `200` | M3U `Content-Type: application/x-mpegurl`, or XML `Content-Type: application/xml; charset=utf-8` |

`/health`:

| Condition | Status | Body |
|---|---|---|
| First sign-in still running | `503` | `authenticating` |
| Sign-in attempt finished, guide not cached | `503` | `not ready` |
| Guide cached | `200` | `ok` |

Error bodies from `http.Error` are `text/plain; charset=utf-8` and end with a newline. The ready `/health` body is the two characters `ok` with no newline, `Content-Type: text/plain; charset=utf-8`, and `Content-Length: 2`. While playlists are still authenticating, `/health` is `authenticating` even if you only wanted the guide.

`/` while sign-in is running says `Still signing in to Pluto TV. Playlists will start working when sign-in finishes.` After that, until the guide is cached, it says `Still loading the channel guide. Playlist and EPG links will start working after the first successful refresh.`

`/status` is indented JSON. `refresh_every` is Go's duration text (`3h0m0s` for the default). `last_updated` is empty until the first successful refresh, then UTC `2006-01-02 15:04:05Z`. `access_token` is `true` or `false`. `tuners` and `epg_url` are absolute and include `?token=` when the gate is on. Other fields: `version`, `ready`, `auth_ready`, `tuner_count`, `channels`, `start_channel`, `guide_hours`, `bind_addr`.

Playlists and the guide are gzip-encoded when `Accept-Encoding` lists `gzip` with a quality above 0. The response sets `Content-Encoding: gzip` and `Vary: Accept-Encoding`, and omits `Content-Length`. Uncompressed bodies set `Content-Length`. `Cache-Control` is `no-cache`, and `Last-Modified` is set once the guide has a timestamp. Errors, `/health`, `/status`, `/metrics`, `/`, and `/style.css` are not gzipped.

`/metrics` always emits `pluto_ready`, `pluto_auth_ready`, `pluto_channels`, `pluto_tuners`, `pluto_guide_hours`, `pluto_last_refresh_timestamp_seconds`, `pluto_refresh_total` (`ok` and `error`), and `pluto_http_responses_total` for playlist and EPG codes `200`, `503`, and `other`, plus auth `401` and `other`.

## Channels DVR

Use one custom channel source per tuner you want to stream at the same time. Each source is one Pluto session. The status page shows the same steps.

1. In Channels DVR, add a custom channels source for each tuner.
2. Playlist URL: `http://<host>:8080/tuner-1-playlist.m3u` (then tuner 2, tuner 3, and so on). With `ACCESS_TOKEN` set, use one of the URL forms in [Access](#access). Copy them from `http://<host>:8080/` so the token is included.
3. Set that source's stream limit to 1 so Channels spreads concurrent streams across tuners.
4. Point every source at the same guide URL, `http://<host>:8080/epg.xml` (or `/xmltv.xml`). Channel numbers start at `START_CHANNEL` and match across tuners.

Channels refreshes the guide on its own schedule. This process rebuilds it every `REFRESH_EVERY` (default 3 hours) and serves the cached document in between.

## Deployment

### Binary

`make build-local` writes `./pluto` for the machine you are on. `make build` cross-compiles an illumos/amd64 binary named `pluto`. `make build-linux` writes `pluto-linux-amd64`. The version string passed to the status page is `git describe --tags --always --dirty`, or `dev`.

### Container

`Containerfile` is a multi-stage build (Go 1.22, then `scratch`). There is no `Dockerfile` and no Compose file. The image sets `DEVICE_ID_FILE=/data/pluto-devices.conf`, `PORT=8080`, `START_CHANNEL=10000`, and `TUNER_COUNT=12`. Mount a volume on `/data` or Pluto TV sees a new device on every start.

```sh
# Apple container
container build -t pluto .
container run --rm -e PLUTO_EMAIL=you@example.com -e PLUTO_PASSWORD=secret \
  -p 8080:8080 -v pluto-data:/data pluto

# Docker or Podman. The recipe is Containerfile, not Dockerfile.
docker build -f Containerfile -t pluto .
docker run -d --name pluto --restart unless-stopped \
  -e PLUTO_EMAIL=you@example.com -e PLUTO_PASSWORD=secret \
  -p 8080:8080 -v pluto-data:/data pluto
```

`make container` runs the Apple `container build` with a `VERSION` build arg.

### systemd

`make install-systemd` installs the Linux binary at `/usr/local/bin/pluto`, creates `/var/lib/pluto`, and installs `systemd/pluto.service`. The unit reads `/etc/pluto.conf` when that file exists (`EnvironmentFile=-/etc/pluto.conf`) and sets `DEVICE_ID_FILE=/var/lib/pluto/devices.conf`. Put `PLUTO_EMAIL` and `PLUTO_PASSWORD` in the conf file (mode `0600`) or in a drop-in:

```
sudo systemctl edit pluto
```

```
[Service]
Environment=PLUTO_EMAIL=you@example.com
Environment=PLUTO_PASSWORD=secret
```

Then `sudo systemctl enable --now pluto`. The unit restarts on failure after 5 seconds and waits for `network-online.target`.

### launchd (macOS)

`make install-launchd` installs `/usr/local/bin/pluto` and copies `launchd/com.mbc3k.pluto.plist` into `~/Library/LaunchAgents/`. The plist sets `DEVICE_ID_FILE=/usr/local/var/pluto/devices.conf` and logs to `/usr/local/var/log/pluto.log`. Put the email and password in the plist or in `/usr/local/etc/pluto.conf`, then:

```sh
launchctl load ~/Library/LaunchAgents/com.mbc3k.pluto.plist
```

### SmartOS

`make build` produces the illumos binary. The SMF manifest is `smf/pluto.xml` and the start method is `smf/method` (it backgrounds `/opt/local/bin/pluto`). Install both. The method script is required; the manifest does not exec the binary directly. The manifest is world-readable, so credentials go in SMF properties, not in the XML. The stop method is SIGTERM with a 35-second timeout.

```sh
install -m 0755 pluto /opt/local/bin/pluto
install -m 0555 smf/method /opt/local/lib/svc/method/pluto
install -m 0444 smf/pluto.xml /opt/local/lib/svc/manifest/network/pluto.xml
svccfg import /opt/local/lib/svc/manifest/network/pluto.xml
svccfg -s svc:/network/pluto:default setenv PLUTO_EMAIL you@example.com
svccfg -s svc:/network/pluto:default setenv PLUTO_PASSWORD secret
svcadm enable svc:/network/pluto:default
```

`svcs svc:/network/pluto:default` shows online, offline, or maintenance. `svcs -L svc:/network/pluto:default` prints the log path. To replace the binary: `svcadm disable -s svc:/network/pluto:default`, install the new file, then `svcadm enable`.

## Security

- Set `BIND_ADDR` to loopback or the DVR's LAN address when the host has another route in.
- Treat `ACCESS_TOKEN` like a password. It will appear in Channels' source URL, in `/` and `/status`, and in proxy access logs. It is not written to this process's log.
- Keep `pluto.conf` mode `0600`. `PLUTO_PASSWORD` and boot errors are redacted in logs.
- Device IDs are not secret account passwords, but the file is mode `0600` and should stay on the volume you back up. Deleting it makes Pluto TV treat the tuners as new devices.
- The listener is plain HTTP. Terminate TLS in front of it if the link is not trusted, and send `X-Forwarded-Proto: https` if the status page should show `https` URLs.
- `/metrics` has no token. Do not publish it on an open port. It does not contain the secret or account password.

## Troubleshooting

| What you see | What it means |
|---|---|
| Process exits immediately | Configuration failed before listen. The log says which variable (`PLUTO_EMAIL is required`, `TUNER_COUNT`, `PORT`, `BIND_ADDR`, `ACCESS_TOKEN`, `REFRESH_EVERY`, `GUIDE_HOURS`). `PLUTO_CONFIG` pointing at a missing file is also fatal. |
| `/health` is `503 authenticating` | The first Pluto sign-in is still running. Playlists return `503 tuners still authenticating` and do not start a second sign-in. The guide can still be loading in parallel. |
| `/health` is `503 not ready`, playlists say `playlist not yet available` | The sign-in attempt finished (it may have failed) and no guide is cached yet. Startup retries run at 1 minute, 5 minutes, and 30 minutes. A failed sign-in is retried on the next playlist request. The process keeps running. |
| `401 unauthorized` | The gate is on and this URL has no matching token. Copy the URL from `/`. A wrong `/t/...` prefix returns 401 for `/health` too. |
| Guide works, streams fail later | Stream URLs carry a Pluto JWT. It is refreshed when it is within 1 hour of expiry or when it was issued more than 2 hours ago. Handing out a playlist does not extend that age. A stitcher `401` is not retried by the HTTP client. The next playlist request signs in again. |
| New device on every restart | `DEVICE_ID_FILE` is not on a persistent volume. In the container image that path is `/data/pluto-devices.conf`. |
| `GUIDE_HOURS` in `/status` is higher than you set | It was rounded up to a multiple of 6, and capped at 72. The log line is `GUIDE_HOURS rounded up to a multiple of 6`. |
| Channels shows the playlist but no programs | The EPG URL is missing, points at a different host, or was fetched while `/epg.xml` was still `503`. |
| `gzip` clients and plain clients disagree | Only playlist and guide `200`s are gzipped, and only when `gzip` has `q` greater than 0. Error pages stay plain text. |

`LOG_LEVEL=debug` logs retry delays and JWT parse details. Account credentials are redacted from those URLs.

## Building

Go 1.22 or newer.

```sh
make build-local   # this OS
make build         # SmartOS / illumos amd64
make build-linux   # linux/amd64
make test          # go test ./...
make vet           # go vet ./...
make fmt           # gofmt -w .
```
