package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	slog.Info("pluto starting", "version", version)

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

	// Authenticate all sessions concurrently before starting the server.
	slog.Info("authenticating tuners", "count", cfg.TunerCount)
	if err := authenticateAll(context.Background(), sessions); err != nil {
		slog.Error("initial authentication failed", "err", err)
		os.Exit(1)
	}

	c := cache.New(cfg.TunerCount)
	srv := server.New(c, cfg, version)

	// Root context cancelled on SIGTERM/SIGINT.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

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

	// Scheduler goroutine: populates cache immediately then every RefreshEvery.
	wg.Add(1)
	go func() {
		defer wg.Done()
		scheduler.Run(ctx, sessions, c, cfg, client)
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

// authenticateAll authenticates all sessions concurrently and returns the
// first error encountered, if any.
func authenticateAll(ctx context.Context, sessions []*auth.Session) error {
	errs := make([]error, len(sessions))
	var wg sync.WaitGroup
	for i, s := range sessions {
		wg.Add(1)
		go func(idx int, sess *auth.Session) {
			defer wg.Done()
			errs[idx] = sess.Authenticate(ctx)
		}(i, s)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
