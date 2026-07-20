**Never go dark**: `cache.SetAll()` is only called on success. A failed refresh logs an error and serves the previous cache until the next retry. `SetAll` also invalidates lazily-cached per-tuner M3Us.

**JWT lifetime controls** (auth/session.go): `tokenRefreshMargin = 1h`, `maxTokenAge = 2h`. `EnsureFresh` (called on every M3U request and by the scheduler) forces re-auth if the token is within the margin of expiry *or* if the token has been handed out for > maxTokenAge. This mitigates 401s caused by Channels DVR consuming stale M3U entries. 401s from the stitcher are intentionally not retried inside RetryClient; the per-request refresh in server.go is the mitigation.

**M3U path**: On each playlist request the server calls `EnsureFresh` then `Credentials()` (one lock: token + stitcherParams + `tokenGen`). The M3U string is rebuilt only when `tokenGen` or channel data changes; otherwise the cached string is served. The scheduler does **not** pre-build M3Us — only channels + XMLTV.

**EPG fetch**: Four 6-hour windows are fetched concurrently and merged (see `pluto.FetchChannels`).

**Goroutine topology**: main context → HTTP server goroutine + scheduler goroutine. SIGTERM cancels the context, stops the scheduler, then drains the HTTP server with a 30s timeout.