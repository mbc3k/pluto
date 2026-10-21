package pluto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	httpTimeout          = 30 * time.Second
	httpDialTimeout      = 10 * time.Second
	httpKeepAlive        = 30 * time.Second
	httpIdleConns        = 20
	httpIdleConnsPerHost = 10
	httpIdleConnTimeout  = 90 * time.Second
	maxResponseBytes     = 64 << 20
	userAgent            = "pluto-for-channels"
)

// Retry knobs are variables so tests can shorten backoff. Production code
// does not reassign them.
var (
	retryMaxAttempts = 5
	retryBaseDelay   = 2 * time.Second
	retryMaxDelay    = 60 * time.Second
)

// RetryClient is an HTTP client that retries on transient failures with
// exponential backoff and jitter.
type RetryClient struct {
	http *http.Client
}

// NewRetryClient creates a RetryClient with sensible timeout defaults.
func NewRetryClient() *RetryClient {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   httpDialTimeout,
			KeepAlive: httpKeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          httpIdleConns,
		MaxIdleConnsPerHost:   httpIdleConnsPerHost,
		IdleConnTimeout:       httpIdleConnTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &RetryClient{
		http: &http.Client{
			Timeout:   httpTimeout,
			Transport: transport,
			// Boot requests put the account password in the query string.
			// Never forward that URL as a Referer, and never follow it onto
			// a different host.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("stopped after %d redirects", len(via))
				}
				if req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("refusing cross-host redirect")
				}
				req.Header.Del("Referer")
				return nil
			},
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
func (c *RetryClient) Do(ctx context.Context, method, rawURL string, bodyFn func() io.Reader, headers map[string]string) (*http.Response, error) {
	var lastErr error
	var wait time.Duration
	haveWait := false

	for attempt := 0; attempt < retryMaxAttempts; attempt++ {
		if attempt > 0 {
			delay := backoffDelay(attempt)
			if haveWait {
				delay = wait
				haveWait = false
			}
			slog.Debug("retrying request", "url", redactURL(rawURL), "attempt", attempt+1, "delay", delay)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			}
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var body io.Reader
		if bodyFn != nil {
			body = bodyFn()
		}

		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", userAgent)
		}
		if req.Header.Get("Accept") == "" {
			req.Header.Set("Accept", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			lastErr = redactError(err)
			// A refused redirect is not a transient transport error.
			if resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
				return nil, lastErr
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		// Retry on rate limiting or server errors.
		// 401 Unauthorized is NOT retried here. Stitcher 401s usually mean the
		// embedded JWT in the M3U URL has expired (see maxTokenAge in auth/session.go).
		// The HTTP server calls EnsureFresh on every M3U request; the scheduler
		// also refreshes on its cycle.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 {
			if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
				wait = d
				haveWait = true
			}
			drainAndClose(resp.Body)
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactURL(rawURL))
			continue
		}

		resp.Body = &limitedReadCloser{
			Reader: io.LimitReader(resp.Body, maxResponseBytes),
			Closer: resp.Body,
		}
		return resp, nil
	}

	return nil, fmt.Errorf("after %d attempts: %w", retryMaxAttempts, lastErr)
}

// Get is a convenience wrapper for GET requests.
func (c *RetryClient) Get(ctx context.Context, rawURL string, headers map[string]string) (*http.Response, error) {
	return c.Do(ctx, http.MethodGet, rawURL, nil, headers)
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 8<<10))
	body.Close()
}

// parseRetryAfter reads a Retry-After delay, capped at retryMaxDelay.
func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		d := time.Duration(secs) * time.Second
		if d > retryMaxDelay {
			d = retryMaxDelay
		}
		return d, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		if d > retryMaxDelay {
			d = retryMaxDelay
		}
		return d, true
	}
	return 0, false
}

// backoffDelay returns an exponential backoff delay with ±25% jitter,
// clamped to [retryBaseDelay, retryMaxDelay].
func backoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 5 {
		shift = 5
	}
	base := retryBaseDelay * time.Duration(1<<uint(shift))
	if base > retryMaxDelay || base <= 0 {
		base = retryMaxDelay
	}
	span := base / 4
	if span <= 0 {
		return base
	}
	jitter := time.Duration(rand.Int63n(int64(span*2+1))) - span
	d := base + jitter
	if d < retryBaseDelay {
		d = retryBaseDelay
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d
}

// redactURL hides credentials that the boot request carries in the query.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "redacted-url"
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	q := u.Query()
	redacted := false
	for k := range q {
		switch strings.ToLower(k) {
		case "password", "username", "jwt", "token", "sessiontoken":
			q.Set(k, "REDACTED")
			redacted = true
		}
	}
	if redacted {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// redactError rebuilds url.Error values so a failed boot cannot write the
// account password into a log line.
func redactError(err error) error {
	var uerr *url.Error
	if err == nil || !errors.As(err, &uerr) {
		return err
	}
	cp := *uerr
	cp.URL = redactURL(uerr.URL)
	return &cp
}
