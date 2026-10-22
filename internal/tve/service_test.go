package tve

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mbc3k/pluto/internal/config"
)

func TestRefreshParsesGuidesAndKeepsPrevious(t *testing.T) {
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/schedule/data/"):
			fmt.Fprint(w, `[{"timeslotId":"slot-1","startTime":1700000000000,"endTime":1700003600000,"series":{"title":"Ancient Aliens"},"program":{"title":"The Episode","description":"A & B"}}]`)
		case r.Method == http.MethodPost && r.URL.Query().Get("brand") != "":
			fmt.Fprint(w, `{"data":{"nowPlaying":{"startTime":1700010000,"endTime":1700013600,"program":{"__typename":"Episode","title":"Live Ep","description":"live","series":{"title":"Live Show"}}}}}`)
		case r.URL.Query().Get("network") != "":
			fmt.Fprint(w, `{"day":[{"gmt":"Mon Aug 03 06:04:00 GMT 2026","pacific":"Mon Aug 03 06:04:00 PDT 2026","slotLength":30,"show":{"programTitle":"TNT Show","episode":{"episodeTitle":"Part 1","description":"east desc"}}}]}`)
		case strings.Contains(r.URL.Path, "/graphql"):
			fmt.Fprint(w, `{"data":{"getScheduleEntries":[]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(&config.Config{
		TVESources:      []string{SourceAE, SourceWarner},
		TVECombined:     true,
		TVEStartChannel: 20000,
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
		RefreshEvery:    time.Hour,
	})
	svc.client = &http.Client{Transport: hostRewrite{base: base}, Timeout: 5 * time.Second}
	svc.refresh(context.Background())

	if view := svc.SignIn(); !strings.Contains(view.Notice, "not set") {
		t.Fatalf("notice %q", view.Notice)
	}
	guide, _, ok := svc.Guide(SourceAE)
	if !ok || !strings.Contains(string(guide), "Ancient Aliens") || !strings.Contains(string(guide), "Live Show") {
		t.Fatalf("ae guide ok=%v\n%s", ok, guide)
	}
	warner, _, ok := svc.Guide(SourceWarner)
	if !ok || !strings.Contains(string(warner), "TNT Show") {
		t.Fatalf("warner guide ok=%v\n%s", ok, warner)
	}
	if !strings.Contains(string(warner), "20260803060400") || !strings.Contains(string(warner), "20260803130400") {
		if _, err := time.LoadLocation("America/Los_Angeles"); err != nil {
			t.Skip("timezone data is not installed")
		}
		t.Fatalf("east/west times missing:\n%s", warner)
	}
	pl, ok := svc.Playlist(SourceAE, "http://localhost:8080")
	if !ok || !strings.Contains(pl, "tvg-chno=20000") || !strings.Contains(pl, "https://dai.google.com/linear/hls/event/3pCfCAVSTz24VQ7jZDXLzw/master.m3u8") {
		t.Fatalf("ae playlist ok=%v\n%s", ok, pl)
	}
	wpl, ok := svc.Playlist(SourceWarner, "http://localhost:8080")
	if !ok || !strings.Contains(wpl, "tvg-chno=20004") || !strings.Contains(wpl, "http://localhost:8080/tve/warner/tnt-east/stream") {
		t.Fatalf("warner playlist ok=%v\n%s", ok, wpl)
	}
	all, ok := svc.Playlist(combinedSourceID, "http://localhost:8080")
	if !ok || !strings.Contains(all, "History") || !strings.Contains(all, "TNT (East)") {
		t.Fatalf("combined ok=%v\n%s", ok, all)
	}

	fail.Store(true)
	svc.refresh(context.Background())
	guide, _, ok = svc.Guide(SourceAE)
	if !ok || !strings.Contains(string(guide), "Ancient Aliens") {
		t.Fatalf("failed refresh wiped the guide ok=%v\n%s", ok, guide)
	}
}

func TestConcurrentRefreshAndPlaylist(t *testing.T) {
	svc := NewService(&config.Config{
		TVESources:      []string{SourceFox},
		TVECombined:     true,
		TVEStartChannel: 20000,
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
	})
	svc.client = offlineClient(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.refresh(context.Background())
			if _, ok := svc.Playlist(SourceFox, "http://h"); !ok {
				t.Error("missing playlist")
			}
			if _, _, ok := svc.Guide(combinedSourceID); !ok {
				t.Error("missing guide")
			}
			_ = svc.List()
			_ = svc.SignIn()
		}()
	}
	wg.Wait()
}

func TestLineupWithoutCredentials(t *testing.T) {
	svc := NewService(&config.Config{
		TVESources:      []string{SourceFox, SourceDiscovery},
		TVECombined:     true,
		TVEStartChannel: 20000,
		TVESessionFile:  filepath.Join(t.TempDir(), "session.json"),
	})
	svc.client = offlineClient(t)
	svc.refresh(context.Background())
	pl, ok := svc.Playlist(combinedSourceID, "http://h")
	if !ok {
		t.Fatal("missing combined playlist")
	}
	if !strings.Contains(pl, "https://247.foxnews.com/hls/live/2003586/FNCHLSv3/master.m3u8") {
		t.Fatalf("missing public fox news url:\n%s", pl)
	}
	if !strings.Contains(pl, "http://h/tve/fox/fox_sports_fs1/stream") {
		t.Fatalf("missing local sports url:\n%s", pl)
	}
	msg, ok := svc.StreamStatus(SourceFox, "fox_sports_fs1")
	if !ok || !strings.Contains(msg, "DirecTV") {
		t.Fatalf("stream status %v %q", ok, msg)
	}
	if _, ok := svc.StreamStatus(SourceFox, "fox_news"); ok {
		t.Fatal("a public manifest should not be answered by the local stream route")
	}
	xml, _, ok := svc.Guide(SourceDiscovery)
	if !ok || !strings.Contains(string(xml), `id="hgtv"`) {
		t.Fatalf("discovery guide ok=%v\n%s", ok, xml)
	}
	svc.combined = false
	if _, ok := svc.Playlist(combinedSourceID, "http://h"); ok {
		t.Fatal("combined playlist should be off")
	}
}

func TestPendingSignInIsNotRestarted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{Pending: &Pending{
		Prompt:   "Enter code",
		Kind:     "OneTimePasswordCallback",
		Callback: []byte(`{"callbacks":[]}`),
	}}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(&config.Config{
		TVESources:      []string{SourceFox},
		TVESessionFile:  path,
		DirectvUsername: "viewer@example.com",
		DirectvPassword: "s3cret-password",
	})
	svc.client = offlineClient(t)
	svc.refresh(context.Background())
	view := svc.SignIn()
	if !view.Pending || view.Prompt != "Enter code" || view.Signed {
		t.Fatalf("%+v", view)
	}
	if strings.Contains(view.Notice, "s3cret-password") || strings.Contains(view.Prompt, "s3cret") {
		t.Fatalf("view leaked a secret: %+v", view)
	}
}

func TestStoredSignInIsReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{Signed: true, FinalURL: "https://sp.auth.adobe.com/done"}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(&config.Config{
		TVESources:      []string{SourceAMC},
		TVESessionFile:  path,
		DirectvUsername: "viewer@example.com",
		DirectvPassword: "s3cret-password",
	})
	svc.client = offlineClient(t)
	svc.refresh(context.Background())
	view := svc.SignIn()
	if !view.Signed || view.Pending {
		t.Fatalf("%+v", view)
	}
}

type hostRewrite struct{ base *url.URL }

func (h hostRewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = h.base.Scheme
	req.URL.Host = h.base.Host
	req.Host = h.base.Host
	return http.DefaultTransport.RoundTrip(req)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func offlineClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "adobe") || strings.Contains(r.URL.Host, "directv") || strings.Contains(r.URL.Host, "dtvce") || strings.Contains(r.URL.Path, "/api/v2/") {
			t.Errorf("sign-in request %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("<html>offline</html>")),
			Request:    r,
		}, nil
	}), Timeout: 5 * time.Second}
}
