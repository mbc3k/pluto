package tve

import (
	"bytes"
	"context"
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
type SignInView struct {
	Signed  bool
	Pending bool
	Prompt  string
	Notice  string
}

// Service refreshes enabled TV Everywhere sources and remembers a DirecTV
// session. A failed refresh keeps the previous guide.
type Service struct {
	sources     []string
	combined    bool
	start       int
	user        string
	password    string
	sessionPath string
	every       time.Duration
	client      *http.Client
	adobeBase   string
	endpoints   Endpoints

	mu     sync.Mutex
	feeds  map[string]source.Feed
	signIn SignInView
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
		s.keepGuides(id, channels)
		if err := s.fillGuide(ctx, id, channels); err != nil {
			slog.Error("tve guide refresh failed", "source", id, "err", redact(err.Error(), s.password, s.user))
			s.keepGuides(id, channels)
		}
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

func (s *Service) fillGuide(ctx context.Context, id string, channels []source.Channel) error {
	switch id {
	case SourceAE:
		return s.fillAE(ctx, channels)
	case SourceWarner:
		return s.fillWarner(ctx, channels)
	default:
		return nil
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
			body, err := s.get(ctx, u)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			part, err := ParseAESchedule(channels[i].ID, body)
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
	if len(channels) > 0 && first != nil && len(channels[0].Programs) == 0 {
		return first
	}
	return nil
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
	cache := map[string][]byte{}
	var first error
	for i := range channels {
		network := warnerNetwork[channels[i].ID]
		if network == "" {
			continue
		}
		body, ok := cache[network]
		if !ok {
			var err error
			body, err = s.get(ctx, "https://tnets-dvs-schedule.wme-digital.com/?network="+network)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			cache[network] = body
		}
		programs, err := ParseWarnerWidget(channels[i].ID, body)
		if err != nil {
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

func (s *Service) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json,*/*")
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("guide HTTP %d", resp.StatusCode)
	}
	return body, nil
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
	if s.user == "" || s.password == "" {
		s.setSignIn(SignInView{Notice: "DirecTV username and password are not set. Lineups are still served; streams that need a media token stay unavailable."})
		return
	}
	st, err := loadState(s.sessionPath)
	if err != nil {
		slog.Warn("tve session", "err", err)
		st = State{}
	}
	if st.Pending != nil {
		s.applyState(st)
		return
	}
	if st.Signed && time.Since(st.Updated) < 12*time.Hour {
		s.applyState(st)
		return
	}
	requestor := ""
	for _, id := range s.sources {
		if requestor = adobeRequestor(id); requestor != "" {
			break
		}
	}
	if requestor == "" {
		s.setSignIn(SignInView{Notice: "None of the enabled sources start a DirecTV sign-in. Public stream URLs are served as-is."})
		return
	}
	pageURL := strings.TrimRight(s.adobeBase, "/") + "/api/v2/" + requestor + "/sessions?mso_id=DTV"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", browserUA)
	resp, err := s.client.Do(req)
	if err != nil {
		slog.Error("directv sign-in did not start", "err", redact(err.Error(), s.password, s.user))
		s.setSignIn(SignInView{Notice: "DirecTV sign-in did not start. The previous session, if any, is unchanged."})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	finalURL := pageURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if resp.StatusCode >= 400 {
		slog.Error("directv sign-in page failed", "status", resp.StatusCode)
		s.setSignIn(SignInView{Notice: "DirecTV sign-in page was not available. Lineups stay up."})
		return
	}
	out, err := Login(ctx, s.client, s.endpoints, string(body), finalURL, s.user, s.password)
	if err != nil {
		slog.Error("directv sign-in failed", "err", redact(err.Error(), s.password, s.user))
		s.setSignIn(SignInView{Notice: "DirecTV sign-in failed. Check the log for the status code, then try again on the next refresh."})
		return
	}
	if err := s.storeOutcome(out, s.client); err != nil {
		slog.Error("tve session was not saved", "err", redact(err.Error(), s.password, s.user))
	}
}

func (s *Service) storeOutcome(out *Outcome, client *http.Client) error {
	st := State{}
	if out != nil && out.Pending != nil {
		st.Pending = out.Pending
		s.applyState(st)
		return saveState(s.sessionPath, st)
	}
	st.Signed = true
	if out != nil {
		st.FinalURL = out.FinalURL
	}
	st.Cookies = exportCookies(client)
	s.applyState(st)
	return saveState(s.sessionPath, st)
}

func (s *Service) applyState(st State) {
	view := SignInView{Signed: st.Signed}
	if st.Pending != nil {
		view.Pending = true
		view.Signed = false
		view.Prompt = st.Pending.Prompt
		view.Notice = "DirecTV needs another step. Open the sign-in page and submit the code or menu choice."
	} else if st.Signed {
		view.Notice = "DirecTV sign-in is stored. Network playback that needs a media token still has to be checked with a real account."
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
