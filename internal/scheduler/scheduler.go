package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mike/pluto/internal/auth"
	"github.com/mike/pluto/internal/cache"
	"github.com/mike/pluto/internal/config"
	"github.com/mike/pluto/internal/epg"
	"github.com/mike/pluto/internal/playlist"
	"github.com/mike/pluto/internal/pluto"
)

// initialRetryDelays controls how quickly we retry if the first data fetch
// fails at startup. After exhausting these, we fall back to cfg.RefreshEvery.
var initialRetryDelays = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
}

// Run starts the background refresh loop. It first populates the cache
// immediately (with retries on failure), then refreshes on the regular
// cfg.RefreshEvery interval. Run blocks until ctx is cancelled.
func Run(ctx context.Context, sessions []*auth.Session, c *cache.Cache, cfg *config.Config, client *pluto.RetryClient) {
	// Initial population with retry schedule.
	if err := refreshAll(ctx, sessions, c, cfg, client); err != nil {
		slog.Error("initial refresh failed", "err", err)
		if !retryInitial(ctx, sessions, c, cfg, client) {
			return // context cancelled
		}
	}

	ticker := time.NewTicker(cfg.RefreshEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refreshAll(ctx, sessions, c, cfg, client); err != nil {
				slog.Error("scheduled refresh failed, keeping stale data", "err", err)
			}
		}
	}
}

// retryInitial attempts the initial cache population using the escalating
// retry schedule. Returns false if the context was cancelled.
func retryInitial(ctx context.Context, sessions []*auth.Session, c *cache.Cache, cfg *config.Config, client *pluto.RetryClient) bool {
	for _, delay := range initialRetryDelays {
		slog.Info("scheduling initial refresh retry", "in", delay)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		if err := refreshAll(ctx, sessions, c, cfg, client); err != nil {
			slog.Error("retry refresh failed", "err", err)
		} else {
			return true
		}
	}
	return true // hand off to regular ticker regardless
}

// refreshAll re-authenticates stale sessions, fetches channels, and
// regenerates all M3U playlists and the XMLTV EPG in one atomic update.
// On failure the existing cache is left intact.
func refreshAll(ctx context.Context, sessions []*auth.Session, c *cache.Cache, cfg *config.Config, client *pluto.RetryClient) error {
	start := time.Now()

	// Refresh sessions that are close to expiry, concurrently.
	if err := refreshSessions(ctx, sessions); err != nil {
		return err
	}

	// One channel fetch serves all tuners.
	channels, err := pluto.FetchChannels(ctx, client)
	if err != nil {
		return err
	}
	slog.Debug("fetched channels", "count", len(channels))

	// Generate all M3U playlists concurrently (pure functions).
	m3u := make([]string, len(sessions))
	var wg sync.WaitGroup
	for i, sess := range sessions {
		wg.Add(1)
		go func(idx int, s *auth.Session) {
			defer wg.Done()
			m3u[idx] = playlist.Generate(cfg.StartChannel, s, channels)
		}(i, sess)
	}
	wg.Wait()

	xmltvData, err := epg.Generate(channels)
	if err != nil {
		return err
	}

	c.SetAll(m3u, xmltvData)
	slog.Info("refresh complete", "channels", len(channels), "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// refreshSessions concurrently re-auths any session expiring soon.
func refreshSessions(ctx context.Context, sessions []*auth.Session) error {
	errs := make([]error, len(sessions))
	var wg sync.WaitGroup
	for i, s := range sessions {
		wg.Add(1)
		go func(idx int, sess *auth.Session) {
			defer wg.Done()
			errs[idx] = sess.EnsureFresh(ctx)
		}(i, s)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("session %d: %w", i, err)
		}
	}
	return nil
}
