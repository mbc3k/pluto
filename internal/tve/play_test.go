package tve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

func TestAMCPlaybackUsesAuthzOnce(t *testing.T) {
	const secret = "client-secret-value"
	var authorize atomic.Int32
	var playback atomic.Int32
	srv := httptest.NewServer(playHandler(t, &authorize, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/playback-id/api/v1/livestream/6398764467112" {
			playback.Add(1)
			fmt.Fprint(w, `{"sources":[{"type":"application/x-mpegurl","src":"https://cdn.example/amc.m3u8"}]}`)
			return true
		}
		return false
	}))
	defer srv.Close()
	logs := captureLogs(t)
	svc := newPlayService(t, SourceAMC, secret)
	wirePlay(t, svc, srv)
	ctx := context.Background()
	target, err := svc.OpenStream(ctx, SourceAMC, "amc")
	if err != nil || target.Redirect != "https://cdn.example/amc.m3u8" {
		t.Fatalf("target %#v err %v", target, err)
	}
	if authorize.Load() != 1 || playback.Load() != 1 {
		t.Fatalf("authorize %d playback %d", authorize.Load(), playback.Load())
	}
	target, err = svc.OpenStream(ctx, SourceAMC, "amc")
	if err != nil || target.Redirect != "https://cdn.example/amc.m3u8" || authorize.Load() != 1 || playback.Load() != 1 {
		t.Fatalf("cache target %#v err %v authorize %d playback %d", target, err, authorize.Load(), playback.Load())
	}
	text := logs.String()
	for _, hidden := range []string{secret, "guid-1", "user-1", "short-token-value", "authz-token-value", "device-1"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("log contained %s\n%s", hidden, text)
		}
	}
}

func TestPlaybackReauthorizesAfterForbidden(t *testing.T) {
	var authorize atomic.Int32
	var playback atomic.Int32
	srv := httptest.NewServer(playHandler(t, &authorize, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/playback-id/api/v1/livestream/6398764467112" {
			return false
		}
		if playback.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"expired"}`)
			return true
		}
		fmt.Fprint(w, `{"sources":[{"src":"https://cdn.example/amc.m3u8"}]}`)
		return true
	}))
	defer srv.Close()
	svc := newPlayService(t, SourceAMC, "pw")
	wirePlay(t, svc, srv)
	target, err := svc.OpenStream(context.Background(), SourceAMC, "amc")
	if err != nil || target.Redirect != "https://cdn.example/amc.m3u8" {
		t.Fatalf("target %#v err %v", target, err)
	}
	if authorize.Load() != 2 || playback.Load() != 2 {
		t.Fatalf("authorize %d playback %d", authorize.Load(), playback.Load())
	}
}

func TestAdobeNotAuthorizedNamesTheStep(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/adobe-services/authorize" {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<error><status>notAuthorized</status><message>not entitled</message></error>`)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	svc := newPlayService(t, SourceAMC, "pw")
	wirePlay(t, svc, srv)
	_, err := svc.OpenStream(context.Background(), SourceAMC, "amc")
	if err == nil || !strings.Contains(err.Error(), "adobe authorize") || strings.Contains(err.Error(), "guid-1") {
		t.Fatalf("err %v", err)
	}
	var pe *PlayError
	if !asPlay(err, &pe) || pe.Status != http.StatusForbidden || pe.Retry {
		t.Fatalf("play error %#v", pe)
	}
	if calls.Load() != 1 {
		t.Fatalf("retried a hard denial %d", calls.Load())
	}
}

func TestFoxNewsSignsManifest(t *testing.T) {
	var authorize atomic.Int32
	srv := httptest.NewServer(playHandler(t, &authorize, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/atk/token/generator":
			fmt.Fprint(w, `{"token":"hdnea=exp=1893456000~acl=*"}`)
			return true
		case strings.Contains(r.URL.Path, "FNCHLSv3/master.m3u8"):
			if !strings.Contains(r.URL.RawQuery, "hdnea=") {
				t.Errorf("manifest query %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nvideo.m3u8\n")
			return true
		default:
			return false
		}
	}))
	defer srv.Close()
	svc := newPlayService(t, SourceFox, "pw")
	wirePlay(t, svc, srv)
	target, err := svc.OpenStream(context.Background(), SourceFox, "fox_news")
	if err != nil || !strings.Contains(target.Redirect, "FNCHLSv3/master.m3u8?hdnea=") {
		t.Fatalf("target %#v err %v", target, err)
	}
	if authorize.Load() != 1 {
		t.Fatalf("authorize %d", authorize.Load())
	}
}

func TestWarnerAndDiscoveryAndFoxSports(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-time.Hour).Format(time.RFC3339)
	stop := now.Add(time.Hour).Format(time.RFC3339)
	var authorize atomic.Int32
	srv := httptest.NewServer(playHandler(t, &authorize, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.Contains(r.URL.Path, "/watchtnt/"):
			fmt.Fprint(w, `<script type="application/json" data-drupal-selector="drupal-settings-json">{"top2":{"serviceAppId":"app-1","auth":{"authBrand":"TNT"}}}</script>`)
			return true
		case r.URL.Path == "/token/token_ngtv":
			fmt.Fprint(w, `{"auth":{"ngtv_token":"ngtv-1"}}`)
			return true
		case strings.Contains(r.URL.Path, "/v2/media/tnt-east/desktop"):
			fmt.Fprint(w, `{"media":{"desktop":{"widevine":{"cenc":{"url":"https://cdn.example/tnt-cenc.m3u8"}},"hls":{"url":"https://cdn.example/tnt.m3u8"}}}}`)
			return true
		case r.URL.Path == "/token":
			w.Header().Add("Set-Cookie", "st=disco-session; Path=/; HttpOnly")
			fmt.Fprint(w, `{"data":{"attributes":{"token":"anon"}}}`)
			return true
		case r.URL.Path == "/playback/v3/channelPlaybackInfo":
			if !strings.Contains(r.Header.Get("Cookie"), "st=disco-session") {
				t.Error("playback missing discovery session cookie")
			}
			fmt.Fprint(w, `{"data":{"attributes":{"streaming":[{"type":"hls","url":"https://cdn.example/tlc.m3u8","protection":{"drmEnabled":false}}]}}}`)
			return true
		case strings.Contains(r.URL.Path, "/sporting/"):
			fmt.Fprintf(w, `{"data":{"listings":{"items":[{"call_sign":"FS1","entity_id":"stream-1","airing_type":"live","title":"Live","start_time":%q,"end_time":%q}]}}}`, start, stop)
			return true
		case r.URL.Path == "/v2.0/login":
			fmt.Fprint(w, `{"accessToken":"anon-token"}`)
			return true
		case r.URL.Path == "/v2.0/checkadobeauthn/v2":
			fmt.Fprint(w, `{"accessToken":"DTV-fox-token"}`)
			return true
		case r.URL.Path == "/sports/v3.0/partner-playback/live":
			fmt.Fprint(w, `{"playbackUrl":"https://cdn.example/fs1.m3u8"}`)
			return true
		default:
			return false
		}
	}))
	defer srv.Close()

	svc := newPlayService(t, SourceWarner, "pw")
	wirePlay(t, svc, srv)
	target, err := svc.OpenStream(context.Background(), SourceWarner, "tnt-east")
	if err != nil || target.Redirect != "https://cdn.example/tnt.m3u8" {
		t.Fatalf("warner %#v %v", target, err)
	}

	svc = newPlayService(t, SourceDiscovery, "pw")
	wirePlay(t, svc, srv)
	target, err = svc.OpenStream(context.Background(), SourceDiscovery, "tlc")
	if err != nil || target.Redirect != "https://cdn.example/tlc.m3u8" {
		t.Fatalf("discovery %#v %v", target, err)
	}

	svc = newPlayService(t, SourceFox, "pw")
	wirePlay(t, svc, srv)
	target, err = svc.OpenStream(context.Background(), SourceFox, "fox_sports_fs1")
	if err != nil || target.Redirect != "https://cdn.example/fs1.m3u8" {
		t.Fatalf("fox sports %#v %v", target, err)
	}
}

func TestFoxNewsRefererFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/adobe-services/authorize":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<error><status>notAuthorized</status></error>`)
		case strings.Contains(r.URL.Path, "FNCHLSv3/master.m3u8"):
			if r.Header.Get("Referer") == "" {
				t.Error("missing referer")
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\nseg.m3u8\n")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	svc := newPlayService(t, SourceFox, "pw")
	wirePlay(t, svc, srv)
	target, err := svc.OpenStream(context.Background(), SourceFox, "fox_news")
	if err != nil || !strings.Contains(string(target.Body), "https://247.foxnews.com/hls/live/2003586/FNCHLSv3/seg.m3u8") {
		t.Fatalf("body %s err %v", target.Body, err)
	}
}

func TestAMCPrefersDecisionToken(t *testing.T) {
	var shortCalls atomic.Int32
	var sawDecision atomic.Bool
	srv := httptest.NewServer(playHandler(t, new(atomic.Int32), func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/api/v2/AMC/decisions/authorize/DTV":
			fmt.Fprint(w, `{"decisions":[{"resource":"AMC","authorized":true,"token":{"serializedToken":"decision-token-value"}}]}`)
			return true
		case r.URL.Path == "/adobe-services/shortAuthorize":
			shortCalls.Add(1)
			return false
		case r.URL.Path == "/playback-id/api/v1/livestream/6398764467112":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if json.Unmarshal(raw, &body) != nil || body["adobeShortMediaToken"] != "decision-token-value" {
				t.Errorf("playback body %s", raw)
			}
			sawDecision.Store(true)
			fmt.Fprint(w, `{"sources":[{"src":"https://cdn.example/amc.m3u8"}]}`)
			return true
		default:
			return false
		}
	}))
	defer srv.Close()
	svc := newPlayService(t, SourceAMC, "pw")
	wirePlay(t, svc, srv)
	target, err := svc.OpenStream(context.Background(), SourceAMC, "amc")
	if err != nil || target.Redirect != "https://cdn.example/amc.m3u8" || !sawDecision.Load() || shortCalls.Load() != 0 {
		t.Fatalf("target %#v err %v short %d", target, err, shortCalls.Load())
	}
}

func TestTCMDecodesDecisionToken(t *testing.T) {
	xmlToken := `<authzToken>tcm-media</authzToken>`
	encoded := base64.StdEncoding.EncodeToString([]byte(xmlToken))
	var saw atomic.Bool
	srv := httptest.NewServer(playHandler(t, new(atomic.Int32), func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.Contains(r.URL.Path, "/watchtcm/"):
			fmt.Fprint(w, `<script id="__NEXT_DATA__" type="application/json">{"props":{"mediaAppId":"tcm-app","authBrand":"TCM"}}</script>`)
			return true
		case r.URL.Path == "/api/v2/TCM/decisions/authorize/DTV":
			fmt.Fprintf(w, `{"decisions":[{"resource":"TCM","authorized":true,"token":{"serializedToken":%q}}]}`, encoded)
			return true
		case r.URL.Path == "/token/token_ngtv":
			if r.Method != http.MethodPost || !strings.Contains(r.URL.RawQuery, "accessToken="+url.QueryEscape(xmlToken)) {
				t.Errorf("tcm token method %s query %s", r.Method, r.URL.RawQuery)
			}
			saw.Store(true)
			fmt.Fprint(w, `{"auth":{"ngtv_token":"ngtv-1"}}`)
			return true
		case strings.Contains(r.URL.Path, "/v2/media/tcm-east/desktop"):
			fmt.Fprint(w, `{"media":{"url":"https://cdn.example/tcm.m3u8"}}`)
			return true
		default:
			return false
		}
	}))
	defer srv.Close()
	svc := newPlayService(t, SourceWarner, "pw")
	wirePlay(t, svc, srv)
	target, err := svc.OpenStream(context.Background(), SourceWarner, "tcm-east")
	if err != nil || target.Redirect != "https://cdn.example/tcm.m3u8" || !saw.Load() {
		t.Fatalf("target %#v err %v", target, err)
	}
}

func TestFoxSportsRejectsAnonymousDevice(t *testing.T) {
	anon := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"mvpdid":"TempPass"}`)) + ".sig"
	var playback atomic.Int32
	srv := httptest.NewServer(playHandler(t, new(atomic.Int32), func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.Contains(r.URL.Path, "/sporting/"):
			now := time.Now().UTC()
			fmt.Fprintf(w, `{"data":{"listings":{"items":[{"call_sign":"FS1","entity_id":"stream-1","airing_type":"live","start_time":%q,"end_time":%q}]}}}`, now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))
			return true
		case r.URL.Path == "/v2.0/login":
			fmt.Fprint(w, `{"accessToken":"anon-token"}`)
			return true
		case r.URL.Path == "/v2.0/checkadobeauthn/v2":
			fmt.Fprintf(w, `{"accessToken":%q}`, anon)
			return true
		case r.URL.Path == "/sports/v3.0/partner-playback/live":
			playback.Add(1)
			return true
		default:
			return false
		}
	}))
	defer srv.Close()
	svc := newPlayService(t, SourceFox, "pw")
	wirePlay(t, svc, srv)
	_, err := svc.OpenStream(context.Background(), SourceFox, "fox_sports_fs1")
	if err == nil || !strings.Contains(err.Error(), "fox sports checkadobeauthn") || strings.Contains(err.Error(), anon) || playback.Load() != 0 {
		t.Fatalf("err %v playback %d", err, playback.Load())
	}
}

func TestMissingSignInDoesNotCallAdobe(t *testing.T) {
	svc := NewService(&config.Config{
		TVESources:     []string{SourceAMC},
		TVESessionFile: filepath.Join(t.TempDir(), "session.json"),
	})
	svc.client = offlineClient(t)
	_, err := svc.OpenStream(context.Background(), SourceAMC, "amc")
	if err == nil || !strings.Contains(err.Error(), "directv sign-in") || !strings.Contains(err.Error(), "DirecTV") {
		t.Fatalf("%v", err)
	}
}

func TestAbsolutizePlaylist(t *testing.T) {
	body := absolutize("https://cdn.example/live/master.m3u8", []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\nvideo.m3u8\n"))
	text := string(body)
	if !strings.Contains(text, `URI="https://cdn.example/live/key"`) || !strings.Contains(text, "https://cdn.example/live/video.m3u8") {
		t.Fatalf("%s", text)
	}
}

func playHandler(t *testing.T, authorize *atomic.Int32, extra func(http.ResponseWriter, *http.Request) bool) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v2/") {
			if extra != nil && extra(w, r) {
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"no decision"}`)
			return
		}
		switch r.URL.Path {
		case "/adobe-services/authorize":
			authorize.Add(1)
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<root><authzToken>authz-token-value</authzToken></root>`)
		case "/adobe-services/shortAuthorize":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<shortMediaToken>short-token-value</shortMediaToken>`)
		case "/auth-orchestration-id/api/v1/unauth", "/auth-orchestration-id/api/v1/refresh", "/auth-orchestration-id/api/v1/mvpd-auth":
			fmt.Fprint(w, `{"data":{"access_token":"amc-bearer"}}`)
		default:
			if extra != nil && extra(w, r) {
				return
			}
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func newPlayService(t *testing.T, source, pass string) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{
		Signed:        true,
		Method:        "browser",
		Expires:       time.Date(2027, 1, 20, 15, 4, 5, 0, time.UTC),
		AuthnToken:    `<authnToken><simpleTokenExpires>2027/01/20 15:04:05 GMT</simpleTokenExpires><simpleTokenMsoID>DTV</simpleTokenMsoID><simpleSamlNameID>user-1</simpleSamlNameID><simpleSamlSessionIndex>idx-1</simpleSamlSessionIndex><simpleTokenAuthenticationGuid>guid-1</simpleTokenAuthenticationGuid></authnToken>`,
		ClientID:      "client-id",
		ClientSecret:  "client-secret-value",
		AccessToken:   "access-token-value",
		AccessExpires: time.Now().Add(24 * time.Hour).UTC(),
		DeviceID:      "device-1",
	}); err != nil {
		t.Fatal(err)
	}
	return NewService(&config.Config{
		TVESources:      []string{source},
		TVESessionFile:  path,
		DirectvPassword: pass,
		RefreshEvery:    time.Hour,
	})
}

func wirePlay(t *testing.T, svc *Service, srv *httptest.Server) {
	t.Helper()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.adobeBase = srv.URL
}

func asPlay(err error, pe **PlayError) bool {
	got, ok := err.(*PlayError)
	if ok {
		*pe = got
	}
	return ok
}
