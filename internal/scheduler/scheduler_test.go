package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryInitialStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if retryInitial(ctx, nil, nil, nil, time.Hour) {
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
