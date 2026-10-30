package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/cache"
	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/pluto"
)

type fakeTuner struct {
	token  string
	params string
	gen    int64
	err    error
	calls  int
}

func (f *fakeTuner) EnsureFresh(context.Context) error {
	f.calls++
	return f.err
}

func (f *fakeTuner) Credentials() (string, string, int64) {
	return f.token, f.params, f.gen
}

func newTestServer(t *testing.T, tuners []Tuner, ready bool) *Server {
	t.Helper()
	cfg := &config.Config{Port: 8080, StartChannel: 10000, TunerCount: len(tuners)}
	c := cache.New(len(tuners))
	if ready {
		if err := c.SetAll([]pluto.Channel{{
			ID: "abc", Name: "Alpha", Slug: "alpha", Category: "News", Number: 1,
			ColorLogoPNG: pluto.ImageRef{Path: "http://logo/a.png"},
		}}, []byte("<tv></tv>\n")); err != nil {
			t.Fatal(err)
		}
	}
	return newServer(c, tuners, cfg, "1.2.3")
}

func do(s *Server, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)
	return rr
}

func TestHealthAndMissingRoutes(t *testing.T) {
	s := newTestServer(t, []Tuner{&fakeTuner{token: "jwt", gen: 1}}, false)
	rr := do(s, http.MethodGet, "/health", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("health %d", rr.Code)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health cache %q", rr.Header().Get("Cache-Control"))
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}

	if err := s.cache.SetAll([]pluto.Channel{{ID: "abc", Name: "Alpha"}}, []byte("<tv/>")); err != nil {
		t.Fatal(err)
	}
	rr = do(s, http.MethodGet, "/health", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("ready health: %d %q", rr.Code, rr.Body.String())
	}

	rr = do(s, http.MethodGet, "/favicon.ico", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("favicon %d", rr.Code)
	}
}

func TestPlaylistAndEPG(t *testing.T) {
	fake := &fakeTuner{token: "jwt-token", params: "deviceId=abc", gen: 3}
	s := newTestServer(t, []Tuner{fake}, false)

	rr := do(s, http.MethodGet, "/tuner-1-playlist.m3u", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("early playlist %d", rr.Code)
	}
	if fake.calls != 1 {
		t.Fatalf("EnsureFresh calls %d", fake.calls)
	}

	if err := s.cache.SetAll([]pluto.Channel{{
		ID: "id/a", Name: "Bad\nName", Slug: "alpha", Category: "News",
	}}, []byte("<tv></tv>\n")); err != nil {
		t.Fatal(err)
	}

	rr = do(s, http.MethodGet, "/tuner1/channels.m3u", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("playlist %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if rr.Header().Get("Content-Type") != "application/x-mpegurl" {
		t.Fatal(rr.Header().Get("Content-Type"))
	}
	if !strings.Contains(body, "jwt=jwt-token") || !strings.Contains(body, "/channel/id%2Fa/master.m3u8?") {
		t.Fatalf("playlist body:\n%s", body)
	}
	if strings.Count(body, "\n") != 3 { // header, extinf, url, trailing
		t.Fatalf("newline in name broke the playlist:\n%s", body)
	}
	if rr.Header().Get("Last-Modified") == "" {
		t.Fatal("missing Last-Modified")
	}

	// Cached playlist is served even if generation would rebuild something else.
	_, gen, _ := s.cache.GetChannels()
	if !s.cache.SetM3U(0, 3, gen, "#EXTM3U\ncached\n") {
		t.Fatal("failed to seed cache")
	}
	rr = do(s, http.MethodGet, "/tuner1/m3u", nil)
	if rr.Body.String() != "#EXTM3U\ncached\n" {
		t.Fatalf("cache miss:\n%s", rr.Body.String())
	}

	fake.err = errors.New("boot down")
	rr = do(s, http.MethodGet, "/tuner-1-playlist.m3u", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh failure should still serve playlist: %d", rr.Code)
	}

	rr = do(s, http.MethodGet, "/epg.xml", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "<tv></tv>\n" {
		t.Fatalf("epg %d %q", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "application/xml; charset=utf-8" {
		t.Fatal(rr.Header().Get("Content-Type"))
	}
	rr = do(s, http.MethodGet, "/xmltv.xml", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("xmltv alias %d", rr.Code)
	}
}

func TestVersionNamesTheBuild(t *testing.T) {
	s := newTestServer(t, nil, false)
	rr := do(s, http.MethodGet, "/version", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "version v1.2.3\n") || !strings.Contains(rr.Body.String(), "commit ") {
		t.Fatalf("version %d %q", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache %s", rr.Header().Get("Cache-Control"))
	}
}

func TestStatusAndIndex(t *testing.T) {
	fake := &fakeTuner{token: "t", gen: 1}
	s := newTestServer(t, []Tuner{fake, &fakeTuner{token: "t2", gen: 1}}, false)

	rr := do(s, http.MethodGet, "/", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("index %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Still loading") {
		t.Fatalf("missing warming banner:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ">v1.2.3<") && !strings.Contains(rr.Body.String(), "v1.2.3") {
		t.Fatalf("version badge:\n%s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "vv1.2.3") {
		t.Fatal("version prefix doubled")
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("index cached: %s", rr.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rr.Body.String(), "http://example.com/tuner-1-playlist.m3u") {
		t.Fatalf("missing absolute playlist url:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "http://example.com/epg.xml") {
		t.Fatalf("missing epg url:\n%s", rr.Body.String())
	}

	if err := s.cache.SetAll([]pluto.Channel{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}, []byte("<tv/>")); err != nil {
		t.Fatal(err)
	}
	rr = do(s, http.MethodGet, "/", nil)
	body := rr.Body.String()
	if strings.Contains(body, "Still loading") {
		t.Fatal("banner shown after ready")
	}
	if !strings.Contains(body, "2 channels") {
		t.Fatalf("channel count missing:\n%s", body)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Host = "internal:8080"
	req.Header.Set("X-Forwarded-Proto", "HTTPS,http")
	req.Header.Set("X-Forwarded-Host", "dvr.example:8443")
	rr = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)
	var status map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["epg_url"] != "https://dvr.example:8443/epg.xml" {
		t.Fatalf("epg_url %v", status["epg_url"])
	}
	if status["channels"] != float64(2) || status["version"] != "1.2.3" || status["ready"] != true {
		t.Fatalf("status %#v", status)
	}
	tuners, _ := status["tuners"].([]any)
	if len(tuners) != 2 || tuners[0] != "https://dvr.example:8443/tuner-1-playlist.m3u" {
		t.Fatalf("tuners %#v", tuners)
	}

	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Host = "[::1]:8080"
	rr = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["epg_url"] != "http://[::1]:8080/epg.xml" {
		t.Fatalf("ipv6 url %v", status["epg_url"])
	}
}

func TestDisplayVersionAndBracketHost(t *testing.T) {
	if displayVersion("dev") != "dev" || displayVersion("") != "dev" {
		t.Fatalf("%q %q", displayVersion("dev"), displayVersion(""))
	}
	if displayVersion("v1.2.3") != "v1.2.3" || displayVersion("1.2.3") != "v1.2.3" {
		t.Fatalf("%q %q", displayVersion("v1.2.3"), displayVersion("1.2.3"))
	}
	if bracketHost("[::1]:8080") != "[::1]:8080" || bracketHost("::1") != "[::1]" || bracketHost("localhost:8080") != "localhost:8080" {
		t.Fatalf("%q %q %q", bracketHost("[::1]:8080"), bracketHost("::1"), bracketHost("localhost:8080"))
	}
}

func TestPlaylistWaitsForInitialAuth(t *testing.T) {
	fake := &fakeTuner{token: "jwt-token", params: "deviceId=abc", gen: 1}
	s := newTestServer(t, []Tuner{fake}, true)
	s.HoldAuth()

	rr := do(s, http.MethodGet, "/health", nil)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "authenticating") {
		t.Fatalf("health while signing in: %d %q", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/tuner-1-playlist.m3u", nil)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "authenticating") {
		t.Fatalf("playlist while signing in: %d %q", rr.Code, rr.Body.String())
	}
	if fake.calls != 0 {
		t.Fatalf("EnsureFresh ran during initial auth: %d", fake.calls)
	}
	rr = do(s, http.MethodGet, "/epg.xml", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "<tv></tv>\n" {
		t.Fatalf("epg during auth: %d %q", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/", nil)
	if !strings.Contains(rr.Body.String(), "signing in") || strings.Contains(rr.Body.String(), "Still loading") {
		t.Fatalf("index banner:\n%s", rr.Body.String())
	}

	s.ReleaseAuth()
	rr = do(s, http.MethodGet, "/", nil)
	if strings.Contains(rr.Body.String(), "signing in") || strings.Contains(rr.Body.String(), "Still loading") {
		t.Fatalf("ready index still warming:\n%s", rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/health", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("health after auth: %d %q", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/tuner-1-playlist.m3u", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "jwt=jwt-token") {
		t.Fatalf("playlist after auth: %d %q", rr.Code, rr.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("EnsureFresh calls %d", fake.calls)
	}

	empty := cache.New(1)
	s2 := newServer(empty, []Tuner{fake}, s.cfg, "1.2.3")
	rr = do(s2, http.MethodGet, "/", nil)
	if !strings.Contains(rr.Body.String(), "Still loading the channel guide") {
		t.Fatalf("guide banner:\n%s", rr.Body.String())
	}
}

func TestAccessTokenGate(t *testing.T) {
	fake := &fakeTuner{token: "jwt-token", params: "deviceId=abc", gen: 1}
	s := newTestServer(t, []Tuner{fake}, true)
	s.cfg.AccessToken = "sekret"
	s.cfg.GuideHours = 36
	s.cfg.RefreshEvery = 3 * time.Hour
	s.cfg.BindAddr = "127.0.0.1"

	rr := do(s, http.MethodGet, "/tuner-1-playlist.m3u", nil)
	if rr.Code != http.StatusUnauthorized || fake.calls != 0 {
		t.Fatalf("open playlist %d calls %d", rr.Code, fake.calls)
	}
	rr = do(s, http.MethodGet, "/epg.xml", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("open epg %d", rr.Code)
	}
	rr = do(s, http.MethodGet, "/t/nope/epg.xml", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong path token %d", rr.Code)
	}
	rr = do(s, http.MethodGet, "/health", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("health stays open: %d %q", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/metrics", nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "sekret") {
		t.Fatalf("metrics leaked or failed: %d\n%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "pluto_http_responses_total{handler=\"auth\",code=\"401\"} 3\n") {
		t.Fatalf("auth counter:\n%s", rr.Body.String())
	}

	rr = do(s, http.MethodGet, "/tuner-1-playlist.m3u?token=sekret", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "jwt=jwt-token") {
		t.Fatalf("query token playlist: %d %q", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/epg.xml?token=sekret", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "<tv></tv>\n" {
		t.Fatalf("query token epg: %d %q", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/t/sekret/xmltv.xml", nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "<tv></tv>\n" {
		t.Fatalf("path token epg: %d %q", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/t/sekret/tuner1/channels.m3u", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("path token legacy playlist: %d %s", rr.Code, rr.Body.String())
	}

	rr = do(s, http.MethodGet, "/status", nil)
	var status map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["access_token"] != true || status["auth_ready"] != true || status["guide_hours"] != float64(36) || status["bind_addr"] != "127.0.0.1" {
		t.Fatalf("status knobs %#v", status)
	}
	if status["refresh_every"] != "3h0m0s" {
		t.Fatalf("refresh_every %v", status["refresh_every"])
	}
	if status["epg_url"] != "http://example.com/epg.xml?token=sekret" {
		t.Fatalf("epg_url %v", status["epg_url"])
	}
	tuners, _ := status["tuners"].([]any)
	if len(tuners) != 1 || tuners[0] != "http://example.com/tuner-1-playlist.m3u?token=sekret" {
		t.Fatalf("tuners %#v", tuners)
	}
	rr = do(s, http.MethodGet, "/", nil)
	if !strings.Contains(rr.Body.String(), "http://example.com/epg.xml?token=sekret") {
		t.Fatalf("index missed tokenized epg:\n%s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "Still loading") {
		t.Fatal("ready index showed the guide banner")
	}
}

func TestGzipPlaylistAndEPG(t *testing.T) {
	s := newTestServer(t, []Tuner{&fakeTuner{token: "jwt", gen: 1}}, true)
	plain := do(s, http.MethodGet, "/epg.xml", nil)
	if plain.Header().Get("Content-Encoding") != "" {
		t.Fatal("uncompressed response set Content-Encoding")
	}
	if plain.Header().Get("Content-Length") == "" {
		t.Fatal("plain response missing Content-Length")
	}

	rr := do(s, http.MethodGet, "/epg.xml", map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})
	if rr.Header().Get("Content-Encoding") != "gzip" || rr.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gzip headers: %v", rr.Header())
	}
	if rr.Header().Get("Content-Length") != "" {
		t.Fatalf("gzip Content-Length %q", rr.Header().Get("Content-Length"))
	}
	zr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != plain.Body.String() {
		t.Fatalf("gunzip %q want %q", got, plain.Body.String())
	}

	rr = do(s, http.MethodGet, "/tuner-1-playlist.m3u", map[string]string{"Accept-Encoding": "gzip;q=0"})
	if rr.Header().Get("Content-Encoding") != "" || !strings.Contains(rr.Body.String(), "jwt=jwt") {
		t.Fatalf("q=0 should be plain: enc %q body %q", rr.Header().Get("Content-Encoding"), rr.Body.String())
	}
	plainPlaylist := do(s, http.MethodGet, "/tuner1/m3u", nil).Body.String()
	rr = do(s, http.MethodGet, "/tuner-1-playlist.m3u", map[string]string{"Accept-Encoding": "gzip"})
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip playlist %d %s", rr.Code, rr.Header().Get("Content-Encoding"))
	}
	zr, err = gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != plainPlaylist {
		t.Fatalf("gunzip playlist %q want %q", got, plainPlaylist)
	}

	rr = do(s, http.MethodGet, "/health", map[string]string{"Accept-Encoding": "gzip"})
	if rr.Header().Get("Content-Encoding") != "" || rr.Body.String() != "ok" {
		t.Fatalf("health gzip: %q %q", rr.Header().Get("Content-Encoding"), rr.Body.String())
	}
}

func TestMetricsAndListenAddr(t *testing.T) {
	fake := &fakeTuner{token: "jwt", gen: 1}
	cfg := &config.Config{Port: 9090, BindAddr: "::1", StartChannel: 10000, GuideHours: 24, TunerCount: 1}
	s := newServer(cache.New(1), []Tuner{fake}, cfg, "dev")
	if s.srv.Addr != "[::1]:9090" {
		t.Fatalf("addr %s", s.srv.Addr)
	}
	rr := do(s, http.MethodGet, "/metrics", nil)
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "pluto_ready 0\n") || !strings.Contains(rr.Body.String(), "pluto_auth_ready 1\n") {
		t.Fatalf("gauges:\n%s", rr.Body.String())
	}
	if err := s.cache.SetAll([]pluto.Channel{{ID: "a", Name: "A"}}, []byte("<tv/>")); err != nil {
		t.Fatal(err)
	}
	rr = do(s, http.MethodGet, "/tuner-1-playlist.m3u", nil)
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Code)
	}
	rr = do(s, http.MethodGet, "/metrics", nil)
	if !strings.Contains(rr.Body.String(), "pluto_ready 1\n") {
		t.Fatalf("ready gauge:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "pluto_http_responses_total{handler=\"playlist\",code=\"200\"} 1\n") {
		t.Fatalf("playlist counter:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "pluto_guide_hours 24\n") {
		t.Fatalf("guide hours:\n%s", rr.Body.String())
	}
}
