package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultStartChannel = 10000
	defaultPort         = 8080
	defaultTunerCount   = 12
	defaultRefresh      = 3 * time.Hour
	defaultGuideHours   = 24
	minGuideHours       = 6
	maxGuideHours       = 72
	minRefresh          = time.Minute
	maxAccessTokenLen   = 128
	defaultDeviceIDFile = "/opt/local/etc/pluto-devices.conf"
)

// defaultConfPaths is searched, in order, when PLUTO_CONFIG is unset.
// The first regular file that exists wins. Environment variables override it.
var defaultConfPaths = []string{
	"/opt/local/etc/pluto.conf",
	"/usr/local/etc/pluto.conf",
	"/etc/pluto.conf",
}

// Config holds all runtime configuration for the pluto service.
type Config struct {
	Email        string
	Password     string
	StartChannel int
	Port         int
	BindAddr     string // host or IP only; empty listens on all interfaces
	AccessToken  string // empty leaves playlist and EPG URLs open
	TunerCount   int
	RefreshEvery time.Duration
	GuideHours   int
	DeviceIDFile string
}

// ListenAddr is the address passed to net.Listen. An empty BindAddr keeps
// the historical ":port" form (all interfaces). IPv6 hosts are bracketed.
func (c *Config) ListenAddr() string {
	port := strconv.Itoa(c.Port)
	host := strings.TrimSpace(c.BindAddr)
	if host == "" {
		return ":" + port
	}
	return net.JoinHostPort(host, port)
}

// Load reads configuration from environment variables, falling back to a
// flat key=value file for any value the environment does not set.
func Load() (*Config, error) {
	path, explicit := configPath()
	file := map[string]string{}
	if path != "" {
		parsed, err := readConfFile(path)
		if err != nil {
			if explicit || !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		} else {
			file = parsed
		}
	}

	// Environment wins. An empty environment variable is treated as unset so
	// a blank shell export does not mask the file.
	get := func(key string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return file[key]
	}

	getInt := func(key string, def int) (int, error) {
		s := strings.TrimSpace(get(key))
		if s == "" {
			return def, nil
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", key, err)
		}
		return v, nil
	}

	cfg := &Config{
		Email:        strings.TrimSpace(get("PLUTO_EMAIL")),
		Password:     get("PLUTO_PASSWORD"),
		DeviceIDFile: defaultDeviceIDFile,
	}

	if cfg.Email == "" {
		return nil, fmt.Errorf("PLUTO_EMAIL is required")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("PLUTO_PASSWORD is required")
	}

	var err error
	if cfg.StartChannel, err = getInt("START_CHANNEL", defaultStartChannel); err != nil {
		return nil, err
	}
	if cfg.Port, err = getInt("PORT", defaultPort); err != nil {
		return nil, err
	}
	if cfg.TunerCount, err = getInt("TUNER_COUNT", defaultTunerCount); err != nil {
		return nil, err
	}
	if cfg.TunerCount < 1 || cfg.TunerCount > 12 {
		return nil, fmt.Errorf("TUNER_COUNT must be between 1 and 12, got %d", cfg.TunerCount)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("PORT must be between 1 and 65535, got %d", cfg.Port)
	}
	if cfg.StartChannel < 0 {
		return nil, fmt.Errorf("START_CHANNEL must be >= 0, got %d", cfg.StartChannel)
	}

	if s := strings.TrimSpace(get("DEVICE_ID_FILE")); s != "" {
		cfg.DeviceIDFile = s
	}
	if cfg.BindAddr, err = normalizeBindAddr(get("BIND_ADDR")); err != nil {
		return nil, err
	}
	if cfg.AccessToken, err = normalizeAccessToken(get("ACCESS_TOKEN")); err != nil {
		return nil, err
	}
	if cfg.RefreshEvery, err = parseRefreshEvery(get("REFRESH_EVERY")); err != nil {
		return nil, err
	}
	if cfg.GuideHours, err = parseGuideHours(get("GUIDE_HOURS")); err != nil {
		return nil, err
	}

	return cfg, nil
}

// normalizeBindAddr accepts a host or IP and rejects anything that already
// includes a port. Empty means all interfaces. Brackets around an IPv6
// address are stripped so ListenAddr can bracket it exactly once.
func normalizeBindAddr(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return "", fmt.Errorf("BIND_ADDR must be a host or IP without a port, got %q", s)
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	if s == "" || strings.ContainsAny(s, " \t/?#") {
		return "", fmt.Errorf("BIND_ADDR is not a host")
	}
	return s, nil
}

// normalizeAccessToken accepts an empty token (gate off) or a short secret
// that is safe to place in a URL path and query string. The value itself is
// never included in the error.
func normalizeAccessToken(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > maxAccessTokenLen {
		return "", fmt.Errorf("ACCESS_TOKEN must be at most %d characters", maxAccessTokenLen)
	}
	for _, r := range s {
		if r <= ' ' || r == '/' || r == '?' || r == '#' || r == '&' || r == '%' {
			return "", fmt.Errorf("ACCESS_TOKEN contains a character that cannot appear in a URL path or query")
		}
	}
	return s, nil
}

func parseRefreshEvery(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultRefresh, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("REFRESH_EVERY: %w", err)
	}
	if d < minRefresh {
		return 0, fmt.Errorf("REFRESH_EVERY must be at least %s, got %s", minRefresh, d)
	}
	return d, nil
}

// parseGuideHours accepts 6–72. Values that are not a multiple of the 6-hour
// Pluto window are rounded up, and 72 is the cap.
func parseGuideHours(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultGuideHours, nil
	}
	h, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("GUIDE_HOURS: %w", err)
	}
	if h < minGuideHours || h > maxGuideHours {
		return 0, fmt.Errorf("GUIDE_HOURS must be between %d and %d, got %d", minGuideHours, maxGuideHours, h)
	}
	rounded := ((h + 5) / 6) * 6
	if rounded > maxGuideHours {
		rounded = maxGuideHours
	}
	if rounded != h {
		slog.Info("GUIDE_HOURS rounded up to a multiple of 6", "requested", h, "using", rounded)
	}
	return rounded, nil
}

// configPath returns the configuration file to read. explicit is true when
// the caller set PLUTO_CONFIG; a missing file is then an error.
func configPath() (path string, explicit bool) {
	if p := strings.TrimSpace(os.Getenv("PLUTO_CONFIG")); p != "" {
		return p, true
	}
	for _, candidate := range defaultConfPaths {
		st, err := os.Stat(candidate)
		if err == nil && !st.IsDir() {
			return candidate, false
		}
	}
	return "", false
}

func readConfFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	file := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "\uFEFF")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		if k == "" {
			continue
		}
		file[k] = parseValue(line[idx+1:])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return file, nil
}

// parseValue trims surrounding space and removes one matching pair of quotes.
// Double quotes honor Go-style escapes so a password can contain a quote.
func parseValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 2 {
		return v
	}
	quote := v[0]
	if (quote != '"' && quote != '\'') || v[len(v)-1] != quote {
		return v
	}
	if quote == '"' {
		if u, err := strconv.Unquote(v); err == nil {
			return u
		}
	}
	return v[1 : len(v)-1]
}
