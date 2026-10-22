package tve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/source"
)

// Link is one playlist/guide pair for the status page.
type Link struct {
	ID       string
	Name     string
	Channels int
	Updated  time.Time
}

// SignInView is what the status page and the code form are allowed to show.
// ActivationURL and Code are only for the sign-in page. The status JSON
// does not include them.
type SignInView struct {
	Signed        bool
	Pending       bool
	Waiting       bool
	Prompt        string
	Notice        string
	Method        string
	Expires       time.Time
	ActivationURL string
	Code          string
	// Renewal is "client" when the Adobe access token can be refreshed
	// without a browser, "retrying" when that refresh is backing off, and
	// "needs_browser" when only a person can extend the DirecTV login.
	Renewal string
}

// Service refreshes enabled TV Everywhere sources and remembers a DirecTV
// session. A failed refresh keeps the previous guide.
type Service struct {
	sources             []string
	combined            bool
	start               int
	user                string
	password            string
	sessionPath         string
	every               time.Duration
	client              *http.Client
	adobeBase           string
	historyStatementURL string
	turnerStatementURL  string
	endpoints           Endpoints

	mu     sync.Mutex
	signMu sync.Mutex
	feeds  map[string]source.Feed
	signIn SignInView

	renewNext    time.Time
	renewDelay   time.Duration
	confirmNext  time.Time
	confirmDelay time.Duration
	confirmQuiet bool
	warnedFor    time.Time
	warnedLevel  int
}

// NewService builds a service from configuration. Catalogs are available
// immediately; guides and DirecTV sign-in fill in when Run is called.
func NewService(cfg *config.Config) *Service {
	s := &Service{
		sources:     append([]string(nil), cfg.TVESources...),
		combined:    cfg.TVECombined,
		start:       cfg.TVEStartChannel,
		user:        cfg.DirectvUsername,
		password:    cfg.DirectvPassword,
		sessionPath: cfg.TVESessionFile,
		every:       cfg.RefreshEvery,
		client:      newBrowserClient(),
		adobeBase:   "https://sp.auth.adobe.com",
		feeds:       map[string]source.Feed{},
	}
	if s.every <= 0 {
		s.every = 3 * time.Hour
	}
	if s.start < 0 {
		s.start = 0
	}
	for _, id := range s.sources {
		s.feeds[id] = source.Feed{ID: id, Name: DisplayName(id), Channels: cloneChannels(id)}
	}
	if st, err := loadState(s.sessionPath); err != nil {
		slog.Warn("tve session was not loaded", "err", err)
	} else {
		s.applyState(st)
	}
	return s
}

// Run signs in, refreshes guides, and repeats on the Pluto refresh interval.
func (s *Service) Run(ctx context.Context) {
	s.refresh(ctx)
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refresh(ctx)
		}
	}
}

func (s *Service) refresh(ctx context.Context) {
	s.trySignIn(ctx)
	for _, id := range s.sources {
		channels := cloneChannels(id)
		err := s.fillGuide(ctx, id, channels)
		if programmeCount(channels) == 0 {
			slog.Warn("tve guide returned zero programmes", "source", id)
		}
		if err != nil {
			slog.Error("tve guide refresh failed", "source", id, "err", redact(err.Error(), s.password, s.user))
		}
		s.keepGuides(id, channels)
		s.mu.Lock()
		s.feeds[id] = source.Feed{ID: id, Name: DisplayName(id), Channels: channels, Updated: time.Now().UTC()}
		s.mu.Unlock()
	}
}

// List is the enabled sources, plus the combined feed when it is on.
func (s *Service) List() []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Link, 0, len(s.sources)+1)
	for _, id := range s.sources {
		f := s.feeds[id]
		out = append(out, Link{ID: id, Name: f.Name, Channels: len(f.Channels), Updated: f.Updated})
	}
	if s.combined && len(s.sources) > 0 {
		f := s.combinedFeed()
		out = append(out, Link{ID: combinedSourceID, Name: f.Name, Channels: len(f.Channels), Updated: f.Updated})
	}
	return out
}

// Playlist renders one source, or "all" for the combined feed.
func (s *Service) Playlist(id, base string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.feedLocked(id)
	if !ok {
		return "", false
	}
	return source.M3U(f, s.startLocked(id), base), true
}

// Guide renders one source's XMLTV, or the combined guide.
func (s *Service) Guide(id string) ([]byte, time.Time, bool) {
	s.mu.Lock()
	f, ok := s.feedLocked(id)
	s.mu.Unlock()
	if !ok {
		return nil, time.Time{}, false
	}
	body, err := source.XMLTV(f)
	if err != nil {
		return nil, time.Time{}, false
	}
	return body, f.Updated, true
}

// SignIn returns the operator-facing DirecTV state.
func (s *Service) SignIn() SignInView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signIn
}

// SubmitCode continues a DirecTV login that stopped for a code or a menu choice.
func (s *Service) SubmitCode(ctx context.Context, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("a code is required")
	}
	s.signMu.Lock()
	defer s.signMu.Unlock()
	st, err := loadState(s.sessionPath)
	if err != nil {
		return err
	}
	if st.Pending == nil {
		return fmt.Errorf("directv is not waiting for a code")
	}
	client := newBrowserClient()
	restoreCookies(client, st.Pending.Cookies)
	out, err := Resume(ctx, client, s.endpoints, st.Pending, code, s.password, s.user)
	if err != nil {
		return err
	}
	return s.storeOutcome(out, client)
}

// StreamStatus is the plain-text answer for a network whose manifest is not
// a public URL. It does not include credentials. ok is false when this
// service is not serving that channel.
func (s *Service) StreamStatus(sourceID, channelID string) (string, bool) {
	s.mu.Lock()
	f, ok := s.feeds[sourceID]
	s.mu.Unlock()
	if !ok || channelID == "" {
		return "", false
	}
	for _, ch := range f.Channels {
		if ch.ID != channelID || !strings.HasPrefix(ch.StreamURL, "/") {
			continue
		}
		return "this stream is issued only after DirecTV authorizes " + DisplayName(sourceID) + "; playback still needs a live check with real credentials", true
	}
	return "", false
}

func (s *Service) feed(id string) (source.Feed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.feedLocked(id)
}

func (s *Service) feedLocked(id string) (source.Feed, bool) {
	if id == combinedSourceID {
		if !s.combined || len(s.sources) == 0 {
			return source.Feed{}, false
		}
		return s.combinedFeed(), true
	}
	f, ok := s.feeds[id]
	if !ok {
		return source.Feed{}, false
	}
	return f, true
}

func (s *Service) combinedFeed() source.Feed {
	f := source.Feed{ID: combinedSourceID, Name: DisplayName(combinedSourceID)}
	var latest time.Time
	for _, id := range s.sources {
		part := s.feeds[id]
		f.Channels = append(f.Channels, part.Channels...)
		if part.Updated.After(latest) {
			latest = part.Updated
		}
	}
	f.Updated = latest
	return f
}

func (s *Service) startLocked(id string) int {
	if id == combinedSourceID || len(s.sources) == 0 {
		return s.start
	}
	n := s.start
	for _, sid := range s.sources {
		if sid == id {
			return n
		}
		n += len(s.feeds[sid].Channels)
	}
	return s.start
}

func (s *Service) keepGuides(id string, channels []source.Channel) {
	s.mu.Lock()
	prev := s.feeds[id]
	s.mu.Unlock()
	byID := map[string][]source.Program{}
	for _, ch := range prev.Channels {
		if len(ch.Programs) > 0 {
			byID[ch.ID] = ch.Programs
		}
	}
	for i := range channels {
		if len(channels[i].Programs) == 0 {
			channels[i].Programs = byID[channels[i].ID]
		}
	}
}

func (s *Service) fillAE(ctx context.Context, channels []source.Channel) error {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	today := time.Now().In(loc)
	var first error
	for i := range channels {
		prefix := aeSchedulePrefix[channels[i].ID]
		if prefix == "" {
			continue
		}
		var programs []source.Program
		for day := 0; day < 2; day++ {
			d := today.AddDate(0, 0, day)
			u := fmt.Sprintf("%s/%d/%d/%d/ET", prefix, d.Month(), d.Day(), d.Year())
			body, err := s.getGuide(ctx, SourceAE, u, nil)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			part, err := ParseAESchedule(channels[i].ID, body.Body)
			if err != nil {
				err = s.noteNonJSON(SourceAE, body, err)
			}
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			programs = append(programs, part...)
		}
		if live, ok := s.aeNow(ctx, channels[i].ID); ok && !coversNow(programs, live.Start) {
			programs = append(programs, live)
			sortPrograms(programs)
		}
		if len(programs) > 0 {
			channels[i].Programs = programs
		}
	}
	return first
}

func (s *Service) aeNow(ctx context.Context, id string) (source.Program, bool) {
	body := []byte(`{"operationName":"getNowPlaying","variables":{},"query":` + aeNowPlayingQueryJSON + `}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://yoga.appsvcs.aetnd.com?brand="+id+"&mode=live&platform=web", bytes.NewReader(body))
	if err != nil {
		return source.Program{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("x-apollo-operation-name", "getNowPlaying")
	resp, err := s.client.Do(req)
	if err != nil {
		return source.Program{}, false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return source.Program{}, false
	}
	return ParseAENowPlaying(raw)
}

func (s *Service) fillWarner(ctx context.Context, channels []source.Channel) error {
	cache := map[string]fetched{}
	var first error
	for i := range channels {
		if channels[i].ID == "tcm-east" || channels[i].ID == "tcm-west" {
			programs, err := s.tcmGuide(ctx, channels[i].ID)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			if len(programs) > 0 {
				channels[i].Programs = programs
			}
			continue
		}
		network := warnerNetwork[channels[i].ID]
		if network == "" {
			continue
		}
		body, ok := cache[network]
		if !ok {
			var err error
			body, err = s.getGuide(ctx, SourceWarner, warnerWidgetURL+"?network="+network, map[string]string{"Accept": "text/html,application/json;q=0.9,*/*;q=0.8"})
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			cache[network] = body
		}
		programs, err := ParseWarnerWidget(channels[i].ID, body.Body)
		if err != nil {
			err = s.noteNonJSON(SourceWarner, body, err)
			if first == nil {
				first = err
			}
			continue
		}
		if len(programs) > 0 {
			channels[i].Programs = programs
		}
	}
	return first
}

type fetched struct {
	Status      int
	ContentType string
	Body        []byte
	Header      http.Header
	URL         string
}

func (s *Service) http() *http.Client {
	if s.client != nil {
		return s.client
	}
	return newBrowserClient()
}

func (s *Service) fetch(ctx context.Context, rawURL string, headers map[string]string) (fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetched{}, err
	}
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", browserUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http().Do(req)
	if err != nil {
		return fetched{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	got := fetched{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		Header:      resp.Header.Clone(),
		URL:         rawURL,
	}
	if resp.Request != nil && resp.Request.URL != nil {
		got.URL = resp.Request.URL.String()
	}
	return got, nil
}

func (s *Service) getGuide(ctx context.Context, sourceID, rawURL string, headers map[string]string) (fetched, error) {
	got, err := s.fetch(ctx, rawURL, headers)
	if err != nil {
		return got, err
	}
	if got.Status == http.StatusOK {
		return got, nil
	}
	return got, s.badGuide(sourceID, got, fmt.Errorf("guide HTTP %d", got.Status))
}

func (s *Service) noteNonJSON(sourceID string, got fetched, err error) error {
	if err == nil || looksJSON(got.Body) {
		return err
	}
	return s.badGuide(sourceID, got, err)
}

func (s *Service) badGuide(sourceID string, got fetched, err error) error {
	ct := got.ContentType
	if ct == "" {
		ct = "unknown"
	}
	snip := snippet(got.Body, s.password, s.user)
	if !looksJSON(got.Body) {
		slog.Error("tve guide response was not json", "source", sourceID, "status", got.Status, "content_type", ct, "body", snip)
	}
	return fmt.Errorf("%v (HTTP %d, content-type %s, body %q)", err, got.Status, ct, snip)
}

func looksJSON(body []byte) bool {
	b := bytes.TrimSpace(body)
	if len(b) == 0 || (b[0] != '{' && b[0] != '[') {
		return false
	}
	return json.Valid(b)
}

func snippet(body []byte, secrets ...string) string {
	if len(body) > 512 {
		body = body[:512]
	}
	s := strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, string(body))
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 160 {
		s = string(runes[:160])
	}
	return redact(s, secrets...)
}

func coversNow(programs []source.Program, start time.Time) bool {
	for _, p := range programs {
		if !p.Stop.After(p.Start) {
			continue
		}
		if !start.Before(p.Start) && start.Before(p.Stop) {
			return true
		}
	}
	return false
}

func (s *Service) trySignIn(ctx context.Context) {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	s.trySignInLocked(ctx)
}

// PollActivation checks a browser sign-in that is already in the session
// file. It does not start a scripted login.
func (s *Service) PollActivation(ctx context.Context) {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	st, err := loadState(s.sessionPath)
	if err != nil || st.Activation == nil {
		return
	}
	s.pollActivationLocked(ctx, st)
}

// BeginBrowserSignIn registers an Adobe device code and stores the link a
// person opens in Safari or Firefox. Adobe's redirect returns to the
// network's site. Pluto learns the result by polling the regcode.
func (s *Service) BeginBrowserSignIn(ctx context.Context) error {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	sess, err := s.openAdobe(ctx)
	if err != nil {
		s.setNotice("DirecTV browser sign-in did not start. " + redact(err.Error(), s.password, s.user))
		return err
	}
	st := State{Activation: &Activation{
		Code:          sess.Code,
		URL:           sess.AuthURL,
		Requestor:     sess.Requestor,
		Expires:       sess.CodeExpires,
		ClientID:      sess.ClientID,
		ClientSecret:  sess.ClientSecret,
		AccessToken:   sess.AccessToken,
		DeviceID:      sess.DeviceID,
		Fingerprint:   sess.Fingerprint,
		PassSFP:       sess.PassSFP,
		AccessExpires: sess.AccessExpires,
	}}
	if err := saveState(s.sessionPath, st); err != nil {
		s.setNotice("DirecTV browser sign-in started but the session file was not saved.")
		return err
	}
	s.applyState(st)
	return nil
}

// ImportSession stores a pasted Adobe authn token, or one found in a
// Netscape or JSON cookie export. The result is on the sign-in view.
func (s *Service) ImportSession(raw string) error {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	token, exp, err := sessionMaterial(raw)
	if err != nil {
		s.setNotice(err.Error())
		return err
	}
	requestor, _, _ := s.registration()
	st := State{
		Signed:     true,
		Method:     "token",
		Expires:    exp,
		AuthnToken: token,
		Requestor:  requestor,
	}
	if err := saveState(s.sessionPath, st); err != nil {
		s.setNotice("The Adobe token was valid but the session file was not saved.")
		return err
	}
	s.applyState(st)
	return nil
}

// SetNotice replaces the operator-facing message and leaves the stored
// session alone. The text must already be safe to show.
func (s *Service) SetNotice(msg string) {
	s.signMu.Lock()
	defer s.signMu.Unlock()
	s.setNotice(msg)
}

func (s *Service) setNotice(msg string) {
	s.mu.Lock()
	v := s.signIn
	v.Notice = msg
	s.signIn = v
	s.mu.Unlock()
}

func (s *Service) trySignInLocked(ctx context.Context) {
	st, err := loadState(s.sessionPath)
	if err != nil {
		slog.Warn("tve session", "err", err)
		st = State{}
	}
	if st.Activation != nil {
		s.pollActivationLocked(ctx, st)
		return
	}
	if st.Pending != nil {
		s.applyState(st)
		return
	}
	if st.Signed || st.AuthnToken != "" {
		s.maintainAuthn(ctx, &st)
		if authnCurrent(st) {
			s.applyState(st)
			return
		}
		if st.AuthnToken != "" {
			s.noteAuthnExpired(st)
			return
		}
	}
	_, _, scripted := s.registration()
	if s.user == "" || s.password == "" {
		s.setSignIn(SignInView{Notice: "DirecTV username and password are not set. Lineups are still served; streams that need a media token stay unavailable. Use the DirecTV sign-in page in Safari or Firefox to store a session."})
		return
	}
	if !scripted {
		s.setSignIn(SignInView{Notice: "None of the enabled sources start a scripted DirecTV sign-in. Public stream URLs are served as-is. Browser sign-in is on the DirecTV page."})
		return
	}
	html, finalURL, sess, err := s.fetchSignInPage(ctx)
	if err != nil {
		slog.Error("directv sign-in did not start", "err", redact(err.Error(), s.password, s.user))
		s.setSignIn(SignInView{Notice: "DirecTV sign-in page was not available. The log has the HTTP status, content type, and a short response snippet. Use Safari or Firefox on the DirecTV sign-in page. Lineups stay up."})
		return
	}
	out, err := Login(ctx, s.http(), s.endpoints, html, finalURL, s.user, s.password)
	if err != nil {
		slog.Error("directv sign-in failed", "err", redact(err.Error(), s.password, s.user))
		s.setSignIn(SignInView{Notice: "DirecTV sign-in failed. The log has the HTTP status. identity.directv.com often blocks this process. Use Safari or Firefox on the DirecTV sign-in page. Lineups stay up."})
		return
	}
	if err := s.storeOutcome(out, s.http()); err != nil {
		slog.Error("tve session was not saved", "err", redact(err.Error(), s.password, s.user))
		return
	}
	if sess == nil || out == nil || out.Pending != nil {
		return
	}
	token, pending, err := s.pollAdobe(ctx, sess)
	if err != nil || pending || token == "" {
		return
	}
	saved, err := loadState(s.sessionPath)
	if err != nil {
		return
	}
	saved.AuthnToken = token
	saved.Method = "scripted"
	saved.Signed = true
	adoptClient(&saved, sess)
	if exp := tokenExpiry(token); !exp.IsZero() {
		saved.Expires = exp
	}
	if err := saveState(s.sessionPath, saved); err != nil {
		slog.Error("tve session was not saved", "err", redact(err.Error(), s.password, s.user))
		return
	}
	s.applyState(saved)
}

func (s *Service) pollActivationLocked(ctx context.Context, st State) {
	if st.Activation == nil {
		return
	}
	if !st.Activation.Expires.IsZero() && time.Now().After(st.Activation.Expires) {
		st.Activation = nil
		if err := saveState(s.sessionPath, st); err != nil {
			slog.Error("tve session was not saved", "err", err)
		}
		s.setSignIn(SignInView{Notice: "That DirecTV activation code expired. Start browser sign-in again. Safari or Firefox can finish the provider page."})
		return
	}
	sess := &adobeSession{
		Requestor:    st.Activation.Requestor,
		ClientID:     st.Activation.ClientID,
		ClientSecret: st.Activation.ClientSecret,
		AccessToken:  st.Activation.AccessToken,
		DeviceID:     st.Activation.DeviceID,
		Fingerprint:  st.Activation.Fingerprint,
		PassSFP:      st.Activation.PassSFP,
		Code:         st.Activation.Code,
		CodeExpires:  st.Activation.Expires,
		AuthURL:      st.Activation.URL,
	}
	token, pending, err := s.pollAdobe(ctx, sess)
	if pending {
		s.applyState(st)
		return
	}
	if err != nil || token == "" {
		if err != nil {
			slog.Error("directv activation poll failed", "err", redact(err.Error(), s.password, s.user, st.Activation.AccessToken, st.Activation.ClientSecret, st.Activation.Code))
		}
		s.applyState(st)
		view := s.SignIn()
		view.Notice = "DirecTV has not confirmed the browser sign-in yet. The log has the HTTP status, content type, and a short response snippet when Adobe returned an error. Leave this page open, or start again if the code expired."
		s.setSignIn(view)
		return
	}
	st.Signed = true
	st.Method = "browser"
	st.AuthnToken = token
	st.Requestor = st.Activation.Requestor
	st.ClientID = st.Activation.ClientID
	st.ClientSecret = st.Activation.ClientSecret
	st.AccessToken = st.Activation.AccessToken
	st.AccessExpires = st.Activation.AccessExpires
	st.DeviceID = st.Activation.DeviceID
	st.Fingerprint = st.Activation.Fingerprint
	st.PassSFP = st.Activation.PassSFP
	if st.AccessToken != "" && st.AccessExpires.IsZero() {
		st.AccessExpires = time.Now().Add(accessLifetime(0)).UTC()
	}
	// The DirecTV authentication lives as long as simpleTokenExpires says.
	// A token we cannot read is kept with an empty expiry and refreshed
	// until Adobe rejects it. It is not given a 12-hour stand-in.
	st.Expires = tokenExpiry(token)
	st.Activation = nil
	st.Pending = nil
	if err := saveState(s.sessionPath, st); err != nil {
		slog.Error("tve session was not saved", "err", redact(err.Error(), s.password, s.user, token))
		return
	}
	s.applyState(st)
}

// authnCurrent reports whether the stored DirecTV authentication should
// still be used. A parsed expiry is honored until that instant. A token
// whose expiry could not be read stays in use. A cookie-only scripted
// session with no authentication token is reused for 12 hours from the
// time it was written.
func authnCurrent(st State) bool {
	if !st.Signed && st.AuthnToken == "" {
		return false
	}
	if !st.Expires.IsZero() {
		return time.Now().Before(st.Expires)
	}
	if st.AuthnToken != "" {
		return true
	}
	return st.Signed && !st.Updated.IsZero() && time.Since(st.Updated) < 12*time.Hour
}

func (s *Service) signedNotice(st State) string {
	how := "DirecTV session is stored"
	switch st.Method {
	case "browser":
		how = "DirecTV session from Safari or Firefox is stored"
	case "token":
		how = "Pasted Adobe authentication token is stored"
	case "scripted":
		how = "Scripted DirecTV session is stored"
	}
	when := "Sign in again from the DirecTV page if playback starts failing."
	if st.AuthnToken != "" && st.Expires.IsZero() {
		when = "The authentication token did not include a readable expiry, so this session stays until Adobe rejects it."
	}
	if !st.Expires.IsZero() {
		when = fmt.Sprintf("DirecTV authentication expires %s.", clock(st.Expires))
	}
	switch s.renewalKind(st) {
	case renewalClient:
		return how + ". " + when + " The Adobe client token refreshes on its own until then. Start browser sign-in again in Safari or Firefox before that date. The scripted password login cannot extend it."
	case renewalRetry:
		return how + ". " + when + " Refreshing the Adobe client token failed and will be retried. The DirecTV sign-in is still stored."
	default:
		if st.ClientID == "" {
			return how + ". " + when + " This file has no Adobe client identity, so headless renewal cannot run. Start browser sign-in once more on this page; after that the client token refreshes on its own until the DirecTV authentication expires."
		}
		return how + ". " + when + " Start browser sign-in again in Safari or Firefox before then. Silent refresh cannot extend the DirecTV login itself."
	}
}

func (s *Service) noteAuthnExpired(st State) {
	when := "DirecTV authentication has expired."
	if !st.Expires.IsZero() {
		when = fmt.Sprintf("DirecTV authentication expired %s.", clock(st.Expires))
	}
	s.setSignIn(SignInView{
		Method:  st.Method,
		Expires: st.Expires,
		Renewal: renewalBrowser,
		Notice:  when + " Start browser sign-in on this page in Safari or Firefox. The scripted password login cannot extend it.",
	})
}

func (s *Service) storeOutcome(out *Outcome, client *http.Client) error {
	st := State{}
	if out != nil && out.Pending != nil {
		st.Pending = out.Pending
		s.applyState(st)
		return saveState(s.sessionPath, st)
	}
	st.Signed = true
	st.Method = "scripted"
	st.Expires = time.Now().Add(12 * time.Hour).UTC()
	if out != nil {
		st.FinalURL = out.FinalURL
	}
	st.Cookies = exportCookies(client)
	s.applyState(st)
	return saveState(s.sessionPath, st)
}

func (s *Service) applyState(st State) {
	view := SignInView{Signed: st.Signed, Method: st.Method, Expires: st.Expires}
	switch {
	case st.Activation != nil:
		view.Waiting = true
		view.Signed = false
		view.ActivationURL = st.Activation.URL
		view.Code = st.Activation.Code
		view.Expires = st.Activation.Expires
		view.Notice = "Open the activation link in Safari or Firefox and sign in to DirecTV there. Adobe sends the browser back to the network's own site, not to Pluto. This page checks for the finished sign-in on its own. Start again if the code expires."
	case st.Pending != nil:
		view.Pending = true
		view.Signed = false
		view.Prompt = st.Pending.Prompt
		view.Notice = "DirecTV needs another step. Submit the code or menu choice. A browser sign-in in Safari or Firefox does not use this prompt."
	case st.Signed:
		view.Notice = s.signedNotice(st)
		view.Renewal = s.renewalKind(st)
	}
	s.setSignIn(view)
}

func (s *Service) setSignIn(v SignInView) {
	s.mu.Lock()
	s.signIn = v
	s.mu.Unlock()
}

// aeNowPlayingQueryJSON is a JSON string. The live endpoint returns a union,
// so each programme kind is requested with its own fields.
const aeNowPlayingQueryJSON = `"query getNowPlaying { nowPlaying { startTime endTime program { __typename ... on Episode { title description series { title } } ... on Movie { title description } ... on Special { title description } } } }"`

var aeSchedulePrefix = map[string]string{
	"history":  "https://www.history.com/schedule/data",
	"aetv":     "https://www.aetv.com/schedule/data",
	"lifetime": "https://www.mylifetime.com/schedule/data",
	"fyi":      "https://www.aetv.com/fyi/schedule/data",
}

var warnerNetwork = map[string]string{
	"tnt-east": "TNT",
	"tnt-west": "TNT",
	"tbs-east": "TBS",
	"tbs-west": "TBS",
	"tru-east": "TRUTV",
	"tru-west": "TRUTV",
}
