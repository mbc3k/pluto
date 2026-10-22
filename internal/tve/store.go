package tve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is the on-disk DirecTV session. The password is never stored.
// AuthnToken is the long-lived MVPD authentication. ClientID, ClientSecret,
// AccessToken, and DeviceID are the Adobe client that authentication is
// bound to; they stay in this file so the access token can be refreshed
// without another browser sign-in. The file is mode 0600. None of those
// values are written to logs or the status page.
type State struct {
	Updated       time.Time      `json:"updated"`
	Signed        bool           `json:"signed_in"`
	Expires       time.Time      `json:"expires,omitempty"`
	Method        string         `json:"method,omitempty"`
	Requestor     string         `json:"requestor,omitempty"`
	AuthnToken    string         `json:"authn_token,omitempty"`
	ClientID      string         `json:"client_id,omitempty"`
	ClientSecret  string         `json:"client_secret,omitempty"`
	AccessToken   string         `json:"access_token,omitempty"`
	AccessExpires time.Time      `json:"access_expires,omitempty"`
	DeviceID      string         `json:"device_id,omitempty"`
	Fingerprint   string         `json:"fingerprint,omitempty"`
	PassSFP       string         `json:"pass_sfp,omitempty"`
	Pending       *Pending       `json:"pending,omitempty"`
	Activation    *Activation    `json:"activation,omitempty"`
	Cookies       []StoredCookie `json:"cookies,omitempty"`
	FinalURL      string         `json:"final_url,omitempty"`
}

func loadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("open tve session: %w", err)
	}
	var st State
	if len(raw) == 0 {
		return State{}, nil
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("read tve session: %w", err)
	}
	return st, nil
}

func saveState(path string, st State) error {
	st.Updated = time.Now().UTC()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create tve session directory: %w", err)
		}
	}
	f, err := os.CreateTemp(dir, ".pluto-tve-*.tmp")
	if err != nil {
		return fmt.Errorf("temp tve session: %w", err)
	}
	tmp := f.Name()
	success := false
	defer func() {
		if !success {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace tve session: %w", err)
	}
	success = true
	if dirf, err := os.Open(dir); err == nil {
		_ = dirf.Sync()
		dirf.Close()
	}
	return nil
}
