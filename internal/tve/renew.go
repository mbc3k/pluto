package tve

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	renewalClient  = "client"
	renewalRetry   = "retrying"
	renewalBrowser = "needs_browser"
)

func adoptClient(st *State, sess *adobeSession) {
	if st == nil || sess == nil {
		return
	}
	if sess.Requestor != "" {
		st.Requestor = sess.Requestor
	}
	st.ClientID = sess.ClientID
	st.ClientSecret = sess.ClientSecret
	st.AccessToken = sess.AccessToken
	st.DeviceID = sess.DeviceID
	st.Fingerprint = sess.Fingerprint
	st.PassSFP = sess.PassSFP
	if !sess.AccessExpires.IsZero() {
		st.AccessExpires = sess.AccessExpires
	} else if st.AccessToken != "" && st.AccessExpires.IsZero() {
		st.AccessExpires = time.Now().Add(accessLifetime(0)).UTC()
	}
}

func (s *Service) maintainAuthn(ctx context.Context, st *State) {
	if normalizeStoredExpiry(st) {
		if err := saveState(s.sessionPath, *st); err != nil {
			slog.Error("tve session was not saved", "err", err)
		}
	}
	if s.accessDue(*st) {
		s.renewAccessToken(ctx, st)
	}
	if s.shouldConfirm(*st) {
		s.confirmAuthn(ctx, st)
	}
	s.warnExpiry(*st)
}

func normalizeStoredExpiry(st *State) bool {
	exp := tokenExpiry(st.AuthnToken)
	if exp.IsZero() || exp.Equal(st.Expires) {
		return false
	}
	// A check that already rejected this authentication stays rejected.
	// Re-reading the token timestamp must not bring it back.
	if !st.Signed && !st.Expires.IsZero() && !time.Now().Before(st.Expires) {
		return false
	}
	st.Expires = exp.UTC()
	if time.Now().Before(exp) {
		st.Signed = true
	}
	return true
}

func (s *Service) accessDue(st State) bool {
	if st.ClientID == "" || st.ClientSecret == "" {
		return false
	}
	if !s.renewNext.IsZero() && time.Now().Before(s.renewNext) {
		return false
	}
	if st.AccessExpires.IsZero() {
		return true
	}
	margin := s.every + time.Hour
	if margin < 2*time.Hour {
		margin = 2 * time.Hour
	}
	return time.Until(st.AccessExpires) < margin
}

func (s *Service) shouldConfirm(st State) bool {
	if st.DeviceID == "" || st.Requestor == "" || st.AccessToken == "" {
		return false
	}
	if !s.confirmNext.IsZero() && time.Now().Before(s.confirmNext) {
		return false
	}
	if st.Expires.IsZero() {
		return true
	}
	return time.Until(st.Expires) <= 24*time.Hour
}

func (s *Service) renewAccessToken(ctx context.Context, st *State) {
	secrets := s.secretValues(st)
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {st.ClientID},
		"client_secret": {st.ClientSecret},
	}
	got, err := s.adobeExchange(ctx, http.MethodPost, "/o/client/token", "application/x-www-form-urlencoded", []byte(form.Encode()), sessionFromState(st), secrets)
	if err != nil || got.Status < 200 || got.Status >= 300 {
		s.noteRenewFailure(got, err, secrets)
		return
	}
	var token struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if json.Unmarshal(got.Body, &token) != nil || token.AccessToken == "" {
		s.noteRenewFailure(got, nil, secrets)
		return
	}
	st.AccessToken = token.AccessToken
	secs, _ := jsonInt(token.ExpiresIn)
	st.AccessExpires = time.Now().Add(accessLifetime(secs)).UTC()
	s.renewDelay = 0
	s.renewNext = time.Time{}
	if err := saveState(s.sessionPath, *st); err != nil {
		slog.Error("tve session was not saved", "err", err)
		return
	}
	slog.Info("tve adobe client token refreshed", "expires", clock(st.AccessExpires))
}

func (s *Service) noteRenewFailure(got fetched, err error, secrets []string) {
	s.renewDelay = backoff(s.renewDelay)
	s.renewNext = time.Now().Add(s.renewDelay)
	if err != nil {
		slog.Warn("tve adobe client token refresh failed", "err", redact(err.Error(), secrets...))
		return
	}
	ct := got.ContentType
	if ct == "" {
		ct = "unknown"
	}
	slog.Warn("tve adobe client token refresh failed", "status", got.Status, "content_type", ct, "body", snippet(got.Body, secrets...))
}

func (s *Service) confirmAuthn(ctx context.Context, st *State) {
	secrets := s.secretValues(st)
	q := url.Values{
		"requestor": {st.Requestor},
		"deviceId":  {st.DeviceID},
	}
	got, err := s.adobeExchange(ctx, http.MethodGet, "/api/v1/tokens/authn?"+q.Encode(), "", nil, sessionFromState(st), secrets)
	if err != nil {
		s.confirmDelay = backoff(s.confirmDelay)
		s.confirmNext = time.Now().Add(s.confirmDelay)
		slog.Warn("tve authn check failed", "err", redact(err.Error(), secrets...))
		return
	}
	switch got.Status {
	case http.StatusOK:
		s.confirmDelay = 0
		s.confirmNext = time.Time{}
		exp := authnCheckExpiry(got.Body)
		if exp.IsZero() || exp.Equal(st.Expires) {
			return
		}
		st.Expires = exp.UTC()
		if time.Now().Before(exp) {
			st.Signed = true
		}
		if err := saveState(s.sessionPath, *st); err != nil {
			slog.Error("tve session was not saved", "err", err)
		}
	case http.StatusGone:
		parsed := tokenExpiry(st.AuthnToken)
		s.confirmNext = time.Now().Add(24 * time.Hour)
		if !parsed.IsZero() && time.Until(parsed) > 24*time.Hour {
			if !s.confirmQuiet {
				slog.Warn("tve authn check reported expired while the token timestamp is still ahead", "status", got.Status)
				s.confirmQuiet = true
			}
			return
		}
		if !st.Signed && !st.Expires.IsZero() && !time.Now().Before(st.Expires) {
			return
		}
		st.Signed = false
		st.Expires = time.Now().Add(-time.Minute).UTC()
		if err := saveState(s.sessionPath, *st); err != nil {
			slog.Error("tve session was not saved", "err", err)
		}
	default:
		if got.Status == http.StatusNotFound || got.Status == http.StatusUnauthorized || got.Status == http.StatusBadRequest {
			s.confirmNext = time.Now().Add(24 * time.Hour)
		} else {
			s.confirmDelay = backoff(s.confirmDelay)
			s.confirmNext = time.Now().Add(s.confirmDelay)
		}
		if !s.confirmQuiet {
			ct := got.ContentType
			if ct == "" {
				ct = "unknown"
			}
			slog.Warn("tve authn check is not available; using the expiry from the authentication token", "status", got.Status, "content_type", ct, "body", snippet(got.Body, secrets...))
			s.confirmQuiet = true
		}
	}
}

func (s *Service) warnExpiry(st State) {
	if st.AuthnToken == "" && st.Expires.IsZero() {
		return
	}
	if st.Expires.IsZero() {
		if s.warnedLevel != 4 {
			slog.Warn("tve authn expiry was not readable; keeping the session until Adobe rejects it")
			s.warnedLevel = 4
		}
		return
	}
	until := time.Until(st.Expires)
	switch {
	case until <= 0:
		if s.warnedLevel < 3 || !s.warnedFor.Equal(st.Expires) {
			slog.Warn("tve directv authentication has expired", "expires", clock(st.Expires))
			s.warnedLevel = 3
			s.warnedFor = st.Expires
		}
	case until <= time.Hour:
		if s.warnedLevel < 2 || !s.warnedFor.Equal(st.Expires) {
			slog.Warn("tve directv authentication expires within an hour", "expires", clock(st.Expires))
			s.warnedLevel = 2
			s.warnedFor = st.Expires
		}
	case until <= 24*time.Hour:
		if s.warnedLevel < 1 || !s.warnedFor.Equal(st.Expires) {
			slog.Warn("tve directv authentication expires within 24h", "expires", clock(st.Expires))
			s.warnedLevel = 1
			s.warnedFor = st.Expires
		}
	default:
		s.warnedLevel = 0
		s.warnedFor = time.Time{}
	}
}

func (s *Service) renewalKind(st State) string {
	if !st.Expires.IsZero() && !time.Now().Before(st.Expires) {
		return renewalBrowser
	}
	if !st.Expires.IsZero() && time.Until(st.Expires) <= 24*time.Hour {
		return renewalBrowser
	}
	if !s.renewNext.IsZero() && time.Now().Before(s.renewNext) {
		return renewalRetry
	}
	if st.ClientID != "" && st.ClientSecret != "" && st.AccessToken != "" {
		return renewalClient
	}
	return renewalBrowser
}

func (s *Service) secretValues(st *State) []string {
	if st == nil {
		return []string{s.password, s.user}
	}
	return []string{s.password, s.user, st.ClientSecret, st.AccessToken, st.AuthnToken, st.DeviceID, st.PassSFP, st.Fingerprint}
}

func sessionFromState(st *State) *adobeSession {
	if st == nil {
		return nil
	}
	return &adobeSession{
		Requestor:     st.Requestor,
		ClientID:      st.ClientID,
		ClientSecret:  st.ClientSecret,
		AccessToken:   st.AccessToken,
		DeviceID:      st.DeviceID,
		Fingerprint:   st.Fingerprint,
		PassSFP:       st.PassSFP,
		AccessExpires: st.AccessExpires,
	}
}

func authnCheckExpiry(body []byte) time.Time {
	if t, ok := parseAdobeTime(tagText(string(body), "expires")); ok {
		return t
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return time.Time{}
	}
	switch n := doc["expires"].(type) {
	case string:
		if t, ok := parseAdobeTime(n); ok {
			return t
		}
	case float64:
		if t, ok := parseAdobeTime(jsonNumber(n)); ok {
			return t
		}
	}
	return time.Time{}
}

func backoff(cur time.Duration) time.Duration {
	if cur < time.Minute {
		return time.Minute
	}
	next := cur * 2
	if next > time.Hour {
		return time.Hour
	}
	return next
}
