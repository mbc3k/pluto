package tve

import "testing"

func TestRedact(t *testing.T) {
	got := redact("login failed for user secret-pass", "secret-pass", "user")
	if got != "login failed for REDACTED REDACTED" {
		t.Fatalf("%q", got)
	}
	if redact("ab", "ab") != "ab" {
		t.Fatal("short secrets are left alone so they do not blank unrelated text")
	}
}
