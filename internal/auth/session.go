package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/pluto"
)

const (
	defaultBootURL    = "https://boot.pluto.tv/v4/start"
	appName           = "web"
	appVersion        = "8.0.0-111b2b9dc00bd0bea9030b30662159ed9e7c8bc6"
	deviceVersion     = "122.0.0"
	deviceModel       = "web"
	deviceMake        = "chrome"
	deviceType        = "web"
	clientModelNumber = "1.0.0"

	// tokenRefreshMargin: re-auth sessions expiring within this window.
	tokenRefreshMargin = 1 * time.Hour
	// tokenTTLFallback: assumed JWT lifetime when the token's exp claim cannot be parsed.
	tokenTTLFallback = 24 * time.Hour
	// maxTokenAge: maximum time a JWT may be embedded in stream URLs before forcing
	// re-auth, regardless of the exp claim. Measured from issuance, not from the
	// last time a playlist was handed out — otherwise regular Channels polling
	// would keep a stale token in circulation indefinitely.
	maxTokenAge = 2 * time.Hour
)

// uuidPattern matches the canonical 8-4-4-4-12 form. Device IDs that do not
// match are discarded so a corrupt line cannot become a tuner identity.
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Session represents a single authenticated Pluto TV tuner session.
type Session struct {
	mu       sync.Mutex
	index    int
	deviceID string
	token    string
	// endpoint overrides defaultBootURL in tests.
	endpoint       string
	stitcherParams string
	tokenExpiry    time.Time
	// issuedAt is the time of the last successful authenticate. It is not
	// updated when credentials are handed to a playlist request.
	issuedAt time.Time
	// tokenGen increments on every successful authenticate so callers can
	// cheaply detect when a previously generated M3U must be rebuilt.
	tokenGen int64
	cfg      *config.Config
	client   *pluto.RetryClient
}

// NewSessions loads or creates device IDs and returns one Session per tuner.
func NewSessions(cfg *config.Config, client *pluto.RetryClient) ([]*Session, error) {
	deviceIDs, err := loadOrCreateDeviceIDs(cfg.DeviceIDFile, cfg.TunerCount)
	if err != nil {
		return nil, fmt.Errorf("device IDs: %w", err)
	}

	sessions := make([]*Session, cfg.TunerCount)
	for i := 0; i < cfg.TunerCount; i++ {
		sessions[i] = &Session{
			index:    i,
			deviceID: deviceIDs[i],
			cfg:      cfg,
			client:   client,
		}
	}
	return sessions, nil
}

// Authenticate performs the Pluto TV boot request and stores the JWT.
func (s *Session) Authenticate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authenticate(ctx)
}

// authenticate is the internal (lock-held) implementation.
func (s *Session) authenticate(ctx context.Context) error {
	endpoint := s.endpoint
	if endpoint == "" {
		endpoint = defaultBootURL
	}
	params := url.Values{
		"appName":           {appName},
		"appVersion":        {appVersion},
		"deviceVersion":     {deviceVersion},
		"deviceModel":       {deviceModel},
		"deviceMake":        {deviceMake},
		"deviceType":        {deviceType},
		"clientID":          {s.deviceID},
		"clientModelNumber": {clientModelNumber},
		"serverSideAds":     {"false"},
		"drmCapabilities":   {"widevine:L3"},
		"username":          {s.cfg.Email},
		"password":          {s.cfg.Password},
	}
	u := endpoint + "?" + params.Encode()

	resp, err := s.client.Get(ctx, u, nil)
	if err != nil {
		return fmt.Errorf("boot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("boot HTTP %d: %s", resp.StatusCode, redactSecrets(string(body), s.cfg.Password, s.cfg.Email))
	}

	var auth pluto.AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return fmt.Errorf("decode boot response: %w", err)
	}
	if auth.SessionToken == "" {
		return fmt.Errorf("empty sessionToken in boot response")
	}

	s.token = auth.SessionToken
	s.stitcherParams = auth.StitcherParams
	if exp, ok := parseJWTExpiry(s.token); ok {
		s.tokenExpiry = exp
	} else {
		slog.Warn("could not parse JWT exp, using fallback TTL", "tuner", s.index+1)
		s.tokenExpiry = time.Now().Add(tokenTTLFallback)
	}
	s.issuedAt = time.Now()
	s.tokenGen++
	slog.Info("tuner authenticated",
		"tuner", s.index+1,
		"tokenGen", s.tokenGen,
		"expires", s.tokenExpiry.UTC().Format(time.RFC3339))
	return nil
}

// needsRefresh reports whether the session must re-authenticate.
// Caller must hold s.mu, or be the only user of s.
func (s *Session) needsRefresh(now time.Time) bool {
	if s.token == "" || s.issuedAt.IsZero() {
		return true
	}
	if now.Add(tokenRefreshMargin).After(s.tokenExpiry) {
		return true
	}
	return now.Sub(s.issuedAt) > maxTokenAge
}

// EnsureFresh re-authenticates the session if its token is expiring soon
// or was issued more than maxTokenAge ago.
func (s *Session) EnsureFresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.needsRefresh(time.Now()) {
		return nil
	}
	return s.authenticate(ctx)
}

// Credentials returns the JWT and stitcherParams under a single lock.
// gen is a monotonic counter that changes only on re-authentication; callers
// use it to reuse a previously built M3U when the embedded credentials have
// not changed. Handing credentials out does not extend the token's age.
func (s *Session) Credentials() (token, stitcherParams string, gen int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token, s.stitcherParams, s.tokenGen
}

// Token returns the current JWT for use in stream URLs.
func (s *Session) Token() string {
	token, _, _ := s.Credentials()
	return token
}

// StitcherParams returns the stitcherParams string from the boot response.
func (s *Session) StitcherParams() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stitcherParams
}

// DeviceID returns the stable device identifier for this session.
func (s *Session) DeviceID() string {
	return s.deviceID // immutable after construction
}

// parseJWTExpiry decodes the exp claim from a JWT token string.
// Returns the expiry time and true on success, including when the token is
// already expired — callers use that to refresh. Returns false only when the
// token cannot be parsed or the timestamp is implausible.
func parseJWTExpiry(token string) (time.Time, bool) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		slog.Debug("JWT parse failed: not 3 parts")
		return time.Time{}, false
	}
	payload, err := decodeJWTSegment(parts[1])
	if err != nil {
		slog.Debug("JWT parse failed: base64 decode error", "err", err)
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		slog.Debug("JWT parse failed: no exp claim or unmarshal error", "err", err)
		return time.Time{}, false
	}
	exp := time.Unix(claims.Exp, 0)
	now := time.Now()
	// Allow recently-expired tokens through so EnsureFresh refreshes them
	// instead of assuming the 24h fallback TTL. Reject only nonsense.
	if exp.Before(now.Add(-48*time.Hour)) || exp.After(now.Add(7*24*time.Hour)) {
		slog.Debug("JWT parse rejected: implausible exp", "exp", exp, "now", now)
		return time.Time{}, false
	}
	return exp, true
}

func decodeJWTSegment(seg string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(seg); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(seg)
}

func redactSecrets(msg, password, email string) string {
	replacer := func(secret string) {
		if secret == "" {
			return
		}
		msg = strings.ReplaceAll(msg, secret, "REDACTED")
		if esc := url.QueryEscape(secret); esc != secret {
			msg = strings.ReplaceAll(msg, esc, "REDACTED")
		}
	}
	replacer(password)
	replacer(email)
	return msg
}

// loadOrCreateDeviceIDs reads device IDs from path, generating and persisting
// any that are missing. Returns exactly count IDs. IDs beyond count are kept
// on disk so lowering TUNER_COUNT and raising it again reuses the same devices.
func loadOrCreateDeviceIDs(path string, count int) ([]string, error) {
	if count < 1 {
		return nil, fmt.Errorf("tuner count must be positive")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create device id directory: %w", err)
		}
	}

	raw, err := readDeviceIDLines(path)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(raw))
	ids := make([]string, 0, count)
	dropped := 0
	for _, line := range raw {
		line = strings.TrimPrefix(strings.TrimSpace(line), "\uFEFF")
		if line == "" {
			continue
		}
		if !uuidPattern.MatchString(line) {
			dropped++
			continue
		}
		key := strings.ToLower(line)
		if _, ok := seen[key]; ok {
			dropped++
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, line)
	}

	added := 0
	attempts := 0
	for len(ids) < count {
		attempts++
		if attempts > count+100 {
			return nil, fmt.Errorf("could not generate enough device IDs")
		}
		id, err := newUUID()
		if err != nil {
			return nil, fmt.Errorf("generate UUID: %w", err)
		}
		if _, ok := seen[strings.ToLower(id)]; ok {
			continue
		}
		seen[strings.ToLower(id)] = struct{}{}
		ids = append(ids, id)
		added++
	}

	if added > 0 || dropped > 0 {
		if err := writeDeviceIDsAtomic(path, ids); err != nil {
			return nil, err
		}
		if dropped > 0 {
			slog.Warn("device id file had invalid or duplicate lines; rewrote it", "path", path, "dropped", dropped)
		}
		if added > 0 {
			slog.Info("persisted new device ids", "path", path, "added", added, "stored", len(ids))
		}
	}

	slog.Info("device ids loaded", "path", path, "tuners", count, "stored", len(ids))
	return ids[:count], nil
}

func readDeviceIDLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return lines, nil
}

func writeDeviceIDsAtomic(path string, ids []string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".pluto-devices-*.tmp")
	if err != nil {
		return fmt.Errorf("temp device id file: %w", err)
	}
	tmp := f.Name()
	success := false
	defer func() {
		if !success {
			os.Remove(tmp)
		}
	}()

	for _, id := range ids {
		if _, err := fmt.Fprintln(f, id); err != nil {
			f.Close()
			return fmt.Errorf("write device ID: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync device ID file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close device ID file: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("chmod device ID file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace device ID file: %w", err)
	}
	success = true
	if dirf, err := os.Open(dir); err == nil {
		_ = dirf.Sync()
		dirf.Close()
	}
	return nil
}

// newUUID generates a random UUID v4.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
