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
	"strings"
	"sync"
	"time"

	"github.com/mbc3k/pluto/internal/config"
	"github.com/mbc3k/pluto/internal/pluto"
)

const (
	bootURL          = "https://boot.pluto.tv/v4/start"
	appName          = "web"
	appVersion       = "8.0.0-111b2b9dc00bd0bea9030b30662159ed9e7c8bc6"
	deviceVersion    = "122.0.0"
	deviceModel      = "web"
	deviceMake       = "chrome"
	deviceType       = "web"
	clientModelNumber = "1.0.0"

	// tokenRefreshMargin: re-auth sessions expiring within this window.
	tokenRefreshMargin = 4 * time.Hour
	// tokenTTLFallback: assumed JWT lifetime when the token's exp claim cannot be parsed.
	tokenTTLFallback = 24 * time.Hour
)

// Session represents a single authenticated Pluto TV tuner session.
type Session struct {
	mu          sync.Mutex
	index       int
	deviceID    string
	token       string
	stitcherParams string
	tokenExpiry time.Time
	cfg         *config.Config
	client      *pluto.RetryClient
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
	params := url.Values{
		"appName":          {appName},
		"appVersion":       {appVersion},
		"deviceVersion":    {deviceVersion},
		"deviceModel":      {deviceModel},
		"deviceMake":       {deviceMake},
		"deviceType":       {deviceType},
		"clientID":         {s.deviceID},
		"clientModelNumber": {clientModelNumber},
		"serverSideAds":    {"false"},
		"drmCapabilities":  {"widevine:L3"},
		"username":         {s.cfg.Email},
		"password":         {s.cfg.Password},
	}
	u := bootURL + "?" + params.Encode()

	resp, err := s.client.Get(ctx, u, nil)
	if err != nil {
		return fmt.Errorf("tuner %d boot: %w", s.index, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("tuner %d boot HTTP %d: %s", s.index, resp.StatusCode, body)
	}

	var auth pluto.AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return fmt.Errorf("tuner %d decode boot response: %w", s.index, err)
	}
	if auth.SessionToken == "" {
		return fmt.Errorf("tuner %d: empty sessionToken in boot response", s.index)
	}

	s.token = auth.SessionToken
	s.stitcherParams = auth.StitcherParams
	if exp, ok := parseJWTExpiry(s.token); ok {
		s.tokenExpiry = exp
	} else {
		slog.Warn("could not parse JWT exp, using fallback TTL", "index", s.index)
		s.tokenExpiry = time.Now().Add(tokenTTLFallback)
	}
	slog.Info("tuner authenticated", "index", s.index)
	return nil
}

// EnsureFresh re-authenticates the session if its token is expiring soon.
func (s *Session) EnsureFresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Add(tokenRefreshMargin).After(s.tokenExpiry) {
		return s.authenticate(ctx)
	}
	return nil
}

// Token returns the current JWT for use in stream URLs.
func (s *Session) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
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
// Returns the expiry time and true on success, or zero/false if the token
// cannot be parsed or contains an implausible timestamp.
func parseJWTExpiry(token string) (time.Time, bool) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	exp := time.Unix(claims.Exp, 0)
	now := time.Now()
	if exp.Before(now) || exp.After(now.Add(7*24*time.Hour)) {
		return time.Time{}, false
	}
	return exp, true
}

// loadOrCreateDeviceIDs reads device IDs from path, generating and persisting
// any that are missing. Returns exactly count IDs.
func loadOrCreateDeviceIDs(path string, count int) ([]string, error) {
	var existing []string

	f, err := os.Open(path)
	if err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				existing = append(existing, line)
			}
		}
		f.Close()
	}

	if len(existing) >= count {
		return existing[:count], nil
	}

	// Generate missing IDs.
	needed := count - len(existing)
	newIDs := make([]string, needed)
	for i := range newIDs {
		id, err := newUUID()
		if err != nil {
			return nil, fmt.Errorf("generate UUID: %w", err)
		}
		newIDs[i] = id
	}

	// Append to file, creating it if necessary.
	af, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer af.Close()
	for _, id := range newIDs {
		if _, err := fmt.Fprintln(af, id); err != nil {
			return nil, fmt.Errorf("write device ID: %w", err)
		}
	}

	return append(existing, newIDs...), nil
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
