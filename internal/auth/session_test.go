package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/pluto"
)

func makeJWT(exp int64, padded bool) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	var payloadJSON string
	if padded {
		payloadJSON = fmt.Sprintf("{\"exp\": %d}", exp)
	} else {
		payloadJSON = fmt.Sprintf("{\"exp\":%d}", exp)
	}
	enc := base64.RawURLEncoding.EncodeToString
	if padded {
		enc = base64.URLEncoding.EncodeToString
	}
	return header + "." + enc([]byte(payloadJSON)) + ".sig"
}

func TestParseJWTExpiry(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour).Unix()
	exp, ok := parseJWTExpiry(makeJWT(soon, false))
	if !ok || exp.Unix() != soon {
		t.Fatalf("raw jwt: ok=%v exp=%s", ok, exp)
	}
	padded := makeJWT(soon, true)
	if !strings.Contains(strings.Split(padded, ".")[1], "=") {
		t.Fatal("fixture was not padded")
	}
	exp, ok = parseJWTExpiry(padded)
	if !ok || exp.Unix() != soon {
		t.Fatalf("padded jwt: ok=%v exp=%s token=%s", ok, exp, padded)
	}

	past := time.Now().Add(-time.Hour).Unix()
	exp, ok = parseJWTExpiry(makeJWT(past, false))
	if !ok || !exp.Before(time.Now()) {
		t.Fatalf("expired jwt should parse, ok=%v exp=%s", ok, exp)
	}

	if _, ok := parseJWTExpiry(makeJWT(time.Now().Add(30*24*time.Hour).Unix(), false)); ok {
		t.Fatal("far-future exp should be rejected")
	}
	if _, ok := parseJWTExpiry("not-a-jwt"); ok {
		t.Fatal("malformed token should be rejected")
	}
	if _, ok := parseJWTExpiry(makeJWT(0, false)); ok {
		t.Fatal("zero exp should be rejected")
	}
}

func TestDeviceIDsStableCleanAndCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "devices.conf")
	if _, err := loadOrCreateDeviceIDs(path, 2); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}

	first, err := loadOrCreateDeviceIDs(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateDeviceIDs(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("ids changed: %v vs %v", first, second)
	}
	for _, id := range first {
		if !uuidPattern.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
	}

	// Extras beyond the requested count stay on disk.
	third, err := loadOrCreateDeviceIDs(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	shrunk, err := loadOrCreateDeviceIDs(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if shrunk[0] != third[0] || shrunk[1] != third[1] {
		t.Fatalf("shrinking tuner count changed ids: %v vs %v", shrunk, third)
	}
	restored, err := loadOrCreateDeviceIDs(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if restored[2] != third[2] {
		t.Fatalf("extra id was not preserved: %v vs %v", restored, third)
	}

	dirty := filepath.Join(dir, "dirty.conf")
	body := "\nnot-a-uuid\n" + first[0] + "\n" + strings.ToUpper(first[0]) + "\n" + first[1] + "\n"
	if err := os.WriteFile(dirty, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cleaned, err := loadOrCreateDeviceIDs(dirty, 2)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned[0] != first[0] || cleaned[1] != first[1] {
		t.Fatalf("cleanup dropped valid ids: %v", cleaned)
	}
	got, err := os.ReadFile(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "not-a-uuid") || strings.Count(string(got), first[0]) != 1 {
		t.Fatalf("file not rewritten:\n%s", got)
	}
}

func TestDeviceIDsRejectDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadOrCreateDeviceIDs(dir, 1); err == nil {
		t.Fatal("expected error when device id path is a directory")
	}
}

func TestNewUUID(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidPattern.MatchString(id) {
		t.Fatalf("uuid %q", id)
	}
}

func newTestSession(t *testing.T, handler http.HandlerFunc) (*Session, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		Email:        "user@example.com",
		Password:     "supersecret",
		TunerCount:   1,
		DeviceIDFile: filepath.Join(t.TempDir(), "devices.conf"),
	}
	sessions, err := NewSessions(cfg, pluto.NewRetryClient())
	if err != nil {
		t.Fatal(err)
	}
	sessions[0].endpoint = srv.URL
	return sessions[0], &hits
}

func TestAuthenticateRefreshAndRedaction(t *testing.T) {
	var mode string
	s, hits := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("password") != "supersecret" {
			t.Errorf("password not sent")
		}
		if r.URL.Query().Get("clientID") == "" {
			t.Errorf("missing clientID")
		}
		switch mode {
		case "expired":
			fmt.Fprintf(w, `{"sessionToken":"%s","stitcherParams":"deviceId=abc"}`, makeJWT(time.Now().Add(-time.Hour).Unix(), false))
		case "garbage":
			ioWrite(w, `{"sessionToken":"not-a-jwt","stitcherParams":"deviceId=abc"}`)
		case "deny":
			http.Error(w, "bad password supersecret for user@example.com", http.StatusUnauthorized)
		default:
			fmt.Fprintf(w, `{"sessionToken":"%s","stitcherParams":"deviceId=abc"}`, makeJWT(time.Now().Add(24*time.Hour).Unix(), false))
		}
	})

	if err := s.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Fatalf("hits %d", *hits)
	}
	if s.needsRefresh(time.Now()) {
		t.Fatal("fresh token should not need refresh")
	}
	if err := s.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Fatalf("EnsureFresh re-authed a fresh token, hits %d", *hits)
	}

	issued := s.issuedAt
	s.Credentials()
	if !s.issuedAt.Equal(issued) {
		t.Fatal("handing out credentials extended the token age")
	}
	if !s.needsRefresh(time.Now().Add(maxTokenAge + time.Minute)) {
		t.Fatal("maxTokenAge should force refresh even when exp is far away")
	}

	// An old issuedAt refreshes on the next request.
	s.issuedAt = time.Now().Add(-3 * time.Hour)
	if err := s.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *hits != 2 {
		t.Fatalf("stale token was not refreshed, hits %d", *hits)
	}
	if s.needsRefresh(time.Now()) {
		t.Fatal("token should be fresh after refresh")
	}

	mode = "expired"
	s.issuedAt = time.Now().Add(-3 * time.Hour)
	if err := s.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.needsRefresh(time.Now()) {
		t.Fatal("already-expired token was treated as valid")
	}
	if time.Until(s.tokenExpiry) > 0 {
		t.Fatalf("expired token got a future expiry %s", s.tokenExpiry)
	}

	mode = "garbage"
	if err := s.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.needsRefresh(time.Now()) {
		t.Fatal("fallback TTL should be usable immediately")
	}
	if !s.needsRefresh(time.Now().Add(maxTokenAge + time.Minute)) {
		t.Fatal("fallback TTL must still honor maxTokenAge")
	}

	mode = "deny"
	s.issuedAt = time.Time{}
	err := s.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected boot failure")
	}
	if strings.Contains(err.Error(), "supersecret") || strings.Contains(err.Error(), "user@example.com") {
		t.Fatalf("credentials leaked: %v", err)
	}
}

func ioWrite(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body))
}
