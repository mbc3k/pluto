package tve

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/config"
)

func TestRenewCorrectsFalseTwelveHourExpiry(t *testing.T) {
	const (
		secret = "client-secret-value"
		pass   = "s3cret-password"
	)
	token := `<authnToken><simpleTokenExpires>2027/01/20 15:04:05 GMT</simpleTokenExpires><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>`
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/o/client/token":
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"refreshed-access-token","expires_in":86400}`)
		case strings.Contains(r.URL.Path, "/api/v2/") || strings.Contains(r.URL.Path, "weblogin") || r.URL.Path == "/o/client/register" || r.URL.Path == "/indiv/devices":
			t.Errorf("renewal used %s %s", r.Method, r.URL.Path)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{
		Signed:        true,
		Method:        "browser",
		Expires:       time.Now().Add(12 * time.Hour).UTC(),
		AuthnToken:    token,
		Requestor:     "HISTORY",
		ClientID:      "client-id-value",
		ClientSecret:  secret,
		AccessToken:   "old-access-token",
		AccessExpires: time.Now().Add(30 * time.Minute).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	svc := newRenewService(t, path, pass)
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.adobeBase = srv.URL
	svc.refresh(context.Background())

	view := svc.SignIn()
	want := time.Date(2027, 1, 20, 15, 4, 5, 0, time.UTC)
	if !view.Signed || !view.Expires.Equal(want) || view.Renewal != renewalClient {
		t.Fatalf("view %+v", view)
	}
	if calls.Load() != 1 {
		t.Fatalf("token refresh calls %d", calls.Load())
	}
	raw := readSession(t, path)
	if !strings.Contains(raw, "refreshed-access-token") || strings.Contains(raw, pass) || !strings.Contains(raw, secret) {
		t.Fatalf("session file\n%s", raw)
	}
	assertMode(t, path, 0o600)
	text := logs.String()
	for _, hidden := range []string{secret, pass, "guid-1", "refreshed-access-token", "old-access-token"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("log contained %s\n%s", hidden, text)
		}
	}

	svc.refresh(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("refreshed again: %d", calls.Load())
	}
}

func TestClientRefreshBacksOff(t *testing.T) {
	const secret = "client-secret-value"
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/o/client/token" {
			calls.Add(1)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "<html>unavailable</html>")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	exp := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second)
	token := `<authnToken><simpleTokenExpires>` + exp.Format("2006/01/02 15:04:05") + ` GMT</simpleTokenExpires><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>`
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{
		Signed:        true,
		Method:        "browser",
		Expires:       exp,
		AuthnToken:    token,
		ClientID:      "client-id-value",
		ClientSecret:  secret,
		AccessToken:   "old-access-token",
		AccessExpires: time.Now().Add(10 * time.Minute).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	svc := newRenewService(t, path, secret)
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.adobeBase = srv.URL
	svc.refresh(context.Background())
	svc.refresh(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	view := svc.SignIn()
	if !view.Signed || view.Renewal != renewalRetry {
		t.Fatalf("%+v", view)
	}
	text := logs.String()
	if !strings.Contains(text, "tve adobe client token refresh failed") || !strings.Contains(text, "status=500") || !strings.Contains(text, "content_type=text/html") || !strings.Contains(text, "unavailable") {
		t.Fatalf("log %s", text)
	}
	if strings.Contains(text, secret) {
		t.Fatalf("log leaked the client secret\n%s", text)
	}
}

func TestExpiryWarnings(t *testing.T) {
	exp := time.Now().Add(20 * time.Hour).UTC().Truncate(time.Second)
	token := `<authnToken><simpleTokenExpires>` + exp.Format("2006/01/02 15:04:05") + ` GMT</simpleTokenExpires><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>`
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{Signed: true, Method: "browser", Expires: exp, AuthnToken: token}); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	svc := newRenewService(t, path, "")
	svc.client = offlineClient(t)
	svc.refresh(context.Background())
	svc.refresh(context.Background())
	text := logs.String()
	if strings.Count(text, "tve directv authentication expires within 24h") != 1 || !strings.Contains(text, clock(exp)) {
		t.Fatalf("log %s", text)
	}
	view := svc.SignIn()
	if !view.Signed || view.Renewal != renewalBrowser || !strings.Contains(view.Notice, "Safari or Firefox") || strings.Contains(view.Notice, "guid-1") {
		t.Fatalf("%+v", view)
	}

	soon := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	soonToken := `<authnToken><simpleTokenExpires>` + soon.Format("2006/01/02 15:04:05") + ` GMT</simpleTokenExpires><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>`
	path = filepath.Join(t.TempDir(), "soon.json")
	if err := saveState(path, State{Signed: true, Method: "browser", Expires: soon, AuthnToken: soonToken}); err != nil {
		t.Fatal(err)
	}
	logs = captureLogs(t)
	svc = newRenewService(t, path, "")
	svc.client = offlineClient(t)
	svc.refresh(context.Background())
	if !strings.Contains(logs.String(), "tve directv authentication expires within an hour") || strings.Contains(logs.String(), "guid-1") {
		t.Fatalf("log %s", logs.String())
	}
}

func TestAuthnCheckGoneAndUnavailable(t *testing.T) {
	const device = "device-12345"
	soon := time.Now().Add(10 * time.Hour).UTC().Truncate(time.Second)
	token := `<authnToken><simpleTokenExpires>` + soon.Format("2006/01/02 15:04:05") + ` GMT</simpleTokenExpires><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>`
	var status atomic.Int32
	var checks atomic.Int32
	status.Store(http.StatusGone)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tokens/authn" {
			checks.Add(1)
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(int(status.Load()))
			fmt.Fprint(w, `<error><status>410</status><message>Token expired</message></error>`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{
		Signed:        true,
		Method:        "browser",
		Expires:       soon,
		AuthnToken:    token,
		Requestor:     "HISTORY",
		ClientID:      "client-id-value",
		ClientSecret:  "client-secret-value",
		AccessToken:   "access-token-value",
		AccessExpires: time.Now().Add(24 * time.Hour).UTC(),
		DeviceID:      device,
	}); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	svc := newRenewService(t, path, "")
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.adobeBase = srv.URL
	svc.refresh(context.Background())
	view := svc.SignIn()
	if view.Signed || view.Renewal != renewalBrowser || !strings.Contains(view.Notice, "expired") {
		t.Fatalf("%+v", view)
	}
	if strings.Contains(logs.String(), device) || strings.Contains(logs.String(), "access-token-value") || strings.Contains(logs.String(), "guid-1") {
		t.Fatalf("log leaked session material\n%s", logs.String())
	}
	svc.refresh(context.Background())
	view = svc.SignIn()
	if view.Signed || checks.Load() != 1 {
		t.Fatalf("rejected authn was checked again: signed=%v checks=%d", view.Signed, checks.Load())
	}

	status.Store(http.StatusNotFound)
	checks.Store(0)
	unknown := `<authnToken><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>`
	path = filepath.Join(t.TempDir(), "unknown.json")
	if err := saveState(path, State{
		Signed:        true,
		Method:        "browser",
		AuthnToken:    unknown,
		Requestor:     "HISTORY",
		ClientID:      "client-id-value",
		ClientSecret:  "client-secret-value",
		AccessToken:   "access-token-value",
		AccessExpires: time.Now().Add(24 * time.Hour).UTC(),
		DeviceID:      device,
	}); err != nil {
		t.Fatal(err)
	}
	logs = captureLogs(t)
	svc = newRenewService(t, path, "")
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.adobeBase = srv.URL
	svc.refresh(context.Background())
	svc.refresh(context.Background())
	view = svc.SignIn()
	if !view.Signed || !view.Expires.IsZero() {
		t.Fatalf("a 404 cleared the session %+v", view)
	}
	if strings.Count(logs.String(), "tve authn check is not available") != 1 {
		t.Fatalf("log %s", logs.String())
	}
}

func newRenewService(t *testing.T, path, pass string) *Service {
	t.Helper()
	return NewService(&config.Config{
		TVESources:      []string{SourceFox},
		TVESessionFile:  path,
		RefreshEvery:    time.Hour,
		DirectvPassword: pass,
	})
}
