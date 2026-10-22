package tve

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/source"
)

func TestBrowserActivationThenPoll(t *testing.T) {
	const (
		pass   = "s3cret-password"
		secret = "client-secret-value"
		access = "access-token-value"
		code   = "REGCODE1"
	)
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v2/") || strings.Contains(r.URL.Path, "weblogin") {
			t.Errorf("old sign-in request %s %s", r.Method, r.URL.Path)
		}
		switch {
		case r.URL.Path == "/live":
			fmt.Fprint(w, `<html><script id="__NEXT_DATA__">{"props":{"config":{"adobeSoftwareStatement":{"history":"eyJhbGciOiJub25lIn0.e30.sig"}}}}</script></html>`)
		case r.URL.Path == "/o/client/register":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"client_id":"client-id-value","client_secret":"%s"}`, secret)
		case r.URL.Path == "/o/client/token":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"access_token":"%s"}`, access)
		case r.URL.Path == "/indiv/devices":
			w.Header().Set("pass_sfp", "sfp-value")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"deviceId":"device-1"}`)
		case r.URL.Path == "/reggie/v1/HISTORY/regcode":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"code":"%s","expires":%d}`, code, time.Now().Add(20*time.Minute).UnixMilli())
		case r.URL.Path == "/adobe-services/session":
			w.Header().Set("Content-Type", "application/xml")
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "<reason>34763</reason>")
				return
			}
			fmt.Fprint(w, authnFixture("2000000000000"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	logs := captureLogs(t)
	path := filepath.Join(t.TempDir(), "session.json")
	svc := NewService(&config.Config{
		TVESources:      []string{SourceAE},
		TVESessionFile:  path,
		DirectvUsername: "viewer@example.com",
		DirectvPassword: pass,
	})
	svc.client = srv.Client()
	svc.adobeBase = srv.URL
	svc.historyStatementURL = srv.URL + "/live"

	if err := svc.BeginBrowserSignIn(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := svc.SignIn()
	if !view.Waiting || view.Code != code || !strings.Contains(view.ActivationURL, "reg_code="+code) || strings.Contains(view.Notice, secret) || strings.Contains(view.Notice, pass) {
		t.Fatalf("waiting view %+v", view)
	}
	assertMode(t, path, 0o600)
	raw := readSession(t, path)
	if !strings.Contains(raw, secret) || strings.Contains(raw, pass) {
		t.Fatal("session file did not keep the activation secret, or it stored the password")
	}

	svc.PollActivation(context.Background())
	view = svc.SignIn()
	if !view.Waiting || view.Signed {
		t.Fatalf("still waiting %+v", view)
	}

	svc.PollActivation(context.Background())
	view = svc.SignIn()
	if !view.Signed || view.Waiting || view.Method != "browser" || view.Code != "" || view.ActivationURL != "" {
		t.Fatalf("signed view %+v", view)
	}
	if strings.Contains(view.Notice, secret) || strings.Contains(view.Notice, access) || strings.Contains(view.Notice, "guid-1") || !strings.Contains(view.Notice, "expires") {
		t.Fatalf("notice %q", view.Notice)
	}
	raw = readSession(t, path)
	if !strings.Contains(raw, secret) || strings.Contains(raw, pass) || !strings.Contains(raw, "guid-1") || !strings.Contains(raw, `"method": "browser"`) {
		t.Fatalf("stored session after poll:\n%s", raw)
	}
	if view.Expires.IsZero() || time.Until(view.Expires) < 24*time.Hour {
		t.Fatalf("expiry fell back to a short lifetime: %s", view.Expires)
	}
	text := logs.String()
	for _, hidden := range []string{secret, access, pass, code, "guid-1"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("log contained %s\n%s", hidden, text)
		}
	}

	svc.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/o/client/") || strings.Contains(r.URL.Path, "regcode") || strings.Contains(r.URL.Path, "session") || strings.Contains(r.URL.Path, "/api/v2/") || strings.Contains(r.URL.Host, "directv") {
			t.Errorf("fresh session started another sign-in %s %s", r.Method, r.URL.Host+r.URL.Path)
		}
		return htmlResponse(r, http.StatusNotFound, "<html></html>"), nil
	})}
	svc.refresh(context.Background())
	view = svc.SignIn()
	if !view.Signed || view.Method != "browser" {
		t.Fatalf("refresh cleared the session %+v", view)
	}
}

func TestScriptedSignInDoesNotGetSessions(t *testing.T) {
	const pass = "s3cret-password"
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/live":
			fmt.Fprint(w, `<html><script id="__NEXT_DATA__">{"props":{"config":{"adobeSoftwareStatement":{"history":"eyJhbGciOiJub25lIn0.e30.sig"}}}}</script></html>`)
		case r.URL.Path == "/o/client/register":
			fmt.Fprint(w, `{"client_id":"client-id-value","client_secret":"client-secret-value"}`)
		case r.URL.Path == "/o/client/token":
			fmt.Fprint(w, `{"access_token":"access-token-value"}`)
		case r.URL.Path == "/indiv/devices":
			fmt.Fprint(w, `{"deviceId":"device-1"}`)
		case r.URL.Path == "/reggie/v1/HISTORY/regcode":
			fmt.Fprintf(w, `{"code":"REGCODE1","expires":%d}`, time.Now().Add(20*time.Minute).UnixMilli())
		case strings.Contains(r.URL.Path, "/authenticate/saml"):
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, "<html><body>Method Not Allowed</body></html>")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	logs := captureLogs(t)
	svc := NewService(&config.Config{
		TVESources:      []string{SourceAE},
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
		DirectvUsername: "viewer@example.com",
		DirectvPassword: pass,
	})
	svc.client = srv.Client()
	svc.adobeBase = srv.URL
	svc.historyStatementURL = srv.URL + "/live"

	_, _, _, err := svc.fetchSignInPage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 405") || !strings.Contains(err.Error(), "text/html") || !strings.Contains(err.Error(), "Method Not Allowed") {
		t.Fatalf("err %v", err)
	}
	text := logs.String()
	if !strings.Contains(text, "directv sign-in page failed") || !strings.Contains(text, "status=405") || !strings.Contains(text, "content_type=text/html") || !strings.Contains(text, "Method Not Allowed") {
		t.Fatalf("log %s", text)
	}
	if strings.Contains(text, pass) || strings.Contains(text, "client-secret-value") || strings.Contains(text, "access-token-value") || strings.Contains(text, "REGCODE1") {
		t.Fatalf("log leaked a secret\n%s", text)
	}
	for _, line := range snapshot(&mu, &seen) {
		if strings.Contains(line, "/api/v2/") || strings.Contains(line, "/sessions") {
			t.Fatalf("sessions collection was requested: %s", line)
		}
	}
}

func TestImportSessionFile(t *testing.T) {
	const pass = "s3cret-password"
	token := authnFixture("2000000000000")
	path := filepath.Join(t.TempDir(), "session.json")
	svc := NewService(&config.Config{
		TVESources:      []string{SourceFox},
		TVESessionFile:  path,
		DirectvUsername: "viewer@example.com",
		DirectvPassword: pass,
	})
	if err := svc.ImportSession(token); err != nil {
		t.Fatal(err)
	}
	view := svc.SignIn()
	if !view.Signed || view.Method != "token" || !strings.Contains(view.Notice, "expires") || strings.Contains(view.Notice, "guid-1") {
		t.Fatalf("%+v", view)
	}
	assertMode(t, path, 0o600)
	raw := readSession(t, path)
	if strings.Contains(raw, pass) || !strings.Contains(raw, "guid-1") {
		t.Fatalf("session file\n%s", raw)
	}

	netscape := "# Netscape HTTP Cookie File\n.adobe.com\tTRUE\t/\tTRUE\t0\tauthn\t" + token + "\n"
	if err := svc.ImportSession(netscape); err != nil {
		t.Fatal(err)
	}
	if err := svc.ImportSession(`[{"name":"authn","value":"` + token + `"}]`); err != nil {
		t.Fatal(err)
	}

	err := svc.ImportSession(".directv.com\tTRUE\t/\tTRUE\t0\tsession\twebsite-cookie-secret\n")
	if err == nil || strings.Contains(err.Error(), "website-cookie-secret") || !strings.Contains(err.Error(), "do not include") {
		t.Fatalf("cookie err %v", err)
	}
	view = svc.SignIn()
	if !view.Signed || strings.Contains(view.Notice, "website-cookie-secret") {
		t.Fatalf("failed paste cleared the session: %+v", view)
	}
	if err := svc.ImportSession(authnFixture("1500000000000")); err == nil || !strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "guid-1") {
		t.Fatalf("expired %v", err)
	}
}

func TestAMCOnlyBrowserSignInDoesNotCallNetwork(t *testing.T) {
	svc := NewService(&config.Config{
		TVESources:     []string{SourceAMC},
		TVESessionFile: filepath.Join(t.TempDir(), "session.json"),
	})
	svc.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return nil, fmt.Errorf("no network")
	})}
	if err := svc.ImportSession(authnFixture("2000000000000")); err != nil {
		t.Fatal(err)
	}
	err := svc.BeginBrowserSignIn(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not publish") {
		t.Fatalf("err %v", err)
	}
	view := svc.SignIn()
	if !view.Signed || view.Method != "token" || !strings.Contains(view.Notice, "does not publish") {
		t.Fatalf("%+v", view)
	}
}

func TestFoxPasswordDoesNotStartScriptedLogin(t *testing.T) {
	svc := NewService(&config.Config{
		TVESources:      []string{SourceFox},
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
		DirectvUsername: "viewer@example.com",
		DirectvPassword: "s3cret-password",
	})
	svc.client = offlineClient(t)
	svc.refresh(context.Background())
	view := svc.SignIn()
	if view.Signed || !strings.Contains(view.Notice, "scripted") || strings.Contains(view.Notice, "s3cret-password") {
		t.Fatalf("%+v", view)
	}
}

func TestGuideHTMLIsLoggedAndPreviousStays(t *testing.T) {
	const pass = "s3cret-password"
	logs := captureLogs(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html>not-json %s</html>", pass)
	}))
	defer upstream.Close()
	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(&config.Config{
		TVESources:      []string{SourceFox},
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
		DirectvPassword: pass,
	})
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.mu.Lock()
	feed := svc.feeds[SourceFox]
	feed.Channels[0].Programs = []source.Program{{
		Title: "Kept Show",
		Start: time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC),
		Stop:  time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC),
	}}
	svc.feeds[SourceFox] = feed
	svc.mu.Unlock()

	svc.refresh(context.Background())
	guide, _, ok := svc.Guide(SourceFox)
	if !ok || !strings.Contains(string(guide), "Kept Show") {
		t.Fatalf("previous guide lost ok=%v\n%s", ok, guide)
	}
	text := logs.String()
	if !strings.Contains(text, "tve guide returned zero programmes") || !strings.Contains(text, "tve guide response was not json") || !strings.Contains(text, "status=200") || !strings.Contains(text, "content_type=text/html") {
		t.Fatalf("log %s", text)
	}
	if strings.Contains(text, pass) || !strings.Contains(text, "REDACTED") {
		t.Fatalf("snippet was not redacted\n%s", text)
	}
}

func TestPublicGuidesFromFixtures(t *testing.T) {
	const stmt = "eyJhbGciOiJub25lIn0.e30.sig"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/schedule/data/"):
			fmt.Fprint(w, `[{"timeslotId":"slot-1","startTime":1700000000000,"endTime":1700003600000,"series":{"title":"Ancient Aliens"},"program":{"title":"The Episode","description":"A"}}]`)
		case r.Method == http.MethodPost && r.URL.Query().Get("brand") != "":
			fmt.Fprint(w, `{"data":{"nowPlaying":{"startTime":1700010000,"endTime":1700013600,"program":{"__typename":"Episode","title":"Live Ep","description":"live","series":{"title":"Live Show"}}}}}`)
		case r.URL.Query().Get("network") != "":
			fmt.Fprint(w, `<html><script id="__NEXT_DATA__">{"props":{"pageProps":{"groupedSchedule":{"Monday":[{"gmt":"Mon Aug 03 06:04:00 GMT 2026","pacific":"Mon Aug 03 06:04:00 PDT 2026","slotLength":30,"show":{"programTitle":"Widget Show","episode":{"episodeTitle":"Part 1","description":"east desc"}}}]}}}}</script></html>`)
		case strings.Contains(r.URL.Path, "/graphql"):
			fmt.Fprint(w, `{"data":{"getScheduleEntries":[{"scheduledTimestamp":"2026-08-03T10:00:00Z","scheduledDuration":"6300","show":{"title":{"en_US":{"full":"TCM Film"}}},"summary":{"short":"a film"}}]}}`)
		case strings.Contains(r.URL.Path, "/schedule/"):
			fmt.Fprint(w, `<div>{"type":"tv-listings","properties":{"scheduleItems":[{"properties":{"text":{"showTitle":"AMC Show","episodeTitle":"Ep","description":"d","startDate":"2026-08-03","startTime":"6:00PM","endDate":"2026-08-03","endTime":"7:00PM"}}}]}}</div>`)
		case strings.HasSuffix(r.URL.Path, ".jn"):
			fmt.Fprint(w, `{"day":[{"show":[{"title":"Fox News Show","description":"desk","start-utc":"2026-08-03T10:00:00Z","end-utc":"2026-08-03T11:00:00Z"}]}]}`)
		case strings.Contains(r.URL.Path, "by_filters"):
			fmt.Fprint(w, `{"data":{"listings":{"items":[{"title":"FS1 Game","description":"ball","call_sign":"FS1","start_time":"2026-08-03T10:00:00Z","end_time":"2026-08-03T12:00:00Z"}]}}}`)
		case strings.Contains(r.URL.Path, "/token"):
			fmt.Fprint(w, `{"data":{"attributes":{"token":"anon-token-value"}}}`)
		case strings.Contains(r.URL.Path, "/cms/routes/channel/"):
			fmt.Fprint(w, `{"included":[{"type":"video","attributes":{"videoType":"LISTING","description":"Series Title","name":"Episode Name","scheduleStart":"2026-08-03T10:00:00Z","scheduleEnd":"2026-08-03T11:00:00Z"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	svc := NewService(&config.Config{
		TVESources:      []string{SourceAE, SourceAMC, SourceWarner, SourceFox, SourceDiscovery},
		TVECombined:     true,
		TVEStartChannel: 20000,
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
	})
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.refresh(context.Background())

	want := map[string]string{
		SourceAE:        "Ancient Aliens",
		SourceAMC:       "AMC Show",
		SourceWarner:    "Widget Show",
		SourceFox:       "FS1 Game",
		SourceDiscovery: "Series Title",
	}
	for id, title := range want {
		guide, _, ok := svc.Guide(id)
		if !ok || !strings.Contains(string(guide), title) {
			t.Fatalf("%s missing %s ok=%v\n%s", id, title, ok, guide)
		}
	}
	warner, _, _ := svc.Guide(SourceWarner)
	if !strings.Contains(string(warner), "TCM Film") {
		t.Fatalf("tcm missing\n%s", warner)
	}
	fox, _, _ := svc.Guide(SourceFox)
	if !strings.Contains(string(fox), "Fox News Show") {
		t.Fatalf("fox news missing\n%s", fox)
	}
	if strings.Contains(logs.String(), "anon-token-value") || strings.Contains(logs.String(), stmt) {
		t.Fatalf("log leaked guide material\n%s", logs.String())
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != mode {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
}

func readSession(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func snapshot(mu *sync.Mutex, seen *[]string) []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), (*seen)...)
}

func htmlResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}
