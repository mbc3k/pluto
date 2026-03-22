package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mike/pluto/internal/cache"
	"github.com/mike/pluto/internal/config"
)

// Server is the HTTP server for serving playlists and EPG data.
type Server struct {
	srv     *http.Server
	cache   *cache.Cache
	cfg     *config.Config
	version string
}

// New creates a Server wired to the given cache and config.
func New(c *cache.Cache, cfg *config.Config, version string) *Server {
	s := &Server{cache: c, cfg: cfg, version: version}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/epg.xml", s.handleEPG)
	mux.HandleFunc("/xmltv.xml", s.handleEPG)

	// Register tuner endpoints for each configured tuner (1-indexed for users).
	for n := 1; n <= cfg.TunerCount; n++ {
		idx := n - 1 // 0-indexed for cache
		mux.HandleFunc(fmt.Sprintf("/tuner%d/channels.m3u", n), s.makeM3UHandler(idx))
		mux.HandleFunc(fmt.Sprintf("/tuner%d/m3u", n), s.makeM3UHandler(idx))
	}

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// ListenAndServe starts the HTTP server. It returns http.ErrServerClosed
// after Shutdown is called.
func (s *Server) ListenAndServe() error {
	slog.Info("HTTP server listening", "addr", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Shutdown gracefully drains in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// makeM3UHandler returns a handler that serves the M3U for the given cache index.
func (s *Server) makeM3UHandler(idx int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, ok := s.cache.GetM3U(idx)
		if !ok {
			http.Error(w, "playlist not yet available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		fmt.Fprint(w, data)
	}
}

func (s *Server) handleEPG(w http.ResponseWriter, r *http.Request) {
	data, ok := s.cache.GetXMLTV()
	if !ok {
		http.Error(w, "EPG not yet available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !s.cache.IsReady() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	updated := s.cache.LastUpdated()

	tuners := make([]string, s.cfg.TunerCount)
	for i := 1; i <= s.cfg.TunerCount; i++ {
		host := r.Host
		if host == "" {
			host = "localhost:" + strconv.Itoa(s.cfg.Port)
		}
		tuners[i-1] = fmt.Sprintf("http://%s/tuner%d/channels.m3u", host, i)
	}

	epgURL := ""
	if host := r.Host; host != "" {
		epgURL = fmt.Sprintf("http://%s/epg.xml", host)
	} else {
		epgURL = fmt.Sprintf("http://localhost:%d/epg.xml", s.cfg.Port)
	}

	status := map[string]any{
		"version":      s.version,
		"ready":        s.cache.IsReady(),
		"last_updated": formatTime(updated),
		"tuner_count":  s.cfg.TunerCount,
		"start_channel": s.cfg.StartChannel,
		"tuners":       tuners,
		"epg_url":      epgURL,
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(status)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(strings.Replace(time.RFC3339, "T", " ", 1))
}

var indexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pluto for Channels</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
         background: #0f1117; color: #e2e8f0; margin: 0; padding: 2rem; }
  h1   { color: #fff; margin-bottom: 0.25rem; }
  .sub { color: #94a3b8; margin-top: 0; margin-bottom: 2rem; }
  .card { background: #1e2330; border-radius: 8px; padding: 1.25rem 1.5rem;
          margin-bottom: 1.25rem; }
  .card h2 { margin: 0 0 1rem; font-size: 0.85rem; text-transform: uppercase;
              letter-spacing: 0.08em; color: #64748b; }
  .badge { display: inline-block; padding: 0.2rem 0.55rem; border-radius: 4px;
           font-size: 0.8rem; font-weight: 600; }
  .badge.ready  { background: #14532d; color: #86efac; }
  .badge.wait   { background: #451a03; color: #fdba74; }
  table { border-collapse: collapse; width: 100%; }
  td    { padding: 0.4rem 0; vertical-align: top; }
  td:first-child { color: #94a3b8; width: 9rem; }
  a  { color: #60a5fa; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .tuner-list { display: flex; flex-direction: column; gap: 0.4rem; }
</style>
</head>
<body>
<h1>Pluto for Channels</h1>
<p class="sub">Pluto TV &rarr; Channels DVR bridge &nbsp;&bull;&nbsp; v{{.Version}}</p>

<div class="card">
  <h2>Status</h2>
  <table>
    <tr><td>Ready</td><td>{{if .Ready}}<span class="badge ready">yes</span>{{else}}<span class="badge wait">not yet</span>{{end}}</td></tr>
    <tr><td>Last updated</td><td>{{if .LastUpdated}}{{.LastUpdated}}{{else}}—{{end}}</td></tr>
    <tr><td>Tuners</td><td>{{.TunerCount}}</td></tr>
  </table>
</div>

<div class="card">
  <h2>EPG / Guide Data</h2>
  <a href="{{.EPGURL}}">{{.EPGURL}}</a>
</div>

<div class="card">
  <h2>Tuner Playlists</h2>
  <div class="tuner-list">
    {{range .Tuners}}<div><a href="{{.}}">{{.}}</a></div>{{end}}
  </div>
</div>
</body>
</html>
`))

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	host := r.Host
	if host == "" {
		host = "localhost:" + strconv.Itoa(s.cfg.Port)
	}

	tuners := make([]string, s.cfg.TunerCount)
	for i := 1; i <= s.cfg.TunerCount; i++ {
		tuners[i-1] = fmt.Sprintf("http://%s/tuner%d/channels.m3u", host, i)
	}

	data := struct {
		Version     string
		Ready       bool
		LastUpdated string
		TunerCount  int
		EPGURL      string
		Tuners      []string
	}{
		Version:     s.version,
		Ready:       s.cache.IsReady(),
		LastUpdated: formatTime(s.cache.LastUpdated()),
		TunerCount:  s.cfg.TunerCount,
		EPGURL:      fmt.Sprintf("http://%s/epg.xml", host),
		Tuners:      tuners,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTmpl.Execute(w, data)
}
