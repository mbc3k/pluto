# Pluto for Channels

A small Go program that signs in to [Pluto TV](https://pluto.tv) and serves M3U playlists and an XMLTV guide over HTTP. [Channels DVR](https://getchannels.com) can add those URLs as custom channel sources.

It keeps the playlist URLs from [maddox/pluto-for-channels](https://github.com/maddox/pluto-for-channels): `/tuner-N-playlist.m3u` and `/epg.xml`. That project was archived on 2 July 2026 and is read-only. Its README recommends [FastChannels](https://github.com/kineticman/FastChannels) for a new install that should use one Pluto login and one Channels source, and for other FAST services. This program is the per-tuner bridge: up to 12 independent Pluto sessions, one playlist URL each.

The module is stdlib-only Go 1.22 (`github.com/mbc3k/pluto`). A failed guide refresh keeps the last good snapshot. A failed sign-in does not exit the process. With `TVE_SOURCES` set it also serves DirecTV TV Everywhere lineups. See [TV Everywhere](#tv-everywhere).

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
| `ACCESS_TOKEN` | empty | Empty leaves Pluto playlist, EPG, and TV Everywhere URLs open. Otherwise 1–128 characters, with no whitespace and no `/ ? # & %`. See [Access](#access). |
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
# TVE_SOURCES=fox,aenetworks
# DIRECTV_USERNAME=
# DIRECTV_PASSWORD=
```

There is no Docker Compose file in this repo. Pass the same variables with `-e` or `EnvironmentFile`.

The guide is fetched as consecutive 6-hour Pluto windows, at most four at a time. One failed window fails that refresh and the previous cache stays. On startup a failed fetch is retried after 1 minute, 5 minutes, and 30 minutes, then on `REFRESH_EVERY`.

## Access

Leave `ACCESS_TOKEN` unset and existing Channels URLs keep working. Set it when the port is reachable past your own network. Channels cannot send an `Authorization` header, so the secret is in the URL. Either form is enough. A valid `/t/<token>/` prefix is stripped once.

- `http://host:8080/tuner-1-playlist.m3u?token=YOUR_SECRET`
- `http://host:8080/epg.xml?token=YOUR_SECRET`
- `http://host:8080/tve/fox.m3u?token=YOUR_SECRET`
- `http://host:8080/t/YOUR_SECRET/tuner-1-playlist.m3u`
- `http://host:8080/t/YOUR_SECRET/epg.xml`
- `http://host:8080/t/YOUR_SECRET/tve/fox.m3u`

These paths require the token when it is set: `/tuner-N-playlist.m3u`, `/tunerN/channels.m3u`, `/tunerN/m3u`, `/epg.xml`, `/xmltv.xml`, `/tve/{id}.m3u`, `/tve/{id}.xml`, `/tve/{source}/{channel}/stream`, `/tve/directv`, `/tve/directv/code`, `/tve/directv/activate`, and `/tve/directv/import`. A wrong or missing token is `401` with body `unauthorized`. A `/t/` prefix that does not match is `401` even for a path that would otherwise be open, including `/health`.

`/health`, `/metrics`, `/`, `/status`, and `/style.css` do not require the token. `/` and `/status` embed it in the Pluto and TV Everywhere URLs they display. Pair the token with `BIND_ADDR` so those pages are not reachable off-box. The process logs whether the gate is on, not the token. A rejected token is not copied into the error text. Every response sets `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.

`X-Forwarded-Proto` (`http` or `https`) and `X-Forwarded-Host` change the origin shown on `/` and `/status`. Only the first comma-separated value is used.

## HTTP

Tuner numbers are 1 through `TUNER_COUNT`. Only those playlist routes are registered.

| Path | Response |
|---|---|
| `/tuner-N-playlist.m3u` | Primary M3U. Also `/tunerN/channels.m3u` and `/tunerN/m3u`. |
| `/epg.xml`, `/xmltv.xml` | Shared XMLTV guide. |
| `/tve/{id}.m3u`, `/tve/{id}.xml` | One TV Everywhere source, when `TVE_SOURCES` includes it. `id` `all` is the combined feed when `TVE_COMBINED` is on. `404` when the feature is off or the id is unknown. |
| `/tve/{source}/{channel}/stream` | Local TV Everywhere stream. A channel whose playlist URL is already absolute does not use this route (`404`). |
| `/tve/directv` | DirecTV sign-in page. `Cache-Control: no-store`. `POST /tve/directv/activate` starts browser sign-in. `POST /tve/directv/import` stores a pasted Adobe token or a cookie export. `POST /tve/directv/code` continues a paused scripted code prompt. |
| `/` | HTML status page with playlist and EPG URLs, and the Channels setup steps. Any other unmatched path is `404`. |
| `/style.css` | Stylesheet for that page. `Cache-Control: public, max-age=300`. |
| `/health` | Readiness probe. `Cache-Control: no-store`. |
| `/version` | Plain text `version` and `commit` for the running binary. `Cache-Control: no-store`. Open without `ACCESS_TOKEN`. |
| `/status` | JSON snapshot. `Cache-Control: no-store`. The token value is not a field. |
| `/metrics` | Prometheus text `0.0.4`. `Content-Type: text/plain; version=0.0.4; charset=utf-8`. `Cache-Control: no-store`. |
| `/t/<token>/...` | The Pluto and TV Everywhere paths above, after one prefix strip, when `ACCESS_TOKEN` is set. |

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

`/status` is indented JSON. `refresh_every` is Go's duration text (`3h0m0s` for the default). `last_updated` is empty until the first successful refresh, then UTC `2006-01-02 15:04:05Z`. `access_token` is `true` or `false`. `tuners` and `epg_url` are absolute and include `?token=` when the gate is on. Other fields: `version`, `ready`, `auth_ready`, `tuner_count`, `channels`, `start_channel`, `guide_hours`, `bind_addr`. When TV Everywhere is enabled, a `tve` object lists the provider, `signed_in`, `pending`, `activation`, `method`, `expires`, `renewal`, `notice`, and the playlist and guide URLs. `expires` uses the same clock text. During browser sign-in it is when the activation code expires; after a session is stored it is when the DirecTV authentication expires. `activation` is true while a code is waiting. `renewal` is `client` while the Adobe client token can refresh on its own, `retrying` while that refresh is backing off, and `needs_browser` when only a person can replace the DirecTV login. `signed_in` stays true until that login's expiry, including during the last day, when `renewal` is already `needs_browser`. The object does not include the DirecTV username, password, cookies, Adobe authn token, client secret, device id, activation URL, or regcode. Those last two are only on `/tve/directv`.

Pluto playlists and the Pluto guide are gzip-encoded when `Accept-Encoding` lists `gzip` with a quality above 0. The response sets `Content-Encoding: gzip` and `Vary: Accept-Encoding`, and omits `Content-Length`. Uncompressed bodies set `Content-Length`. `Cache-Control` is `no-cache`, and `Last-Modified` is set once the guide has a timestamp. TV Everywhere responses, errors, `/health`, `/status`, `/metrics`, `/`, and `/style.css` are not gzipped.

`/metrics` always emits `pluto_ready`, `pluto_auth_ready`, `pluto_channels`, `pluto_tuners`, `pluto_guide_hours`, `pluto_last_refresh_timestamp_seconds`, `pluto_refresh_total` (`ok` and `error`), and `pluto_http_responses_total` for playlist and EPG codes `200`, `503`, and `other`, plus auth `401` and `other`.

## TV Everywhere

Pluto can also publish DirecTV TV Everywhere lineups as separate Channels DVR custom sources. This follows the behavior described by [FastChannels](https://github.com/kineticman/FastChannels/tree/development/app/tve). That project ships no license, so the Go code here is a clean-room reimplementation: public channel names, requestor ids, and stream URLs are used as data, and none of the Python is copied.

Set `TVE_SOURCES` to turn the feature on. Each enabled source has its own playlist and its own XMLTV document. `TVE_COMBINED` (default on) also publishes `/tve/all.m3u` and `/tve/all.xml`. Channel numbers start at `TVE_START_CHANNEL` (default `20000`) so they do not collide with Pluto's `START_CHANNEL`. The same `ACCESS_TOKEN` from [Access](#access) covers these routes. Status-page links use `?token=`. Stream URLs inside a playlist use `/t/<token>/` so a player that drops query strings still presents the token. Those bodies are not gzipped. A playlist is `application/x-mpegurl`. A guide is `application/xml; charset=utf-8`.

| Variable | Default | Allowed |
|---|---|---|
| `TVE_SOURCES` | empty (off) | Comma-separated `aenetworks`, `amcn`, `warner`, `fox`, `discovery`. Unknown ids are a startup error. Duplicates are dropped. |
| `TVE_PROVIDER` | `dtv` when any source is set | Only `dtv`. Anything else is a startup error. |
| `TVE_COMBINED` | `true` | Go bool (`true`, `false`, `1`, `0`). |
| `TVE_START_CHANNEL` | `20000` | Integer ≥ 0. |
| `DIRECTV_USERNAME` | empty | Trimmed. Lineups still serve when this is empty. |
| `DIRECTV_PASSWORD` | empty | Kept as written, including spaces. Never logged and never written to the session file. |
| `TVE_SESSION_FILE` | beside the device-id file, `pluto-tve-session.json` | DirecTV session: the Adobe authn token, the Adobe client id, client secret, device id, and access token that token is bound to, cookies, a paused ForgeRock prompt, and a browser activation while one is in progress. Written atomically, mode `0600`. The password is not a field. |

### DirecTV sign-in

Browser sign-in is the path that works. Headless calls to `identity.directv.com` are often blocked, and Adobe's provider redirect cannot land on a local Pluto URL. Open `/tve/directv` in Safari or Firefox (add the access token when the gate is on).

1. Choose **Start browser sign-in**. Pluto reads a public Adobe registration document from the History or Turner site, registers an Adobe client, and shows an activation link plus a code. The code lasts about 30 minutes. A+E uses requestor `HISTORY`. Warner, when A+E is off, uses `TNT`. Fox and Discovery use the History document. AMC alone does not publish a document this process can discover, so that button explains the miss: enable `aenetworks` or `warner`, or paste a token.
2. Open the link and sign in to DirecTV in that browser. Adobe then sends the browser to history.com or tntdrama.com. This page reloads every 15 seconds and stores the Adobe authn token when the code completes. The method is `browser`.
3. `/tve/directv` and `/status` show whether a session is stored, how it was stored, and when it expires. Start browser sign-in again before then. `/status` and `/` do not show the activation link or the code.

The other handoff is a paste. Put an Adobe authn token in the box, or upload a Netscape `cookies.txt` or a JSON cookie export that contains one. The next page says whether it was stored. The method is `token`. DirecTV website cookies that do not carry an authn token are rejected: this process cannot replay them through the identity provider. A token with no expiry, or one that has already expired, is rejected and is not written to the session file.

Scripted login remains a fallback, and only when no authn token is stored. On refresh, when `DIRECTV_USERNAME` and `DIRECTV_PASSWORD` are both set, A+E or Warner is enabled, no browser activation is waiting, and the session file has no authn token, the process fetches the same Adobe sign-in form and continues DirecTV's ForgeRock login. The method is `scripted`. If DirecTV asks for a code or a menu choice, refresh pauses. Submit it at `POST /tve/directv/code`. The code is not logged. `identity.directv.com` often answers 403 to this client. That path is not used to renew a session that already has an authn token.

The DirecTV authentication lasts until the `simpleTokenExpires` time inside the authn token. Adobe sends that as a calendar time, commonly `2027/01/20 15:04:05 GMT` or the same instant with `_GMT`, and the element may be HTML-escaped, namespaced, or wrapped in CDATA. A unix time in seconds or milliseconds is accepted too. A small integer such as `simpleTokenTTL` of `43200` is a duration, not the login's expiry, and is ignored. The MVPD chooses the lifetime. It is not a fixed 90 days, and it is not 12 hours. A build that could not read the timestamp stored "now plus 12 hours" instead. That stand-in is gone. The next refresh rewrites a stored expiry from the token when the timestamp is readable. A token with no readable expiry stays in use until Adobe rejects it. A cookie-only scripted session, which has no authn token, is still reused for 12 hours after it was written.

Browser sign-in keeps the Adobe client id, client secret, device id, fingerprint, and access token in the session file after the code completes. The authn token is bound to that client and that device. On each guide refresh the process asks Adobe for a new client access token with those same credentials (`grant_type=client_credentials` on `/o/client/token`). It does this when the access token has less than the guide interval plus one hour left, and always when less than two hours remain. `expires_in` is the lifetime, in seconds. A missing or absurd value is treated as 6 hours. That call refreshes the client credential only. It does not extend the DirecTV login, and it does not register a new client or a new device. A failure backs off from one minute to one hour, in memory, and `/status` shows `renewal` as `retrying`. The log line is the HTTP status, content type, and a short snippet. The client secret, access token, authn token, device id, and fingerprint are not in the log.

A session file written before those client fields were kept cannot refresh headlessly. One more browser sign-in stores them. Until then `renewal` is `needs_browser` and the sign-in page says so. The false 12-hour date is still corrected on the next refresh when the stored token contains `simpleTokenExpires`.

`GET /api/v1/tokens/authn` reports the current expiry. It does not extend the login. The check runs when the stored expiry is unknown or inside 24 hours, and only when a device id and access token are on file. HTTP 404, 401, and 400 are logged once and retried a day later; the session stays. HTTP 410 is ignored when the token's own timestamp is still more than a day ahead. Otherwise the session is marked expired and a later refresh does not bring it back from the old timestamp. One WARN is logged when the DirecTV authentication is within 24 hours, one when it is within an hour, and one when it has expired. With the default 3-hour guide interval, a refresh can land inside the last day and skip the last-hour warning.

AMC's refresh call must send the refresh token from the unauth response, not the access token. The cache hashes from that response are sent on the later calls. A refresh 401 or 403 is logged and playback continues with the unauth token. Every `/tve/{source}/{channel}/stream` request logs `tve stream request` and then `tve stream` with the source, channel, and HTTP status, including a 503. AMC also logs `tve playback step` for unauth, refresh, mvpd-auth, and playback with the host, path, and status. `/status` `tve.playback` is the same result. Startup logs `pluto starting` with `version` and `commit`, and `tve session loaded` with whether an authn token is on disk. A browser or pasted session is not replaced by the scripted password login. That login logs `directv scripted sign-in starting` only when the session file has no authentication token.

Playback uses that stored authn token. `/tve/{source}/{channel}/stream` asks Adobe to authorize the channel's requestor and resource, mints a short media token, and calls the network's playback API. AMC, and TCM, first ask Adobe's decision API for that requestor. AMC sends the serialized token. TCM base64-decodes it. If that API is missing for this Adobe client, playback uses the legacy short token instead. An explicit "not entitled" decision is returned as-is. A successful result is a 302 to the HLS URL. Fox News and Fox Business, whose unsigned CDN URLs answer 403, are signed that way; if signing fails and the CDN accepts a browser referer, the playlist is returned directly with relative addresses rewritten to the CDN. Fox sports calls `checkadobeauthn` for the stored device. A token whose `mvpdid` claim names a different provider is rejected before playback. Discovery forwards whatever session cookie its token call sets into the playback call. The authz token is cached for about 23 hours. The short media token is cached for 5 minutes. A manifest URL is cached for 10 minutes, or until a signed URL's own expiry. A network 401 or 403 drops those caches and authorizes once more. The response body names the failing step (`directv sign-in`, `adobe authorize`, `adobe decision`, `adobe shortAuthorize`, `amc playback`, `warner media`, `fox token`, `fox sports checkadobeauthn`, `fox sports playback`, `discovery token`, `discovery playback`, and the others). It does not include the authn token, media token, or password. The log line is `tve playback failed` with the source, channel, HTTP status, and that same text.

The authn token is bound to the Adobe client and requestor from the browser sign-in, usually `HISTORY` when A+E is enabled. Authorize is attempted for the channel's own requestor, then once more with the requestor named inside the token when that field is present. AMC, Warner, Fox, and Discovery each have their own requestor. A History sign-in may not entitle them until Adobe accepts that token. A+E playback stays on the public HLS URL.

A failed sign-in keeps the previous guide. The session file is mode `0600`. None of the secrets above are logged, and `/status` and `/` do not include them.

An older build logged `directv sign-in page failed` with HTTP 405 because it sent GET to Adobe's POST-only session collection. That request is no longer made.

### What each source does

| Source | Playlist | Guide | Streams |
|---|---|---|---|
| `aenetworks` | History, A&E, Lifetime, FYI | Two days of the public schedule, plus a now-playing row when it does not overlap | Public Google DAI HLS URLs |
| `amcn` | AMC, BBC America, IFC, WE tv | Two days from each network's public schedule page | Adobe decision token when that API answers, otherwise the short media token, then AMC's livestream API. 302 to the HLS URL |
| `warner` | TNT, TBS, truTV, and TCM, east and west | Turner schedule widget for TNT, TBS, and truTV (the HTML page's schedule blob). TCM uses the public listings query | Adobe media token, then the Turner media service. 302 to the HLS URL. The manifest is often Widevine, so Channels may still be unable to decrypt it |
| `fox` | News, Business, Weather, and the sports channels | News and Business JSON feeds, plus the public sports listings. Weather has no public schedule and stays empty | News and Business are signed HLS. Weather stays on the public Uplynk URL. Sports uses Fox's listing id, `checkadobeauthn`, and the partner playback API |
| `discovery` | TLC, Food, Discovery, HGTV, ID, Animal Planet, OWN, Travel, Cooking, Magnolia | Anonymous listings from the Discovery playback service. Catalog ids that differ from the site slug are mapped | Adobe media token, then Discovery's channel playback API, carrying the session cookie from the token call. 302 to the HLS URL. A clear stream is preferred when the response includes one |

These sources were not implemented: NBCUniversal (Widevine), FOX One, Disney (phone activation), CNN, C-SPAN, Versant (USA, E!, Syfy, and the rest of that family), Univision, Tennis Channel, DAZN, YES, MSG, and Sundance. Xfinity, Cox, and Spectrum need a browser session, and YouTube TV needs a Google master token. None of those match a DirecTV login that this process can finish on its own.

A guide response that is not JSON is logged with the source, HTTP status, content type, and a short body snippet. A source that produces zero programmes is logged too, and the previous programmes for that channel are kept.

These playback calls still need a live check with the stored DirecTV session. The authn from browser sign-in is usually for History, so Adobe may refuse AMC, Turner, Fox, and Discovery until that requestor accepts it. AMC's livestream API must accept the decision token or the legacy short token. Turner's media service often returns a Widevine manifest, and Channels may be unable to decrypt it. Fox News and Fox Business need the token generator to accept a media token for `foxnews` or `foxbusiness`; the unsigned CDN URLs answered 403 without one. Fox sports `checkadobeauthn` only recognizes a device that completed Fox's own sign-in, which this History session does not. Discovery's playback API expects a gauth session cookie from `auth.watch.hgtv.com`, which the Adobe authn does not create; the token call is likely to fail and the body will name `discovery token`. The guides above are public and do not use the DirecTV password.

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
- Keep `pluto.conf` mode `0600`. `PLUTO_PASSWORD`, `DIRECTV_PASSWORD`, and boot errors are redacted in logs. The DirecTV session file is mode `0600` and does not store the password. It stores the Adobe authn token and, after browser sign-in, the client id, client secret, device id, fingerprint, and access token. During browser sign-in it also stores the regcode. Those values are not written to the log, `/status`, or `/`.
- Device IDs are not secret account passwords, but the file is mode `0600` and should stay on the volume you back up. Deleting it makes Pluto TV treat the tuners as new devices.
- The listener is plain HTTP. Terminate TLS in front of it if the link is not trusted, and send `X-Forwarded-Proto: https` if the status page should show `https` URLs.
- `/metrics` has no token. Do not publish it on an open port. It does not contain the secret or account password.

## Troubleshooting

| What you see | What it means |
|---|---|
| Process exits immediately | Configuration failed before listen. The log says which variable (`PLUTO_EMAIL is required`, `TUNER_COUNT`, `PORT`, `BIND_ADDR`, `ACCESS_TOKEN`, `REFRESH_EVERY`, `GUIDE_HOURS`, `TVE_SOURCES`, `TVE_PROVIDER`, `TVE_COMBINED`, `TVE_START_CHANNEL`). `PLUTO_CONFIG` pointing at a missing file is also fatal. |
| `/health` is `503 authenticating` | The first Pluto sign-in is still running. Playlists return `503 tuners still authenticating` and do not start a second sign-in. The guide can still be loading in parallel. |
| `/health` is `503 not ready`, playlists say `playlist not yet available` | The sign-in attempt finished (it may have failed) and no guide is cached yet. Startup retries run at 1 minute, 5 minutes, and 30 minutes. A failed sign-in is retried on the next playlist request. The process keeps running. |
| `401 unauthorized` | The gate is on and this URL has no matching token. Copy the URL from `/`. A wrong `/t/...` prefix returns 401 for `/health` too. The same token covers `/tve/...`. |
| `/tve/...` is `404` | `TVE_SOURCES` is empty, or the source id is not one of the five names above. `/tve/all.m3u` is also `404` when `TVE_COMBINED` is false. |
| A TV Everywhere stream names a step (`adobe authorize`, `amc playback`, `warner media`, `fox token`, `fox sports playback`, `discovery playback`) | That step failed. The body is the step name and the HTTP status or Adobe's short message. A 401 or 403 is retried once. The body does not include the password or the media token. |
| Fox News or Fox Business is 403 | The playlist now points at `/tve/fox/fox_news/stream` and `/tve/fox/fox_business/stream`, which sign the CDN URL. An unsigned `247.foxnews.com` or `247.foxbusiness.com` master answers 403. |
| `directv sign-in page failed` with status 405 | An old build sent GET to Adobe's POST-only session collection. Use browser sign-in on `/tve/directv`. |
| Session expires about 12 hours after browser sign-in | An old build stored "now plus 12 hours" when it could not read `simpleTokenExpires`. Update, then let one refresh run. A readable timestamp in the stored token replaces that date. A token with no readable date stays until Adobe rejects it. |
| `renewal` is `needs_browser` right after upgrading | The session file from the previous build has the authn token and not the Adobe client identity. One more browser sign-in on `/tve/directv` stores that identity. After that the client token refreshes on the guide interval until the DirecTV authentication expires. |
| `tve directv authentication expires within 24h` | The DirecTV login itself is inside its last day. The client-token refresh cannot extend it. Sign in again with Safari or Firefox on `/tve/directv`. A second WARN is logged inside the last hour when a refresh lands there. |
| DirecTV sign-in page was not available | The scripted fallback did not get a provider form. The log has the HTTP status, content type, and a short snippet. Sign in with Safari or Firefox on `/tve/directv`. |
| `tve guide response was not json` | That guide URL returned HTML or another non-JSON body. The log includes the source, status, content type, and a short snippet. Previous programmes stay. |
| `tve guide returned zero programmes` | The new fetch had no programmes. Fox Weather does this because it has no public schedule. Previous programmes stay. |
| `invalid character '<'` looking for JSON | An old build parsed the Turner schedule page as JSON. That page is HTML. Update. |
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
