package pluto

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDoDoesNotRetryUnauthorized(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	resp, err := NewRetryClient().Get(context.Background(), srv.URL+"?password=supersecret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if hits != 1 {
		t.Fatalf("401 was retried %d times", hits)
	}
}

func TestDoRetriesRedactsAndHonorsRetryAfter(t *testing.T) {
	oldAttempts, oldBase, oldMax := retryMaxAttempts, retryBaseDelay, retryMaxDelay
	retryMaxAttempts = 3
	retryBaseDelay = time.Millisecond
	retryMaxDelay = 20 * time.Millisecond
	t.Cleanup(func() {
		retryMaxAttempts = oldAttempts
		retryBaseDelay = oldBase
		retryMaxDelay = oldMax
	})

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	raw := srv.URL + "?password=supersecret&username=user@example.com"
	_, err := NewRetryClient().Get(context.Background(), raw, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if hits != 3 {
		t.Fatalf("hits %d", hits)
	}
	if strings.Contains(err.Error(), "supersecret") || strings.Contains(err.Error(), "user@example.com") {
		t.Fatalf("credentials leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("expected redacted url, got %v", err)
	}
}

func TestDoStripsRefererAndRefusesCrossHostRedirect(t *testing.T) {
	var referer string
	var nextHits int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		nextHits++
		referer = r.Referer()
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.test/stolen", http.StatusFound)
	})

	c := NewRetryClient()
	resp, err := c.Get(context.Background(), srv.URL+"/start?password=supersecret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" || nextHits != 1 {
		t.Fatalf("redirect follow failed: hits=%d body=%q err=%v", nextHits, body, err)
	}
	if strings.Contains(referer, "supersecret") || referer != "" {
		t.Fatalf("referer leaked credentials: %q", referer)
	}

	resp, err = c.Get(context.Background(), srv.URL+"/away?password=supersecret", nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected cross-host redirect to fail")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("password leaked: %v", err)
	}
}

func TestDoCancelsPromptly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewRetryClient().Get(ctx, srv.URL, nil)
	if err == nil {
		t.Fatal("expected cancel")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
}

func TestBackoffDelayIsBounded(t *testing.T) {
	for attempt := 0; attempt <= 20; attempt++ {
		d := backoffDelay(attempt)
		if d < retryBaseDelay || d > retryMaxDelay {
			t.Fatalf("attempt %d delay %s out of range", attempt, d)
		}
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("https://boot.pluto.tv/v4/start?username=a@b.c&password=s3cret&appName=web")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "a@b.c") {
		t.Fatalf("not redacted: %s", got)
	}
	if !strings.Contains(got, "appName=web") && !strings.Contains(got, "appName=web") {
		t.Fatalf("non-secret param dropped: %s", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("missing redaction marker: %s", got)
	}
}
