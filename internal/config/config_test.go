package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PLUTO_EMAIL", "PLUTO_PASSWORD", "PLUTO_CONFIG",
		"PORT", "START_CHANNEL", "TUNER_COUNT", "DEVICE_ID_FILE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadRequiresCredentials(t *testing.T) {
	clearConfigEnv(t)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PLUTO_EMAIL") {
		t.Fatalf("expected missing email, got %v", err)
	}
	t.Setenv("PLUTO_EMAIL", "user@example.com")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PLUTO_PASSWORD") {
		t.Fatalf("expected missing password, got %v", err)
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PLUTO_EMAIL", " user@example.com ")
	t.Setenv("PLUTO_PASSWORD", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Email != "user@example.com" || cfg.Password != "secret" {
		t.Fatalf("credentials: %+v", cfg)
	}
	if cfg.Port != 8080 || cfg.StartChannel != 10000 || cfg.TunerCount != 12 {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.DeviceIDFile != defaultDeviceIDFile {
		t.Fatalf("device file: %s", cfg.DeviceIDFile)
	}
	if cfg.RefreshEvery != defaultRefresh {
		t.Fatalf("refresh: %s", cfg.RefreshEvery)
	}

	t.Setenv("PORT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected port 0 to be rejected")
	}
	t.Setenv("PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatal("expected port 70000 to be rejected")
	}
	t.Setenv("PORT", "8081")
	t.Setenv("TUNER_COUNT", "13")
	if _, err := Load(); err == nil {
		t.Fatal("expected tuner count 13 to be rejected")
	}
	t.Setenv("TUNER_COUNT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected tuner count 0 to be rejected")
	}
	t.Setenv("TUNER_COUNT", "2")
	t.Setenv("START_CHANNEL", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected negative start channel to be rejected")
	}
	t.Setenv("START_CHANNEL", "nope")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "START_CHANNEL") {
		t.Fatalf("expected int error, got %v", err)
	}
}

func TestLoadFileQuotesExportAndEnvOverride(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "pluto.conf")
	body := "\uFEFF# comment\n" +
		"export PLUTO_EMAIL=\"user@example.com\"\n" +
		"PLUTO_PASSWORD='p@ss w0rd'\n" +
		"PORT=\"9090\"\n" +
		"START_CHANNEL=500\n" +
		"TUNER_COUNT=2\n" +
		"DEVICE_ID_FILE=\"/data/devices.conf\"\n" +
		"not a pair\n" +
		"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLUTO_CONFIG", path)
	t.Setenv("PORT", "9091")       // env wins
	t.Setenv("PLUTO_PASSWORD", "") // empty env falls through to the file

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Email != "user@example.com" {
		t.Fatalf("email %q", cfg.Email)
	}
	if cfg.Password != "p@ss w0rd" {
		t.Fatalf("password %q", cfg.Password)
	}
	if cfg.Port != 9091 || cfg.StartChannel != 500 || cfg.TunerCount != 2 {
		t.Fatalf("parsed: %+v", cfg)
	}
	if cfg.DeviceIDFile != "/data/devices.conf" {
		t.Fatalf("device file %q", cfg.DeviceIDFile)
	}
}

func TestLoadQuotedPasswordKeepsEscapes(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "pluto.conf")
	body := "PLUTO_EMAIL=a@b.c\nPLUTO_PASSWORD=\"a\\\"b\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLUTO_CONFIG", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != `a"b` {
		t.Fatalf("password %q", cfg.Password)
	}
}

func TestLoadExplicitConfigMustExist(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PLUTO_CONFIG", filepath.Join(t.TempDir(), "missing.conf"))
	t.Setenv("PLUTO_EMAIL", "a@b.c")
	t.Setenv("PLUTO_PASSWORD", "x")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing PLUTO_CONFIG to fail")
	}
}

func TestConfigPathUnsetWhenNoDefaultFile(t *testing.T) {
	clearConfigEnv(t)
	path, explicit := configPath()
	if explicit {
		t.Fatal("expected implicit path")
	}
	if path != "" {
		t.Fatalf("found unexpected config %s", path)
	}
}
