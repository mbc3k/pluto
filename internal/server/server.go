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

// Tuner is the authenticated Pluto session surface used to build playlists.
type Tuner interface {
	EnsureFresh(ctx context.Context) error
	Credentials() (token, stitcherParams string, gen int64)
}

// Server is the HTTP server for serving playlists and EPG data.
type Server struct {
	srv      *http.Server
	cache    *cache.Cache
	cfg      *config.Config
	sessions []Tuner
	version  string
}

// New creates a Server wired to the given cache, config, and sessions.
// Sessions are required for dynamic M3U generation on each request.
func New(c *cache.Cache, sessions []*auth.Session, cfg *config.Config, version string) *Server {
	tuners := make([]Tuner, len(sessions))
	for i, s := range sessions {
		tuners[i] = s
	}
	return newServer(c, tuners, cfg, version)
}

func newServer(c *cache.Cache, tuners []Tuner, cfg *config.Config, version string) *Server {
	s := &Server{cache: c, cfg: cfg, sessions: tuners, version: version}

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
	for n := 1; n <= len(tuners); n++ {
		idx := n - 1 // 0-indexed for cache
		mux.HandleFunc(fmt.Sprintf("/tuner-%d-playlist.m3u", n), s.makeM3UHandler(idx))
		mux.HandleFunc(fmt.Sprintf("/tuner%d/channels.m3u", n), s.makeM3UHandler(idx))
		mux.HandleFunc(fmt.Sprintf("/tuner%d/m3u", n), s.makeM3UHandler(idx))
	}

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      securityHeaders(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
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
		if idx < 0 || idx >= len(s.sessions) {
			http.Error(w, "tuner index out of range", http.StatusBadRequest)
			return
		}
		sess := s.sessions[idx]

		// Ensure the session's token is fresh before generating URLs.
		// A failed refresh still serves the previous token rather than going dark.
		if err := sess.EnsureFresh(r.Context()); err != nil {
			slog.Warn("session refresh failed for dynamic M3U", "tuner", idx+1, "err", err)
		}

		// Single lock acquisition for token + stitcherParams + generation counter.
		token, stitcherParams, gen := sess.Credentials()

		if data, ok := s.cache.GetM3U(idx, gen); ok {
			writeM3U(w, data, s.cache.LastUpdated())
			return
		}

		channels, channelsGen, ok := s.cache.GetChannels()
		if !ok {
			http.Error(w, "playlist not yet available", http.StatusServiceUnavailable)
			return
		}

		data := playlist.Generate(s.cfg.StartChannel, token, stitcherParams, channels)
		if !s.cache.SetM3U(idx, gen, channelsGen, data) {
			// The guide was refreshed while we built this playlist. Rebuild
			// once against the current snapshot so we don't cache a mismatch.
			if channels2, gen2, ok2 := s.cache.GetChannels(); ok2 && gen2 != channelsGen {
				token, stitcherParams, gen = sess.Credentials()
				data = playlist.Generate(s.cfg.StartChannel, token, stitcherParams, channels2)
				s.cache.SetM3U(idx, gen, gen2, data)
			}
		}
		writeM3U(w, data, s.cache.LastUpdated())
	}
}

func writeM3U(w http.ResponseWriter, data string, updated time.Time) {
	w.Header().Set("Content-Type", "application/x-mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	if !updated.IsZero() {
		w.Header().Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// WriteString avoids an extra []byte copy of a potentially large playlist.
	_, _ = io.WriteString(w, data)
}

func (s *Server) handleEPG(w http.ResponseWriter, r *http.Request) {
	data, updated, ok := s.cache.GetXMLTV()
	if !ok {
		http.Error(w, "EPG not yet available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.cache.IsReady() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	updated := s.cache.LastUpdated()
	base := publicBase(r, s.cfg.Port)
	channels, _, _ := s.cache.GetChannels()

	tuners := make([]string, len(s.sessions))
	for i := 1; i <= len(s.sessions); i++ {
		tuners[i-1] = fmt.Sprintf("%s/tuner-%d-playlist.m3u", base, i)
	}

	status := map[string]any{
		"version":       s.version,
		"ready":         s.cache.IsReady(),
		"last_updated":  formatTime(updated),
		"tuner_count":   len(s.sessions),
		"channels":      len(channels),
		"start_channel": s.cfg.StartChannel,
		"tuners":        tuners,
		"epg_url":       base + "/epg.xml",
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(status); err != nil {
		slog.Debug("write status", "err", err)
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(strings.Replace(time.RFC3339, "T", " ", 1))
}

// publicBase is the origin a caller should use to reach this server.
// Reverse proxies can pass the external scheme and host.
func publicBase(r *http.Request, port int) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if proto := forwardedProto(r); proto != "" {
		scheme = proto
	}
	host := forwardedHost(r)
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		host = "localhost:" + strconv.Itoa(port)
	}
	return scheme + "://" + bracketHost(host)
}

func forwardedProto(r *http.Request) string {
	proto := strings.ToLower(firstHeaderToken(r.Header.Get("X-Forwarded-Proto")))
	if proto == "https" || proto == "http" {
		return proto
	}
	return ""
}

func forwardedHost(r *http.Request) string {
	return firstHeaderToken(r.Header.Get("X-Forwarded-Host"))
}

func firstHeaderToken(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// bracketHost wraps a bare IPv6 address so it can sit in an http URL.
func bracketHost(host string) string {
	if host == "" || strings.HasPrefix(host, "[") {
		return host
	}
	if strings.Count(host, ":") >= 2 {
		return "[" + host + "]"
	}
	return host
}

func displayVersion(v string) string {
	if v == "" {
		return "dev"
	}
	if v == "dev" || strings.HasPrefix(v, "v") || strings.HasPrefix(v, "V") {
		return v
	}
	return "v" + v
}

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

type tunerEntry struct {
	N    int
	Path string
}

type pageData struct {
	Version      string
	LastUpdated  string
	TunerCount   int
	ChannelCount int
	Ready        bool
	BaseURL      string
	PlaylistURL  string
	EPGURL       string
	Tuners       []tunerEntry
}

func (s *Server) handleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprint(w, styleCSS)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	base := publicBase(r, s.cfg.Port)
	channels, _, ready := s.cache.GetChannels()
	tuners := make([]tunerEntry, len(s.sessions))
	for i := 1; i <= len(s.sessions); i++ {
		tuners[i-1] = tunerEntry{N: i, Path: fmt.Sprintf("/tuner-%d-playlist.m3u", i)}
	}

	data := pageData{
		Version:      displayVersion(s.version),
		Ready:        ready,
		ChannelCount: len(channels),
		TunerCount:   len(s.sessions),
		BaseURL:      base,
		EPGURL:       base + "/epg.xml",
		Tuners:       tuners,
	}
	if ready {
		data.LastUpdated = formatTime(s.cache.LastUpdated())
	}
	if len(tuners) > 0 {
		data.PlaylistURL = base + tuners[0].Path
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := indexTmpl.Execute(w, data); err != nil {
		slog.Error("render index", "err", err)
	}
}
