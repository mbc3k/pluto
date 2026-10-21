package server

import (
	"compress/gzip"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mbc3k/pluto/internal/auth"
	"github.com/mbc3k/pluto/internal/cache"
	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/metrics"
	"github.com/mbc3k/pluto/internal/playlist"
	"github.com/mbc3k/pluto/internal/tve"
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

// tveFeeds is the TV Everywhere surface used by the status page and /tve routes.
type tveFeeds interface {
	List() []tve.Link
	Playlist(id, base string) (string, bool)
	Guide(id string) ([]byte, time.Time, bool)
	SignIn() tve.SignInView
	SubmitCode(ctx context.Context, code string) error
	StreamStatus(sourceID, channelID string) (string, bool)
}

// Server is the HTTP server for serving playlists and EPG data.

type Server struct {
	srv       *http.Server
	cache     *cache.Cache
	cfg       *config.Config
	sessions  []Tuner
	version   string
	authReady atomic.Bool
	metrics   *metrics.Recorder
	tve       tveFeeds
}

var gzipPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
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
	s := &Server{cache: c, cfg: cfg, sessions: tuners, version: version, metrics: metrics.New()}
	// Tests and any caller that does not HoldAuth serve playlists immediately.
	s.authReady.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/style.css", s.handleCSS)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/epg.xml", s.handleEPG)
	mux.HandleFunc("/xmltv.xml", s.handleEPG)
	mux.HandleFunc("/tve/directv", s.handleTVESignIn)
	mux.HandleFunc("/tve/directv/code", s.handleTVECode)
	mux.HandleFunc("/tve/", s.handleTVE)
	if len(cfg.TVESources) > 0 {
		s.tve = tve.NewService(cfg)
	}

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
		Addr:         cfg.ListenAddr(),
		Handler:      securityHeaders(s.gate(mux)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// HoldAuth makes playlist requests wait until ReleaseAuth. The guide can
// still be served. main calls this before Listen so a request cannot land
// in the window before the flag flips.
func (s *Server) HoldAuth() { s.authReady.Store(false) }

// ReleaseAuth lets playlist requests call EnsureFresh. It runs even when
// the initial sign-in failed, so the next playlist request retries the boot.
func (s *Server) ReleaseAuth() { s.authReady.Store(true) }

// Metrics is the recorder the scheduler updates after each guide refresh.
func (s *Server) Metrics() *metrics.Recorder { return s.metrics }

// TVE returns the background refresher, or nil when no TV Everywhere source
// is enabled.
func (s *Server) TVE() *tve.Service {
	svc, _ := s.tve.(*tve.Service)
	return svc
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// gate checks ACCESS_TOKEN for Pluto playlists, the EPG, and TV Everywhere
// routes. An empty token leaves every route open. Channels DVR cannot send
// an Authorization header, so the secret is accepted as ?token= or as a
// /t/<token>/ prefix. Either match is enough, and a valid prefix is stripped
// once. /health, /metrics, /status, and / stay open so an operator can see
// whether the process is up and copy a URL that already includes the secret.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.cfg.AccessToken
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/t/") {
			rest := strings.TrimPrefix(r.URL.Path, "/t/")
			got, suffix, ok := strings.Cut(rest, "/")
			if !ok || !tokenMatch(got, token) {
				s.metrics.Hit("auth", http.StatusUnauthorized)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + suffix
			next.ServeHTTP(w, r2)
			return
		}
		if isProtectedPath(r.URL.Path) && !tokenMatch(r.URL.Query().Get("token"), token) {
			s.metrics.Hit("auth", http.StatusUnauthorized)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tokenMatch(got, want string) bool {
	if len(got) != len(want) {
		subtle.ConstantTimeCompare([]byte(want), []byte(want))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func isProtectedPath(path string) bool {
	switch path {
	case "/epg.xml", "/xmltv.xml":
		return true
	}
	if tve.ProtectedPath(path) {
		return true
	}
	if strings.HasPrefix(path, "/tuner-") && strings.HasSuffix(path, "-playlist.m3u") {
		mid := strings.TrimSuffix(strings.TrimPrefix(path, "/tuner-"), "-playlist.m3u")
		return mid != "" && allDigits(mid)
	}
	if !strings.HasPrefix(path, "/tuner") {
		return false
	}
	rest := strings.TrimPrefix(path, "/tuner")
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		return false
	}
	suf := rest[i:]
	if suf != "/channels.m3u" && suf != "/m3u" {
		return false
	}
	return allDigits(rest[:i])
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ListenAndServe starts the HTTP server. It returns http.ErrServerClosed
// after Shutdown is called.
func (s *Server) ListenAndServe() error {
	slog.Info("HTTP server listening", "addr", s.srv.Addr, "access_gate", s.cfg.AccessToken != "")
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
			s.metrics.Hit("playlist", http.StatusBadRequest)
			http.Error(w, "tuner index out of range", http.StatusBadRequest)
			return
		}
		// The initial sign-in is still running. Do not call EnsureFresh here:
		// that would pile duplicate boots onto the startup attempt.
		if !s.authReady.Load() {
			s.metrics.Hit("playlist", http.StatusServiceUnavailable)
			http.Error(w, "tuners still authenticating", http.StatusServiceUnavailable)
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
			s.metrics.Hit("playlist", http.StatusOK)
			writeBody(w, r, "application/x-mpegurl", []byte(data), s.cache.LastUpdated())
			return
		}

		channels, channelsGen, ok := s.cache.GetChannels()
		if !ok {
			s.metrics.Hit("playlist", http.StatusServiceUnavailable)
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
		s.metrics.Hit("playlist", http.StatusOK)
		writeBody(w, r, "application/x-mpegurl", []byte(data), s.cache.LastUpdated())
	}
}

// writeBody serves a playlist or EPG document, gzip-encoded when the client
// asks for it. Error responses do not come through here. Content-Length is
// set only for an uncompressed body so a gzip response is not labeled with
// the uncompressed size.
func writeBody(w http.ResponseWriter, r *http.Request, contentType string, body []byte, updated time.Time) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept-Encoding")
	if !updated.IsZero() {
		w.Header().Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
	}
	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzipPool.Get().(*gzip.Writer)
		zw.Reset(w)
		_, _ = zw.Write(body)
		_ = zw.Close()
		gzipPool.Put(zw)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// acceptsGzip reports whether Accept-Encoding lists gzip with a non-zero q.
func acceptsGzip(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		q := 1.0
		if i := strings.IndexByte(part, ';'); i >= 0 {
			name = strings.TrimSpace(part[:i])
			for _, param := range strings.Split(part[i+1:], ";") {
				param = strings.TrimSpace(param)
				if !strings.HasPrefix(param, "q=") {
					continue
				}
				if v, err := strconv.ParseFloat(param[2:], 64); err == nil {
					q = v
				}
			}
		}
		if strings.EqualFold(name, "gzip") && q > 0 {
			return true
		}
	}
	return false
}

func (s *Server) handleEPG(w http.ResponseWriter, r *http.Request) {
	data, updated, ok := s.cache.GetXMLTV()
	if !ok {
		s.metrics.Hit("epg", http.StatusServiceUnavailable)
		http.Error(w, "EPG not yet available", http.StatusServiceUnavailable)
		return
	}
	s.metrics.Hit("epg", http.StatusOK)
	writeBody(w, r, "application/xml; charset=utf-8", data, updated)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authReady.Load() {
		http.Error(w, "authenticating", http.StatusServiceUnavailable)
		return
	}
	if !s.cache.IsReady() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	channels, _, _ := s.cache.GetChannels()
	var last int64
	if updated := s.cache.LastUpdated(); !updated.IsZero() {
		last = updated.Unix()
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.metrics.Write(w, metrics.Gauges{
		Ready:       s.cache.IsReady(),
		AuthReady:   s.authReady.Load(),
		Channels:    len(channels),
		Tuners:      len(s.sessions),
		GuideHours:  s.cfg.GuideHours,
		LastRefresh: last,
	})
}

// streamBase is the origin prefixed onto local stream paths inside a playlist.
// When an access token is set, the path form is used so a player that drops
// query strings still presents the token.
func (s *Server) streamBase(r *http.Request) string {
	base := publicBase(r, s.cfg.Port)
	if s.cfg.AccessToken == "" {
		return base
	}
	return base + "/t/" + s.cfg.AccessToken
}

func (s *Server) tveURL(base, path string) string {
	if s.cfg.AccessToken == "" {
		return base + path
	}
	return base + path + "?token=" + url.QueryEscape(s.cfg.AccessToken)
}

func (s *Server) handleTVE(w http.ResponseWriter, r *http.Request) {
	if s.tve == nil {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/tve/")
	switch {
	case strings.HasSuffix(rel, ".m3u") && !strings.Contains(rel, "/"):
		id := strings.TrimSuffix(rel, ".m3u")
		body, ok := s.tve.Playlist(id, s.streamBase(r))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	case strings.HasSuffix(rel, ".xml") && !strings.Contains(rel, "/"):
		id := strings.TrimSuffix(rel, ".xml")
		body, updated, ok := s.tve.Guide(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if !updated.IsZero() {
			w.Header().Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	case strings.HasSuffix(rel, "/stream"):
		parts := strings.Split(rel, "/")
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		msg, ok := s.tve.StreamStatus(parts[0], parts[1])
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, msg, http.StatusServiceUnavailable)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleTVESignIn(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/tve/directv" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.tve == nil {
		http.NotFound(w, r)
		return
	}
	view := s.tve.SignIn()
	action := "/tve/directv/code"
	if s.cfg.AccessToken != "" {
		action += "?token=" + url.QueryEscape(s.cfg.AccessToken)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := signInTmpl.Execute(w, signInPage{
		Notice:  view.Notice,
		Prompt:  view.Prompt,
		Signed:  view.Signed,
		Pending: view.Pending,
		Action:  action,
	}); err != nil {
		slog.Error("render directv sign-in", "err", err)
	}
}

func (s *Server) handleTVECode(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/tve/directv/code" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.tve == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := s.tve.SubmitCode(r.Context(), r.PostForm.Get("code")); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	loc := "/tve/directv"
	if s.cfg.AccessToken != "" {
		loc += "?token=" + url.QueryEscape(s.cfg.AccessToken)
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	updated := s.cache.LastUpdated()
	base := publicBase(r, s.cfg.Port)
	channels, _, _ := s.cache.GetChannels()

	tuners := make([]string, len(s.sessions))
	for i := 1; i <= len(s.sessions); i++ {
		tuners[i-1] = base + s.withToken(fmt.Sprintf("/tuner-%d-playlist.m3u", i))
	}

	status := map[string]any{
		"version":       s.version,
		"ready":         s.cache.IsReady(),
		"auth_ready":    s.authReady.Load(),
		"last_updated":  formatTime(updated),
		"tuner_count":   len(s.sessions),
		"channels":      len(channels),
		"start_channel": s.cfg.StartChannel,
		"refresh_every": s.cfg.RefreshEvery.String(),
		"guide_hours":   s.cfg.GuideHours,
		"bind_addr":     s.cfg.BindAddr,
		"access_token":  s.cfg.AccessToken != "",
		"tuners":        tuners,
		"epg_url":       base + s.withToken("/epg.xml"),
	}
	if tveStatus := s.tveStatus(base); tveStatus != nil {
		status["tve"] = tveStatus
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(status); err != nil {
		slog.Debug("write status", "err", err)
	}
}

func (s *Server) tveStatus(base string) map[string]any {
	if s.tve == nil {
		return nil
	}
	view := s.tve.SignIn()
	var sources []map[string]any
	out := map[string]any{
		"provider":  s.cfg.TVEProvider,
		"signed_in": view.Signed,
		"pending":   view.Pending,
		"notice":    view.Notice,
		"sign_in":   s.tveURL(base, "/tve/directv"),
	}
	if view.Pending {
		out["prompt"] = view.Prompt
	}
	for _, link := range s.tve.List() {
		playlist := s.tveURL(base, "/tve/"+link.ID+".m3u")
		guide := s.tveURL(base, "/tve/"+link.ID+".xml")
		if link.ID == "all" {
			out["combined_playlist"] = playlist
			out["combined_epg"] = guide
			continue
		}
		sources = append(sources, map[string]any{
			"id":       link.ID,
			"name":     link.Name,
			"channels": link.Channels,
			"playlist": playlist,
			"epg":      guide,
			"updated":  formatTime(link.Updated),
		})
	}
	out["sources"] = sources
	return out
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

var signInTmpl = template.Must(template.New("directv").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>DirecTV sign-in</title>
  <link rel="stylesheet" href="/style.css">
</head>
<body>
  <div class="container">
    <h1 class="logo">DirecTV</h1>
    {{if .Notice}}<p class="banner">{{.Notice}}</p>{{end}}
    {{if .Signed}}<p class="section-subtitle">DirecTV session is stored.</p>{{end}}
    {{if .Pending}}
    <p class="section-subtitle">{{.Prompt}}</p>
    <form method="post" action="{{.Action}}">
      <label class="url-label" for="code">Code</label>
      <input class="url-field" id="code" name="code" autocomplete="one-time-code" inputmode="numeric">
      <button class="epg-button" type="submit">Continue</button>
    </form>
    {{end}}
  </div>
</body>
</html>
`))

type tunerEntry struct {
	N    int
	Path string
}

type tvePageLink struct {
	Name     string
	Channels int
	Playlist string
	Guide    string
}

type pageData struct {
	Version      string
	LastUpdated  string
	TunerCount   int
	ChannelCount int
	Ready        bool
	AuthReady    bool
	BaseURL      string
	PlaylistURL  string
	EPGURL       string
	Tuners       []tunerEntry
	TVE          []tvePageLink
	TVENotice    string
	TVEPrompt    string
	TVEPending   bool
	TVESignInURL string
}

type signInPage struct {
	Notice  string
	Prompt  string
	Signed  bool
	Pending bool
	Action  string
}

// withToken appends the shared secret as a query parameter when the gate is
// on, so the status page and index can be pasted into Channels DVR as-is.
func (s *Server) withToken(path string) string {
	if s.cfg.AccessToken == "" {
		return path
	}
	return path + "?token=" + url.QueryEscape(s.cfg.AccessToken)
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
		tuners[i-1] = tunerEntry{N: i, Path: s.withToken(fmt.Sprintf("/tuner-%d-playlist.m3u", i))}
	}

	data := pageData{
		Version:      displayVersion(s.version),
		Ready:        ready,
		AuthReady:    s.authReady.Load(),
		ChannelCount: len(channels),
		TunerCount:   len(s.sessions),
		BaseURL:      base,
		EPGURL:       base + s.withToken("/epg.xml"),
		Tuners:       tuners,
	}
	if ready {
		data.LastUpdated = formatTime(s.cache.LastUpdated())
	}
	if len(tuners) > 0 {
		data.PlaylistURL = base + tuners[0].Path
	}
	if s.tve != nil {
		view := s.tve.SignIn()
		data.TVENotice = view.Notice
		data.TVEPrompt = view.Prompt
		data.TVEPending = view.Pending
		data.TVESignInURL = s.tveURL(base, "/tve/directv")
		for _, link := range s.tve.List() {
			data.TVE = append(data.TVE, tvePageLink{
				Name:     link.Name,
				Channels: link.Channels,
				Playlist: s.tveURL(base, "/tve/"+link.ID+".m3u"),
				Guide:    s.tveURL(base, "/tve/"+link.ID+".xml"),
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := indexTmpl.Execute(w, data); err != nil {
		slog.Error("render index", "err", err)
	}
}
