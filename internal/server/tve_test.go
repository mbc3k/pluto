package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/cache"
	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/tve"
)

func TestTVERoutesGateAndFeeds(t *testing.T) {
	const token = "sekret"
	const pass = "do-not-leak-me"
	dir := t.TempDir()
	session := filepath.Join(dir, "session.json")
	raw, err := json.Marshal(map[string]any{
		"pending": &tve.Pending{
			Prompt:         "Enter the code sent to your phone",
			Kind:           "OneTimePasswordCallback",
			PageURL:        "https://identity.directv.com/weblogin/authorize?code_challenge=abc",
			Callback:       []byte(`{"callbacks":[{"type":"OneTimePasswordCallback"}]}`),
			AuthorizeQuery: url.Values{"code_challenge": {"abc"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Port:            8080,
		StartChannel:    10000,
		TunerCount:      1,
		TVESources:      []string{"fox", "aenetworks"},
		TVEProvider:     "dtv",
		TVECombined:     true,
		TVEStartChannel: 20000,
		TVESessionFile:  session,
		AccessToken:     token,
		DirectvPassword: pass,
		DirectvUsername: "viewer@example.com",
	}
	s := newServer(cache.New(1), []Tuner{&fakeTuner{token: "jwt", gen: 1}}, cfg, "1.2.3")

	if rr := do(s, http.MethodGet, "/tve/fox.m3u", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("open playlist %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/health", nil); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("health should stay open, got %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/metrics", nil); rr.Code == http.StatusUnauthorized {
		t.Fatal("metrics should stay open")
	}
	if rr := do(s, http.MethodGet, "/tuner-1-playlist.m3u", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("pluto playlist without token %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/epg.xml", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("epg without token %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/t/"+token+"/tuner-1-playlist.m3u", nil); rr.Code == http.StatusUnauthorized {
		t.Fatal("a valid token prefix on a pluto path must pass the gate")
	}
	if rr := do(s, http.MethodGet, "/t/"+token+"/tve/fox.m3u", nil); rr.Code != http.StatusOK {
		t.Fatalf("path-token fox playlist %d\n%s", rr.Code, rr.Body.String())
	}
	if rr := do(s, http.MethodGet, "/tve/fox.m3u?token=wrong", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad query token %d", rr.Code)
	}

	rr := do(s, http.MethodGet, "/tve/fox.m3u?token="+token, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/tve/fox/fox_news/stream") || !strings.Contains(rr.Body.String(), "tvg-chno=20000") {
		t.Fatalf("fox playlist %d\n%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "http://example.com/t/"+token+"/tve/fox/fox_sports_fs1/stream") {
		t.Fatalf("local stream dropped the access token:\n%s", rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "application/x-mpegurl" {
		t.Fatal(rr.Header().Get("Content-Type"))
	}
	rr = do(s, http.MethodGet, "/t/"+token+"/tve/all.m3u", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "History") || !strings.Contains(rr.Body.String(), "Fox News") {
		t.Fatalf("combined %d\n%s", rr.Code, rr.Body.String())
	}
	if rr := do(s, http.MethodGet, "/t/wrong/tve/fox.m3u", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad path token %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/tve/nope.m3u?token="+token, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown source %d", rr.Code)
	}
	rr = do(s, http.MethodGet, "/tve/fox.xml?token="+token, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `id="fox_news"`) || !strings.Contains(rr.Body.String(), "pluto-tve") {
		t.Fatalf("guide %d\n%s", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/tve/fox/fox_sports_fs1/stream?token="+token, nil)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "DirecTV") || strings.Contains(rr.Body.String(), pass) {
		t.Fatalf("stream %d %q", rr.Code, rr.Body.String())
	}
	if rr := do(s, http.MethodGet, "/tve/fox/fox_news/stream?token="+token, nil); rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "directv sign-in") || strings.Contains(rr.Body.String(), pass) {
		t.Fatalf("fox news stream %d %q", rr.Code, rr.Body.String())
	}

	rr = do(s, http.MethodGet, "/tve/directv?token="+token, nil)
	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, "Enter the code sent to your phone") || !strings.Contains(body, "/tve/directv/code?token="+token) {
		t.Fatalf("sign-in %d\n%s", rr.Code, body)
	}
	if strings.Contains(body, pass) || strings.Contains(body, "viewer@example.com") || strings.Contains(body, "OneTimePasswordCallback") {
		t.Fatalf("sign-in leaked session material:\n%s", body)
	}
	if rr := do(s, http.MethodGet, "/tve/directv", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("open sign-in %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/tve/directv/code?token="+token, nil); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get code %d", rr.Code)
	}

	rr = do(s, http.MethodGet, "/", nil)
	page := rr.Body.String()
	if !strings.Contains(page, "TV Everywhere") || !strings.Contains(page, "http://example.com/tve/fox.m3u?token="+token) {
		t.Fatalf("index missing feeds:\n%s", page)
	}
	if !strings.Contains(page, "Enter the code sent to your phone") {
		t.Fatalf("index missing the sign-in prompt:\n%s", page)
	}
	if strings.Contains(page, pass) {
		t.Fatalf("index leaked a secret:\n%s", page)
	}
	if !strings.Contains(page, "http://example.com/tuner-1-playlist.m3u?token="+token) {
		t.Fatalf("index should tokenize the pluto playlist:\n%s", page)
	}
	if !strings.Contains(page, "Still loading") {
		t.Fatal("pluto warming banner should stay")
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Host = "dvr.example:8080"
	rr = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), pass) || strings.Contains(rr.Body.String(), "viewer@example.com") {
		t.Fatalf("status leaked credentials:\n%s", rr.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	tveStatus, _ := status["tve"].(map[string]any)
	if tveStatus["provider"] != "dtv" || tveStatus["pending"] != true || tveStatus["signed_in"] != false {
		t.Fatalf("tve status %#v", tveStatus)
	}
	if tveStatus["prompt"] != "Enter the code sent to your phone" {
		t.Fatalf("prompt %#v", tveStatus["prompt"])
	}
	if tveStatus["combined_playlist"] != "http://dvr.example:8080/tve/all.m3u?token="+token {
		t.Fatalf("combined %#v", tveStatus["combined_playlist"])
	}
	sources, _ := tveStatus["sources"].([]any)
	if len(sources) != 2 {
		t.Fatalf("sources %#v", sources)
	}
}

func TestTVECodeWithoutPending(t *testing.T) {
	const pass = "do-not-leak-me"
	cfg := &config.Config{
		Port:            8080,
		TVESources:      []string{"fox"},
		TVEProvider:     "dtv",
		TVECombined:     false,
		TVEStartChannel: 20000,
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
		AccessToken:     "sekret",
		DirectvPassword: pass,
	}
	s := newServer(cache.New(0), nil, cfg, "dev")
	req := httptest.NewRequest(http.MethodPost, "/tve/directv/code?token=sekret", strings.NewReader("code=123456"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || strings.Contains(rr.Body.String(), pass) || strings.Contains(rr.Body.String(), "123456") {
		t.Fatalf("%d %q", rr.Code, rr.Body.String())
	}
	if rr := do(s, http.MethodGet, "/tve/all.m3u?token=sekret", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("combined should be off, got %d", rr.Code)
	}
}

func TestTVEBrowserSignInPageAndStatus(t *testing.T) {
	const token = "sekret"
	const code = "REGCODE99"
	const activateURL = "https://sp.auth.adobe.com/adobe-services/authenticate/saml?reg_code=" + code
	cfg := &config.Config{
		Port:            8080,
		TVESources:      []string{"fox"},
		TVEProvider:     "dtv",
		TVECombined:     false,
		TVEStartChannel: 20000,
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
		AccessToken:     token,
	}
	s := newServer(cache.New(0), nil, cfg, "dev")
	fake := &fakeFeeds{view: tve.SignInView{
		Waiting:       true,
		ActivationURL: activateURL,
		Code:          code,
		Expires:       time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC),
		Notice:        "Open the activation link in Safari or Firefox and sign in to DirecTV there.",
	}}
	s.tve = fake

	if rr := do(s, http.MethodGet, "/tve/directv/activate", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("open activate %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/tve/directv/import", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("open import %d", rr.Code)
	}
	if rr := do(s, http.MethodGet, "/tve/directv/activate?token="+token, nil); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get activate %d", rr.Code)
	}

	rr := do(s, http.MethodGet, "/tve/directv?token="+token, nil)
	body := rr.Body.String()
	if rr.Code != http.StatusOK || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page %d %s", rr.Code, rr.Header().Get("Cache-Control"))
	}
	if !strings.Contains(body, code) || !strings.Contains(body, activateURL) || !strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, "/tve/directv/activate?token="+token) || !strings.Contains(body, "/tve/directv/import?token="+token) {
		t.Fatalf("sign-in page\n%s", body)
	}
	if fake.polled == 0 {
		t.Fatal("sign-in page did not poll")
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Host = "dvr.example:8080"
	rr = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), code) || strings.Contains(rr.Body.String(), "authenticate/saml") {
		t.Fatalf("status leaked the activation\n%s", rr.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	tveStatus, _ := status["tve"].(map[string]any)
	if tveStatus["activation"] != true || tveStatus["expires"] != "2026-10-22 01:00:00Z" || tveStatus["signed_in"] != false {
		t.Fatalf("tve status %#v", tveStatus)
	}

	rr = do(s, http.MethodGet, "/", nil)
	if strings.Contains(rr.Body.String(), code) || strings.Contains(rr.Body.String(), "authenticate/saml") {
		t.Fatalf("index leaked the activation\n%s", rr.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, "/tve/directv/activate?token="+token, nil)
	rr = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, post)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/tve/directv?token="+token || fake.began != 1 {
		t.Fatalf("activate %d %s began=%d", rr.Code, rr.Header().Get("Location"), fake.began)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("token", "pasted-authn-token"); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("cookies", "cookies.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("cookie-export")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	post = httptest.NewRequest(http.MethodPost, "/tve/directv/import?token="+token, &buf)
	post.Header.Set("Content-Type", w.FormDataContentType())
	rr = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, post)
	if rr.Code != http.StatusSeeOther || !strings.Contains(fake.imported, "pasted-authn-token") || !strings.Contains(fake.imported, "cookie-export") || strings.Contains(rr.Header().Get("Location"), "pasted-authn-token") {
		t.Fatalf("import %d loc=%s imported=%q", rr.Code, rr.Header().Get("Location"), fake.imported)
	}
}

type fakeFeeds struct {
	view     tve.SignInView
	imported string
	began    int
	polled   int
	notice   string
}

func (f *fakeFeeds) List() []tve.Link {
	return []tve.Link{{ID: "fox", Name: "Fox", Channels: 1}}
}
func (f *fakeFeeds) Playlist(id, base string) (string, bool) {
	return "", false
}
func (f *fakeFeeds) Guide(id string) ([]byte, time.Time, bool) {
	return nil, time.Time{}, false
}
func (f *fakeFeeds) SignIn() tve.SignInView {
	v := f.view
	if f.notice != "" {
		v.Notice = f.notice
	}
	return v
}
func (f *fakeFeeds) SubmitCode(context.Context, string) error { return nil }
func (f *fakeFeeds) OpenStream(context.Context, string, string) (tve.StreamTarget, error) {
	return tve.StreamTarget{}, tve.ErrUnknownStream
}
func (f *fakeFeeds) PollActivation(context.Context) { f.polled++ }
func (f *fakeFeeds) BeginBrowserSignIn(context.Context) error {
	f.began++
	return nil
}
func (f *fakeFeeds) ImportSession(raw string) error {
	f.imported = raw
	return nil
}
func (f *fakeFeeds) SetNotice(msg string) { f.notice = msg }
