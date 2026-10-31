package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mbc3k/pluto/internal/auth"
	"github.com/mbc3k/pluto/internal/cache"
	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/pluto"
	"github.com/mbc3k/pluto/internal/scheduler"
	"github.com/mbc3k/pluto/internal/server"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	setupLogger()
	slog.Info("pluto starting", "version", version, "commit", server.Commit())

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	client := pluto.NewRetryClient()

	sessions, err := auth.NewSessions(cfg, client)
	if err != nil {
		slog.Error("session init error", "err", err)
		os.Exit(1)
	}

	// Root context cancelled on SIGTERM/SIGINT, including during startup auth.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	c := cache.New(cfg.TunerCount)
	srv := server.New(c, sessions, cfg, version)
	// Playlists stay at 503 until this first attempt finishes. HoldAuth runs
	// before Listen so a request cannot arrive with the flag still open.
	// /health and the guide answer during sign-in.
	srv.HoldAuth()

	var wg sync.WaitGroup

	// HTTP server goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "err", err)
			cancel() // propagate failure
		}
	}()

	// Sign-in runs beside the listener. A failure does not exit: the next
	// playlist request retries through EnsureFresh, and the guide does not
	// need a session.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer srv.ReleaseAuth()
		slog.Info("authenticating tuners", "count", cfg.TunerCount)
		if err := authenticateAll(ctx, sessions); err != nil {
			if ctx.Err() == nil {
				slog.Error("initial authentication failed; playlist requests will retry", "err", err)
			}
			return
		}
		slog.Info("tuners authenticated", "count", cfg.TunerCount)
	}()

	// Scheduler goroutine: populates cache immediately then every RefreshEvery.
	// It starts in parallel with sign-in. The session lock keeps the two from
	// booting the same tuner twice.
	wg.Add(1)
	go func() {
		defer wg.Done()
		scheduler.Run(ctx, sessions, c, cfg, client, srv.Metrics())
	}()

	// Block until signal.
	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown error", "err", err)
	}

	wg.Wait()
	slog.Info("stopped")
}

func setupLogger() {
	level := slog.LevelInfo
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	switch raw {
	case "", "info":
		level = slog.LevelInfo
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		slog.Warn("unknown LOG_LEVEL, using info", "value", os.Getenv("LOG_LEVEL"))
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// bootConcurrency limits how many tuner boot requests run simultaneously
// to avoid bursting Pluto TV's auth endpoint.
const bootConcurrency = 3

// authenticateAll authenticates all sessions concurrently (up to bootConcurrency
// at a time) and returns every error encountered.
func authenticateAll(ctx context.Context, sessions []*auth.Session) error {
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
			errs[idx] = sess.Authenticate(ctx)
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
