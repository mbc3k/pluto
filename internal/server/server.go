package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mbc3k/pluto/internal/auth"
	"github.com/mbc3k/pluto/internal/cache"
	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/playlist"
)

//go:embed index.html
var indexHTML string

//go:embed style.css
var styleCSS string

// Server is the HTTP server for serving playlists and EPG data.
type Server struct {
	srv      *http.Server
	cache    *cache.Cache
	cfg      *config.Config
	sessions []*auth.Session
	version  string
}

// New creates a Server wired to the given cache, config, and sessions.
// Sessions are required for dynamic M3U generation on each request.
func New(c *cache.Cache, sessions []*auth.Session, cfg *config.Config, version string) *Server {
	s := &Server{cache: c, cfg: cfg, sessions: sessions, version: version}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/style.css", s.handleCSS)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/epg.xml", s.handleEPG)
	mux.HandleFunc("/xmltv.xml", s.handleEPG)

	// Register tuner endpoints for each configured tuner (1-indexed for users).
	// Primary format matches upstream: /tuner-N-playlist.m3u
	// Legacy formats kept for backward compatibility.
	for n := 1; n <= cfg.TunerCount; n++ {
		idx := n - 1 // 0-indexed for cache
		mux.HandleFunc(fmt.Sprintf("/tuner-%d-playlist.m3u", n), s.makeM3UHandler(idx))
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
// EnsureFresh runs on every request so the JWT embedded in stream URLs stays
// valid even if the scheduler's refresh cycle hasn't fired yet. The playlist
// string itself is rebuilt only when the token generation or channel data
// changes; otherwise the previous result is served from cache.
func (s *Server) makeM3UHandler(idx int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions := s.sessions
		if idx >= len(sessions) {
			http.Error(w, "tuner index out of range", http.StatusBadRequest)
			return
		}
		sess := sessions[idx]

		// Ensure the session's token is fresh before generating URLs.
		if err := sess.EnsureFresh(r.Context()); err != nil {
			slog.Warn("session refresh failed for dynamic M3U", "tuner", idx, "err", err)
			// Fall through to serve stale data rather than failing completely.
		}

		// Single lock acquisition for token + stitcherParams + generation counter.
		// Also records lastIssued so maxTokenAge tracks actual hand-out time.
		token, stitcherParams, gen := sess.Credentials()

		if data, ok := s.cache.GetM3U(idx, gen); ok {
			writeM3U(w, data)
			return
		}

		channels, ok := s.cache.GetChannels()
		if !ok {
			http.Error(w, "playlist not yet available", http.StatusServiceUnavailable)
			return
		}

		data := playlist.Generate(s.cfg.StartChannel, token, stitcherParams, channels)
		s.cache.SetM3U(idx, gen, data)
		writeM3U(w, data)
	}
}

func writeM3U(w http.ResponseWriter, data string) {
	w.Header().Set("Content-Type", "application/x-mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// WriteString avoids an extra []byte copy of a potentially large playlist.
	io.WriteString(w, data)
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

	tuners := make([]string, len(s.sessions))
	for i := 1; i <= len(s.sessions); i++ {
		host := r.Host
		if host == "" {
			host = "localhost:" + strconv.Itoa(s.cfg.Port)
		}
		tuners[i-1] = fmt.Sprintf("http://%s/tuner-%d-playlist.m3u", host, i)
	}

	epgURL := ""
	if host := r.Host; host != "" {
		epgURL = fmt.Sprintf("http://%s/epg.xml", host)
	} else {
		epgURL = fmt.Sprintf("http://localhost:%d/epg.xml", s.cfg.Port)
	}

	status := map[string]any{
		"version":       s.version,
		"ready":         s.cache.IsReady(),
		"last_updated":  formatTime(updated),
		"tuner_count":   len(s.sessions),
		"start_channel": s.cfg.StartChannel,
		"tuners":        tuners,
		"epg_url":       epgURL,
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

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

type tunerEntry struct {
	N    int
	Path string
}

func (s *Server) handleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprint(w, styleCSS)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tuners := make([]tunerEntry, len(s.sessions))
	for i := 1; i <= len(s.sessions); i++ {
		tuners[i-1] = tunerEntry{N: i, Path: fmt.Sprintf("/tuner-%d-playlist.m3u", i)}
	}

	data := struct {
		Version     string
		LastUpdated string
		TunerCount  int
		Tuners      []tunerEntry
	}{
		Version:     s.version,
		LastUpdated: formatTime(s.cache.LastUpdated()),
		TunerCount:  len(s.sessions),
		Tuners:      tuners,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTmpl.Execute(w, data)
}
