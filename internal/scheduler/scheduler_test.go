package scheduler

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/metrics"
)

func TestRetryInitialStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if retryInitial(ctx, nil, nil, nil, time.Hour, 24, nil) {
		t.Fatal("cancelled context should stop the startup retry loop")
	}
}

func TestSessionRefreshErrorPolicy(t *testing.T) {
	if err := sessionRefreshError(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := sessionRefreshError(context.Background(), errors.New("boot failed")); err != nil {
		t.Fatal("a tuner boot failure should not abort the guide update")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sessionRefreshError(ctx, errors.New("boot failed")); err == nil {
		t.Fatal("cancellation should abort the refresh")
	}
}

func TestNoteRefreshSkipsCancellation(t *testing.T) {
	noteRefresh(context.Background(), nil, nil)
	rec := metrics.New()
	noteRefresh(context.Background(), rec, nil)
	noteRefresh(context.Background(), rec, errors.New("guide down"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	noteRefresh(ctx, rec, errors.New("guide down"))

	var buf bytes.Buffer
	rec.Write(&buf, metrics.Gauges{})
	body := buf.String()
	if !strings.Contains(body, "pluto_refresh_total{result=\"ok\"} 1\n") {
		t.Fatalf("ok count:\n%s", body)
	}
	if !strings.Contains(body, "pluto_refresh_total{result=\"error\"} 1\n") {
		t.Fatalf("error count:\n%s", body)
	}
}
