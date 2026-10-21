package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PLUTO_EMAIL", "PLUTO_PASSWORD", "PLUTO_CONFIG",
		"PORT", "START_CHANNEL", "TUNER_COUNT", "DEVICE_ID_FILE",
		"BIND_ADDR", "ACCESS_TOKEN", "REFRESH_EVERY", "GUIDE_HOURS",
		"TVE_SOURCES", "TVE_PROVIDER", "TVE_COMBINED",
		"TVE_START_CHANNEL", "TVE_SESSION_FILE", "DIRECTV_USERNAME", "DIRECTV_PASSWORD",
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
	if cfg.BindAddr != "" || cfg.AccessToken != "" || cfg.GuideHours != defaultGuideHours {
		t.Fatalf("ops defaults: %+v", cfg)
	}
	if cfg.ListenAddr() != ":8080" {
		t.Fatalf("listen %s", cfg.ListenAddr())
	}
	if cfg.TVEProvider != "" || len(cfg.TVESources) != 0 || !cfg.TVECombined {
		t.Fatalf("tve defaults: %+v", cfg)
	}
	if cfg.TVEStartChannel != 20000 || cfg.TVESessionFile != "/opt/local/etc/pluto-tve-session.json" {
		t.Fatalf("tve session defaults: %+v", cfg)
	}
	if cfg.DirectvUsername != "" || cfg.DirectvPassword != "" {
		t.Fatal("directv credentials should default empty")
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

func TestLoadOpsKnobs(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PLUTO_EMAIL", "a@b.c")
	t.Setenv("PLUTO_PASSWORD", "x")
	t.Setenv("PORT", "9090")
	t.Setenv("BIND_ADDR", "127.0.0.1")
	t.Setenv("REFRESH_EVERY", "3h")
	t.Setenv("GUIDE_HOURS", "48")
	t.Setenv("ACCESS_TOKEN", "s3cret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "127.0.0.1" || cfg.ListenAddr() != "127.0.0.1:9090" {
		t.Fatalf("bind %q listen %s", cfg.BindAddr, cfg.ListenAddr())
	}
	if cfg.RefreshEvery != 3*time.Hour || cfg.GuideHours != 48 || cfg.AccessToken != "s3cret" {
		t.Fatalf("knobs: %+v", cfg)
	}

	t.Setenv("BIND_ADDR", "::1")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr() != "[::1]:9090" {
		t.Fatalf("v6 listen %s", cfg.ListenAddr())
	}
	t.Setenv("BIND_ADDR", "[::1]")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "::1" || cfg.ListenAddr() != "[::1]:9090" {
		t.Fatalf("bracketed v6: %q %s", cfg.BindAddr, cfg.ListenAddr())
	}
	t.Setenv("BIND_ADDR", "127.0.0.1:8080")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "BIND_ADDR") {
		t.Fatalf("host:port should be rejected, got %v", err)
	}

	t.Setenv("BIND_ADDR", "")
	t.Setenv("REFRESH_EVERY", "30s")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REFRESH_EVERY") {
		t.Fatalf("short refresh: %v", err)
	}
	t.Setenv("REFRESH_EVERY", "nope")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REFRESH_EVERY") {
		t.Fatalf("bad refresh: %v", err)
	}
	t.Setenv("REFRESH_EVERY", "90m")
	t.Setenv("GUIDE_HOURS", "25")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshEvery != 90*time.Minute || cfg.GuideHours != 30 {
		t.Fatalf("rounded: refresh %s hours %d", cfg.RefreshEvery, cfg.GuideHours)
	}
	t.Setenv("GUIDE_HOURS", "70")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GuideHours != 72 {
		t.Fatalf("70 -> %d", cfg.GuideHours)
	}
	for _, bad := range []string{"3", "80", "nope"} {
		t.Setenv("GUIDE_HOURS", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GUIDE_HOURS") {
			t.Fatalf("GUIDE_HOURS %s: %v", bad, err)
		}
	}

	t.Setenv("GUIDE_HOURS", "24")
	t.Setenv("ACCESS_TOKEN", "bad/token")
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "bad/token") {
		t.Fatalf("slash token error should not echo the secret: %v", err)
	}
	t.Setenv("ACCESS_TOKEN", "has space")
	if _, err := Load(); err == nil {
		t.Fatal("expected token with a space to be rejected")
	}
}

func TestLoadOpsKnobsFromFile(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "pluto.conf")
	body := "PLUTO_EMAIL=a@b.c\nPLUTO_PASSWORD=x\nBIND_ADDR=127.0.0.1\nACCESS_TOKEN=\"file-secret\"\nREFRESH_EVERY=2h\nGUIDE_HOURS=36\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLUTO_CONFIG", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "127.0.0.1" || cfg.AccessToken != "file-secret" || cfg.RefreshEvery != 2*time.Hour || cfg.GuideHours != 36 {
		t.Fatalf("file knobs: %+v", cfg)
	}
}

func TestLoadTVEAndAccessToken(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PLUTO_EMAIL", "user@example.com")
	t.Setenv("PLUTO_PASSWORD", "secret")
	t.Setenv("TVE_SOURCES", "fox, aenetworks, fox, discovery")
	t.Setenv("DIRECTV_USERNAME", " viewer ")
	t.Setenv("DIRECTV_PASSWORD", " keep spaces ")
	t.Setenv("TVE_SESSION_FILE", "/data/tve.json")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.TVESources, ",") != "fox,aenetworks,discovery" {
		t.Fatalf("sources %v", cfg.TVESources)
	}
	if cfg.TVEProvider != "dtv" || !cfg.TVECombined || cfg.TVEStartChannel != 20000 {
		t.Fatalf("provider defaults %+v", cfg)
	}
	if cfg.DirectvUsername != "viewer" || cfg.DirectvPassword != " keep spaces " {
		t.Fatalf("directv creds username %q password %q", cfg.DirectvUsername, cfg.DirectvPassword)
	}
	if cfg.TVESessionFile != "/data/tve.json" {
		t.Fatalf("session %s", cfg.TVESessionFile)
	}

	t.Setenv("TVE_COMBINED", "false")
	t.Setenv("TVE_PROVIDER", "xfinity")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TVE_PROVIDER") {
		t.Fatalf("expected provider error, got %v", err)
	}
	t.Setenv("TVE_PROVIDER", "dtv")
	t.Setenv("TVE_SOURCES", "nbc")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("expected unknown source, got %v", err)
	}
	t.Setenv("TVE_SOURCES", "fox")
	t.Setenv("TVE_START_CHANNEL", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected negative tve start channel to be rejected")
	}
	t.Setenv("TVE_START_CHANNEL", "21000")
	secret := "bad/token-value"
	t.Setenv("ACCESS_TOKEN", secret)
	_, err = Load()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("token error leaked or was missing: %v", err)
	}
	t.Setenv("ACCESS_TOKEN", "too long "+strings.Repeat("a", 128))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "128") {
		t.Fatalf("expected length error, got %v", err)
	}
	t.Setenv("ACCESS_TOKEN", "good-token")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessToken != "good-token" || cfg.TVECombined || cfg.TVEStartChannel != 21000 {
		t.Fatalf("parsed tve %+v", cfg)
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
