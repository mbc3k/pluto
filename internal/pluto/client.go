package pluto

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"time"
)

const (
	retryMaxAttempts = 5
	retryBaseDelay   = 2 * time.Second
	retryMaxDelay    = 60 * time.Second

	httpTimeout       = 30 * time.Second
	httpKeepAlive     = 30 * time.Second
	httpIdleConns     = 20
	httpIdleConnTimeout = 90 * time.Second
)

// RetryClient is an HTTP client that retries on transient failures with
// exponential backoff and jitter.
type RetryClient struct {
	http *http.Client
}

// NewRetryClient creates a RetryClient with sensible timeout defaults.
func NewRetryClient() *RetryClient {
	transport := &http.Transport{
		MaxIdleConns:        httpIdleConns,
		IdleConnTimeout:     httpIdleConnTimeout,
		DisableCompression:  false,
	}
	return &RetryClient{
		http: &http.Client{
			Timeout:   httpTimeout,
			Transport: transport,
		},
	}
}

// Do executes a request with retry logic. The bodyFn is called before each
// attempt to produce a fresh request body (necessary because the body is
// consumed on the first attempt). Pass nil for bodyFn on GET requests.
//
// IMPORTANT: 401 responses are returned immediately (no retry). When the
// caller receives a 401 from a stitcher URL it should treat it as a signal
// that the JWT embedded in the M3U has expired. The auth/session layer
// (EnsureFresh + maxTokenAge) and the per-request refresh in server.go are
// the intended mitigation.
func (c *RetryClient) Do(ctx context.Context, method, url string, bodyFn func() io.Reader, headers map[string]string) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt < retryMaxAttempts; attempt++ {
		if attempt > 0 {
			delay := backoffDelay(attempt)
			slog.Debug("retrying request", "url", url, "attempt", attempt, "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		var body io.Reader
		if bodyFn != nil {
			body = bodyFn()
		}

		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// Retry on rate limiting or server errors.
		// 401 Unauthorized is NOT retried here. Stitcher 401s usually mean the
		// embedded JWT in the M3U URL has expired (see maxTokenAge in auth/session.go).
		// The HTTP server calls EnsureFresh on every M3U request; the scheduler
		// also refreshes on its cycle.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("after %d attempts: %w", retryMaxAttempts, lastErr)
}

// Get is a convenience wrapper for GET requests.
func (c *RetryClient) Get(ctx context.Context, url string, headers map[string]string) (*http.Response, error) {
	return c.Do(ctx, http.MethodGet, url, nil, headers)
}

// backoffDelay returns an exponential backoff delay with ±25% jitter.
func backoffDelay(attempt int) time.Duration {
	base := retryBaseDelay * time.Duration(1<<uint(attempt-1))
	if base > retryMaxDelay {
		base = retryMaxDelay
	}
	// Add jitter: ±25% of base.
	jitter := time.Duration(rand.Int63n(int64(base/2))) - base/4
	d := base + jitter
	if d < retryBaseDelay {
		d = retryBaseDelay
	}
	return d
}
