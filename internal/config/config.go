package config

import (
	"bufio"
	"fmt"
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
	defaultDeviceIDFile = "/opt/local/etc/pluto-devices.conf"
	defaultConfFile     = "/opt/local/etc/pluto.conf"
)

// Config holds all runtime configuration for the pluto service.
type Config struct {
	Email        string
	Password     string
	StartChannel int
	Port         int
	TunerCount   int
	RefreshEvery time.Duration
	DeviceIDFile string
}

// Load reads configuration from environment variables, falling back to
// the flat key=value file at defaultConfFile for any missing values.
func Load() (*Config, error) {
	file := make(map[string]string)
	if f, err := os.Open(defaultConfFile); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			idx := strings.IndexByte(line, '=')
			if idx < 0 {
				continue
			}
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			file[k] = v
		}
	}

	get := func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return file[key]
	}

	getInt := func(key string, def int) (int, error) {
		s := get(key)
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
		Email:        get("PLUTO_EMAIL"),
		Password:     get("PLUTO_PASSWORD"),
		RefreshEvery: defaultRefresh,
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

	if s := get("DEVICE_ID_FILE"); s != "" {
		cfg.DeviceIDFile = s
	}

	return cfg, nil
}
