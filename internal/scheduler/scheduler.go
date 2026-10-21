package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mbc3k/pluto/internal/auth"
	"github.com/mbc3k/pluto/internal/cache"
	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/epg"
	"github.com/mbc3k/pluto/internal/pluto"
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
	if err := refreshAll(ctx, sessions, c, client); err != nil {
		slog.Error("initial refresh failed", "err", err)
		if !retryInitial(ctx, sessions, c, client, cfg.RefreshEvery) {
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
			if err := refreshAll(ctx, sessions, c, client); err != nil {
				slog.Error("scheduled refresh failed, keeping stale data", "err", err)
			}
		}
	}
}

// retryInitial attempts the initial cache population using the escalating
// retry schedule. Returns false if the context was cancelled.
func retryInitial(ctx context.Context, sessions []*auth.Session, c *cache.Cache, client *pluto.RetryClient, interval time.Duration) bool {
	for _, delay := range initialRetryDelays {
		slog.Info("scheduling initial refresh retry", "in", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		if err := refreshAll(ctx, sessions, c, client); err != nil {
			slog.Error("retry refresh failed", "err", err)
			continue
		}
		return true
	}
	slog.Error("initial refresh retries exhausted; next attempt on the regular interval", "interval", interval)
	return true // hand off to regular ticker regardless
}

// refreshAll re-authenticates stale sessions, fetches channels, and
// regenerates the XMLTV EPG in one atomic update. Per-tuner M3U playlists
// are built lazily on request (and cached until the token or channel data
// changes) so the refresh path does not pay N×playlist cost up front.
// On failure the existing cache is left intact.
func refreshAll(ctx context.Context, sessions []*auth.Session, c *cache.Cache, client *pluto.RetryClient) error {
	start := time.Now()

	// Tuner auth keeps stream URLs working. The guide itself does not need a
	// session, so a boot failure must not block the channel update. Cancellation
	// still aborts the refresh.
	if err := sessionRefreshError(ctx, refreshSessions(ctx, sessions)); err != nil {
		return err
	}

	// One channel fetch serves all tuners.
	channels, err := pluto.FetchChannels(ctx, client)
	if err != nil {
		return err
	}
	slog.Debug("fetched channels", "count", len(channels))

	xmltvData, err := epg.Generate(channels)
	if err != nil {
		return err
	}

	// SetAll also invalidates any lazily-cached M3Us so the next request
	// rebuilds against the new channel list. It refuses an empty payload.
	if err := c.SetAll(channels, xmltvData); err != nil {
		return err
	}
	slog.Info("refresh complete", "channels", len(channels), "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// sessionRefreshError decides whether a tuner re-auth failure should fail the
// guide update. Cancellation aborts the refresh. A tuner that failed to boot
// does not: the channel API is unauthenticated, and playlist requests refresh
// their own session.
func sessionRefreshError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	slog.Error("session refresh failed; updating guide anyway", "err", err)
	return nil
}

// bootConcurrency limits how many tuner boot requests run simultaneously
// to avoid bursting Pluto TV's auth endpoint.
const bootConcurrency = 3

// refreshSessions concurrently re-auths any session expiring soon,
// capped at bootConcurrency simultaneous requests.
func refreshSessions(ctx context.Context, sessions []*auth.Session) error {
	errs := make([]error, len(sessions))
	sem := make(chan struct{}, bootConcurrency)
	var wg sync.WaitGroup
	for i, s := range sessions {
		wg.Add(1)
		go func(idx int, sess *auth.Session) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[idx] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			errs[idx] = sess.EnsureFresh(ctx)
		}(i, s)
	}
	wg.Wait()

	var joined []error
	for i, err := range errs {
		if err != nil {
			joined = append(joined, fmt.Errorf("tuner %d: %w", i+1, err))
		}
	}
	return errors.Join(joined...)
}
