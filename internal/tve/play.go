package tve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	authzTTL  = 23 * time.Hour
	shortTTL  = 5 * time.Minute
	streamTTL = 10 * time.Minute
)

// ErrUnknownStream means this service does not expose that channel on the
// local /stream route. Public manifests are not answered here.
var ErrUnknownStream = errors.New("unknown tve stream")

// StreamTarget is a playable HLS result. Redirect is an absolute manifest
// Channels can request. Body is a playlist this process already fetched,
// with relative URIs rewritten to absolute CDN addresses.
type StreamTarget struct {
	Redirect string
	Body     []byte
}

// PlayError is the operator-facing playback failure. The text is safe to
// return as the HTTP body. Retry is set when a later attempt should drop
// cached Adobe tokens and call the network again.
type PlayError struct {
	Step   string
	Status int
	Detail string
	Retry  bool
}

func (e *PlayError) Error() string {
	if e == nil {
		return "playback failed"
	}
	return e.Step + ": " + e.Detail
}

type timedSecret struct {
	value string
	until time.Time
}

type timedStream struct {
	target StreamTarget
	until  time.Time
}

type playSpec struct {
	source    string
	channel   string
	kind      string
	requestor string
	resource  string
}

// OpenStream authorizes the stored DirecTV session and returns a playable
// HLS target. A 401 or 403 from a network drops the cached Adobe tokens and
// tries once more. Credentials are not included in the error text.
func (s *Service) OpenStream(ctx context.Context, sourceID, channelID string) (StreamTarget, error) {
	slog.Info("tve stream request", "source", sourceID, "channel", channelID)
	spec, ok := playSpecFor(sourceID, channelID)
	if !ok || !s.localStream(sourceID, channelID) {
		return StreamTarget{}, ErrUnknownStream
	}
	if target, ok := s.cachedStream(spec); ok {
		return target, nil
	}
	target, err := s.resolvePlay(ctx, spec)
	if pe, ok := err.(*PlayError); ok && pe.Retry {
		s.forgetPlayback()
		target, err = s.resolvePlay(ctx, spec)
	}
	if err != nil {
		s.logPlay(spec, err)
		return StreamTarget{}, err
	}
	until := time.Now().Add(streamTTL)
	if exp := signedUntil(target.Redirect); !exp.IsZero() && exp.Before(until) {
		until = exp.Add(-2 * time.Minute)
	}
	s.storeStream(spec, target, until)
	if u, err := url.Parse(target.Redirect); err == nil && u.Host != "" {
		slog.Info("tve playback ready", "source", spec.source, "channel", spec.channel, "host", u.Host, "status", http.StatusFound)
		s.notePlayback(spec.source, spec.channel, http.StatusFound, "ready "+u.Host)
	} else if len(target.Body) > 0 {
		slog.Info("tve playback ready", "source", spec.source, "channel", spec.channel, "proxy", true, "status", http.StatusOK)
		s.notePlayback(spec.source, spec.channel, http.StatusOK, "ready proxied playlist")
	}
	return target, nil
}

func (s *Service) resolvePlay(ctx context.Context, spec playSpec) (StreamTarget, error) {
	st, err := s.playbackSession(ctx)
	if err != nil {
		return StreamTarget{}, err
	}
	switch spec.kind {
	case "amc":
		return s.playAMC(ctx, st, spec)
	case "warner":
		return s.playWarner(ctx, st, spec)
	case "foxnews":
		return s.playFoxNews(ctx, st, spec)
	case "foxsports":
		return s.playFoxSports(ctx, st, spec)
	case "discovery":
		return s.playDiscovery(ctx, st, spec)
	default:
		return StreamTarget{}, ErrUnknownStream
	}
}

func (s *Service) playbackSession(ctx context.Context) (State, error) {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	st, err := loadState(s.sessionPath)
	if err != nil {
		return State{}, playFail("directv sign-in", http.StatusServiceUnavailable, "the session file could not be read", false)
	}
	if strings.TrimSpace(st.AuthnToken) == "" {
		return State{}, playFail("directv sign-in", http.StatusServiceUnavailable, "no DirecTV authentication is stored. Sign in with Safari or Firefox on /tve/directv", false)
	}
	if !st.Expires.IsZero() && !time.Now().Before(st.Expires) {
		return State{}, playFail("directv sign-in", http.StatusServiceUnavailable, "DirecTV authentication has expired. Start browser sign-in again on /tve/directv", false)
	}
	s.maintainAuthn(ctx, &st)
	return st, nil
}

func playFail(step string, status int, detail string, retry bool) *PlayError {
	if status == 0 {
		status = http.StatusBadGateway
	}
	return &PlayError{Step: step, Status: status, Detail: detail, Retry: retry}
}

func (s *Service) logPlay(spec playSpec, err error) {
	secrets := s.secretValues(nil)
	st, loadErr := loadState(s.sessionPath)
	if loadErr == nil {
		secrets = s.playbackSecrets(st)
	}
	status := 0
	var pe *PlayError
	if errors.As(err, &pe) {
		status = pe.Status
	}
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	detail := redact(err.Error(), secrets...)
	slog.Warn("tve playback failed", "source", spec.source, "channel", spec.channel, "status", status, "err", detail)
	s.notePlayback(spec.source, spec.channel, status, detail)
}

func (s *Service) notePlayback(source, channel string, status int, detail string) {
	s.mu.Lock()
	s.playback = fmt.Sprintf("%s/%s HTTP %d %s", source, channel, status, detail)
	s.mu.Unlock()
}

func (s *Service) logStep(spec playSpec, step, method, rawURL string, status int) {
	u, err := url.Parse(rawURL)
	host, path := rawURL, ""
	if err == nil {
		host = u.Host
		path = u.Path
	}
	slog.Info("tve playback step", "source", spec.source, "channel", spec.channel, "step", step, "method", method, "host", host, "path", path, "status", status)
}

func (s *Service) cachedStream(spec playSpec) (StreamTarget, bool) {
	s.playMu.Lock()
	defer s.playMu.Unlock()
	s.initPlayLocked()
	item, ok := s.streamCache[spec.key()]
	if !ok || time.Until(item.until) < time.Minute {
		return StreamTarget{}, false
	}
	return item.target, true
}

func (s *Service) storeStream(spec playSpec, target StreamTarget, until time.Time) {
	if until.Before(time.Now().Add(time.Minute)) {
		return
	}
	s.playMu.Lock()
	defer s.playMu.Unlock()
	s.initPlayLocked()
	s.streamCache[spec.key()] = timedStream{target: target, until: until}
}

func (s *Service) forgetPlayback() {
	s.playMu.Lock()
	defer s.playMu.Unlock()
	s.authzCache = map[string]timedSecret{}
	s.shortCache = map[string]timedSecret{}
	s.streamCache = map[string]timedStream{}
}

func (s *Service) initPlayLocked() {
	if s.authzCache == nil {
		s.authzCache = map[string]timedSecret{}
	}
	if s.shortCache == nil {
		s.shortCache = map[string]timedSecret{}
	}
	if s.streamCache == nil {
		s.streamCache = map[string]timedStream{}
	}
}

func (spec playSpec) key() string {
	return spec.source + "\n" + spec.channel
}

func (s *Service) postJSON(ctx context.Context, rawURL string, payload any, headers map[string]string, secrets []string) (fetched, error) {
	var body []byte
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fetched{}, err
		}
		body = raw
	} else {
		body = []byte("{}")
	}
	return s.exchange(ctx, http.MethodPost, rawURL, "application/json", body, headers, secrets)
}

func (s *Service) exchange(ctx context.Context, method, rawURL, contentType string, body []byte, headers map[string]string, secrets []string) (fetched, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return fetched{}, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json,application/vnd.apple.mpegurl,*/*")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http().Do(req)
	if err != nil {
		return fetched{}, fmt.Errorf("%s", redact(err.Error(), secrets...))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return fetched{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        raw,
		Header:      resp.Header.Clone(),
		URL:         rawURL,
	}, nil
}

func (s *Service) httpResult(step string, got fetched, err error, secrets []string) *PlayError {
	if err != nil {
		return playFail(step, http.StatusBadGateway, redact(err.Error(), secrets...), true)
	}
	if got.Status >= 200 && got.Status < 300 {
		return nil
	}
	status := http.StatusBadGateway
	retry := false
	if got.Status == http.StatusUnauthorized || got.Status == http.StatusForbidden {
		status = got.Status
		retry = true
	}
	detail := fmt.Sprintf("HTTP %d", got.Status)
	if snip := snippet(got.Body, secrets...); snip != "" {
		detail += " " + snip
	}
	return playFail(step, status, detail, retry)
}

func findHLS(v any) string {
	var found []string
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			for _, key := range []string{"src", "url", "href", "playbackUrl"} {
				if u := strings.TrimSpace(asText(n[key])); strings.Contains(u, ".m3u8") {
					found = append(found, u)
				}
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(v)
	for _, u := range found {
		lower := strings.ToLower(u)
		if !strings.Contains(lower, "widevine") && !strings.Contains(lower, "cenc") && !strings.Contains(lower, "cbcs") {
			return u
		}
	}
	if len(found) > 0 {
		return found[0]
	}
	return ""
}

func decodeJSON(body []byte) (any, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// absolutize rewrites a playlist so Channels can fetch every URI from the
// CDN that served it. Relative paths would otherwise be requested from Pluto.
func absolutize(master string, body []byte) []byte {
	base, err := url.Parse(master)
	if err != nil {
		return body
	}
	var b strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case trim == "" || strings.HasPrefix(trim, "#") && !strings.Contains(trim, `URI="`):
			b.WriteString(line)
		case strings.Contains(trim, `URI="`):
			b.WriteString(rewriteURIAttr(base, line))
		default:
			if abs, ok := absoluteRef(base, trim); ok {
				b.WriteString(abs)
			} else {
				b.WriteString(line)
			}
		}
		b.WriteByte('\n')
	}
	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}

func rewriteURIAttr(base *url.URL, line string) string {
	const key = `URI="`
	i := strings.Index(line, key)
	if i < 0 {
		return line
	}
	rest := line[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return line
	}
	abs, ok := absoluteRef(base, rest[:j])
	if !ok {
		return line
	}
	return line[:i+len(key)] + abs + rest[j:]
}

func absoluteRef(base *url.URL, ref string) (string, bool) {
	u, err := url.Parse(ref)
	if err != nil || ref == "" {
		return "", false
	}
	if u.IsAbs() {
		return ref, true
	}
	return base.ResolveReference(u).String(), true
}

func signedUntil(raw string) time.Time {
	q := raw
	if i := strings.Index(raw, "?"); i >= 0 {
		q = raw[i+1:]
	}
	for _, part := range strings.FieldsFunc(q, func(r rune) bool { return r == '&' || r == '~' }) {
		if !strings.HasPrefix(part, "exp=") {
			continue
		}
		var n int64
		if _, err := fmt.Sscan(strings.TrimPrefix(part, "exp="), &n); err == nil && n > 1_000_000_000 {
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Time{}
}
