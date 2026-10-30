package tve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// mediaToken is the short Adobe media token for one requestor and resource.
// The longer authz token is cached for about a day. The short token is
// cached for five minutes and minted again after that. A requestor that
// rejects the stored sign-in is retried once with the requestor named
// inside the authn token, when that field is present.
func (s *Service) mediaToken(ctx context.Context, st State, requestor, resource string) (string, error) {
	secrets := s.playbackSecrets(st)
	if tok, ok := s.cachedSecret("short", requestor+"\n"+resource); ok {
		return tok, nil
	}
	authz, used, err := s.authzToken(ctx, st, requestor, resource, secrets)
	if err != nil {
		return "", err
	}
	if tok, ok := s.cachedSecret("short", used+"\n"+resource); ok {
		return tok, nil
	}
	short, err := s.shortAuthorize(ctx, st, used, authz, secrets)
	if err != nil {
		return "", err
	}
	s.storeSecret("short", used+"\n"+resource, short, time.Now().Add(shortTTL))
	return short, nil
}

func (s *Service) authzToken(ctx context.Context, st State, requestor, resource string, secrets []string) (string, string, error) {
	if tok, ok := s.cachedSecret("authz", requestor+"\n"+resource); ok {
		return tok, requestor, nil
	}
	fallback := authnRequestor(st.AuthnToken)
	if fallback != "" && !strings.EqualFold(fallback, requestor) {
		if tok, ok := s.cachedSecret("authz", fallback+"\n"+resource); ok {
			return tok, fallback, nil
		}
	}
	tok, err := s.authorize(ctx, st, requestor, resource, secrets)
	if err != nil && fallback != "" && !strings.EqualFold(fallback, requestor) {
		if tok2, err2 := s.authorize(ctx, st, fallback, resource, secrets); err2 == nil {
			s.storeSecret("authz", fallback+"\n"+resource, tok2, time.Now().Add(authzTTL))
			return tok2, fallback, nil
		}
	}
	if err != nil {
		return "", "", err
	}
	s.storeSecret("authz", requestor+"\n"+resource, tok, time.Now().Add(authzTTL))
	return tok, requestor, nil
}

func (s *Service) authorize(ctx context.Context, st State, requestor, resource string, secrets []string) (string, error) {
	mso := tagText(st.AuthnToken, "simpleTokenMsoID")
	if mso == "" {
		mso = "DTV"
	}
	form := url.Values{
		"resource_id":          {resource},
		"requestor_id":         {requestor},
		"authentication_token": {st.AuthnToken},
		"mso_id":               {mso},
		"generic_data":         {""},
		"userMeta":             {"1"},
	}
	got, err := s.adobeExchange(ctx, http.MethodPost, "/adobe-services/authorize", "application/x-www-form-urlencoded", []byte(form.Encode()), authzSession(st), secrets)
	if err != nil {
		return "", playFail("adobe authorize", http.StatusBadGateway, redact(err.Error(), secrets...), true)
	}
	if fault := adobeFault("adobe authorize", requestor, got, secrets); fault != nil {
		return "", fault
	}
	token := html.UnescapeString(strings.TrimSpace(tagText(string(got.Body), "authzToken")))
	if token == "" {
		return "", playFail("adobe authorize", http.StatusBadGateway, "response did not include an authz token for "+requestor, true)
	}
	return token, nil
}

func (s *Service) shortAuthorize(ctx context.Context, st State, requestor, authz string, secrets []string) (string, error) {
	guid := tagText(st.AuthnToken, "simpleTokenAuthenticationGuid")
	form := url.Values{
		"authz_token":  {authz},
		"requestor_id": {requestor},
		"generic_data": {"{}"},
		"session_guid": {guid},
		"hashed_guid":  {"false"},
	}
	got, err := s.adobeExchange(ctx, http.MethodPost, "/adobe-services/shortAuthorize", "application/x-www-form-urlencoded", []byte(form.Encode()), authzSession(st), secrets)
	if err != nil {
		return "", playFail("adobe shortAuthorize", http.StatusBadGateway, redact(err.Error(), secrets...), true)
	}
	if fault := adobeFault("adobe shortAuthorize", requestor, got, secrets); fault != nil {
		return "", fault
	}
	token := strings.TrimSpace(string(got.Body))
	if token == "" {
		return "", playFail("adobe shortAuthorize", http.StatusBadGateway, "response was empty for "+requestor, true)
	}
	return token, nil
}

// mediaTokenPreferDecision asks Adobe's per-requestor decision API first.
// AMC's playback call wants that serialized token. TCM wants the same token
// after base64 decoding. A client registered for a different requestor often
// cannot call this API; that failure falls through to the legacy short token.
// An explicit not-entitled decision does not fall through.
func (s *Service) mediaTokenPreferDecision(ctx context.Context, st State, requestor, resource string, decode bool) (string, error) {
	secrets := s.playbackSecrets(st)
	if tok, ok := s.cachedSecret("short", requestor+"\n"+resource); ok {
		return tok, nil
	}
	tok, err := s.decisionToken(ctx, st, requestor, resource, secrets)
	if err == nil && tok != "" {
		if decode {
			tok = decodedMediaToken(tok)
		}
		s.storeSecret("short", requestor+"\n"+resource, tok, time.Now().Add(shortTTL))
		return tok, nil
	}
	if pe, ok := err.(*PlayError); ok && pe.Status == http.StatusForbidden && !pe.Retry {
		return "", err
	}
	return s.mediaToken(ctx, st, requestor, resource)
}

func (s *Service) decisionToken(ctx context.Context, st State, requestor, resource string, secrets []string) (string, error) {
	mso := tagText(st.AuthnToken, "simpleTokenMsoID")
	if mso == "" {
		mso = "DTV"
	}
	raw, err := json.Marshal(map[string]any{"resources": []string{resource}})
	if err != nil {
		return "", err
	}
	path := "/api/v2/" + url.PathEscape(requestor) + "/decisions/authorize/" + url.PathEscape(mso)
	got, err := s.adobeExchange(ctx, http.MethodPost, path, "application/json", raw, authzSession(st), secrets)
	if err != nil {
		return "", playFail("adobe decision", http.StatusBadGateway, redact(err.Error(), secrets...), true)
	}
	if got.Status < 200 || got.Status >= 300 {
		retry := got.Status >= 500
		status := http.StatusBadGateway
		if got.Status == http.StatusUnauthorized || got.Status == http.StatusForbidden {
			status = got.Status
			retry = true
		}
		detail := "HTTP " + strconv.Itoa(got.Status) + " for " + requestor
		if snip := snippet(got.Body, secrets...); snip != "" {
			detail += " " + snip
		}
		return "", playFail("adobe decision", status, detail, retry)
	}
	doc, err := decodeJSON(got.Body)
	if err != nil {
		return "", playFail("adobe decision", http.StatusBadGateway, "response was not JSON", true)
	}
	root, _ := doc.(map[string]any)
	for _, item := range asList(root["decisions"]) {
		row, _ := item.(map[string]any)
		if row == nil {
			continue
		}
		if res := strings.TrimSpace(asText(row["resource"])); res != "" && res != resource {
			continue
		}
		if auth, ok := row["authorized"].(bool); ok && !auth {
			return "", playFail("adobe decision", http.StatusForbidden, "DirecTV is not entitled to "+requestor, false)
		}
		token, _ := row["token"].(map[string]any)
		serialized := strings.TrimSpace(asText(token["serializedToken"]))
		if serialized != "" {
			return serialized, nil
		}
	}
	return "", playFail("adobe decision", http.StatusBadGateway, "response did not include a media token for "+requestor, true)
}

func decodedMediaToken(serialized string) string {
	raw, err := base64.StdEncoding.DecodeString(serialized)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(serialized)
	}
	if err != nil || !strings.Contains(string(raw), "<") {
		return serialized
	}
	return strings.TrimSpace(string(raw))
}

func adobeFault(step, requestor string, got fetched, secrets []string) *PlayError {
	text := strings.ToLower(string(got.Body))
	if strings.Contains(text, "pendinglogout") {
		return playFail(step, http.StatusUnauthorized, "DirecTV session expired (pendingLogout) for "+requestor, false)
	}
	if strings.Contains(text, "notauthorized") || (got.Status < 400 && strings.Contains(text, "<error")) {
		detail := "DirecTV is not entitled to " + requestor
		if snip := snippet(got.Body, secrets...); snip != "" {
			detail = snip
		}
		return playFail(step, http.StatusForbidden, detail, false)
	}
	if got.Status < 200 || got.Status >= 300 {
		detail := "HTTP " + strconv.Itoa(got.Status) + " for " + requestor
		if snip := snippet(got.Body, secrets...); snip != "" {
			detail += " " + snip
		}
		retry := got.Status == http.StatusUnauthorized || got.Status == http.StatusForbidden || got.Status >= 500
		status := http.StatusBadGateway
		if got.Status == http.StatusUnauthorized || got.Status == http.StatusForbidden {
			status = got.Status
		}
		return playFail(step, status, detail, retry)
	}
	return nil
}

func authzSession(st State) *adobeSession {
	sess := sessionFromState(&st)
	sess.NameID = tagText(st.AuthnToken, "simpleSamlNameID")
	sess.SessionIndex = tagText(st.AuthnToken, "simpleSamlSessionIndex")
	return sess
}

func authnRequestor(token string) string {
	for _, name := range []string{"simpleTokenRequestorID", "simpleTokenRequestorId", "requestorID"} {
		if v := tagText(token, name); v != "" {
			return v
		}
	}
	return ""
}

func (s *Service) playbackSecrets(st State) []string {
	secrets := s.secretValues(&st)
	secrets = append(secrets,
		tagText(st.AuthnToken, "simpleSamlNameID"),
		tagText(st.AuthnToken, "simpleSamlSessionIndex"),
		tagText(st.AuthnToken, "simpleTokenAuthenticationGuid"),
	)
	return secrets
}

func (s *Service) cachedSecret(kind, key string) (string, bool) {
	s.playMu.Lock()
	defer s.playMu.Unlock()
	s.initPlayLocked()
	cache := s.authzCache
	if kind == "short" {
		cache = s.shortCache
	}
	item, ok := cache[key]
	if !ok || time.Until(item.until) < time.Minute || item.value == "" {
		return "", false
	}
	return item.value, true
}

func (s *Service) storeSecret(kind, key, value string, until time.Time) {
	s.playMu.Lock()
	defer s.playMu.Unlock()
	s.initPlayLocked()
	if kind == "short" {
		s.shortCache[key] = timedSecret{value: value, until: until}
		return
	}
	s.authzCache[key] = timedSecret{value: value, until: until}
}
