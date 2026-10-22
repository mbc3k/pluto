package tve

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Activation is an Adobe Pass device-code sign-in that a person finishes
// in their own browser. The regcode URL is shown on the sign-in page.
// Client credentials stay in the session file so a later poll can collect
// the authn token. They are not logged.
type Activation struct {
	Code          string    `json:"code"`
	URL           string    `json:"url"`
	Requestor     string    `json:"requestor"`
	Expires       time.Time `json:"expires"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret"`
	AccessToken   string    `json:"access_token"`
	DeviceID      string    `json:"device_id"`
	Fingerprint   string    `json:"fingerprint"`
	PassSFP       string    `json:"pass_sfp,omitempty"`
	AccessExpires time.Time `json:"access_expires,omitempty"`
}

type adobeSession struct {
	Requestor     string
	ClientID      string
	ClientSecret  string
	AccessToken   string
	DeviceID      string
	Fingerprint   string
	PassSFP       string
	Code          string
	CodeExpires   time.Time
	AuthURL       string
	AccessExpires time.Time
}

func (s *Service) registration() (requestor, kind string, scripted bool) {
	hasAE, hasWarner, hasAMC, other := false, false, false, false
	for _, id := range s.sources {
		switch id {
		case SourceAE:
			hasAE = true
		case SourceWarner:
			hasWarner = true
		case SourceAMC:
			hasAMC = true
		default:
			other = true
		}
	}
	switch {
	case hasAE:
		return "HISTORY", "history", true
	case hasWarner:
		return "TNT", "tnt", true
	case hasAMC && !other:
		// AMC does not publish a registration document we can discover.
		// Browser sign-in reports that instead of calling identity.directv.com.
		return "AMC", "amc", false
	default:
		// Fox and Discovery do not start the shared Adobe login. Browser
		// sign-in can still register the public History client.
		return "HISTORY", "history", false
	}
}

func (s *Service) openAdobe(ctx context.Context) (*adobeSession, error) {
	requestor, kind, _ := s.registration()
	if kind == "amc" {
		return nil, fmt.Errorf("AMC does not publish an Adobe registration document this process can discover. Enable aenetworks or warner, or paste an Adobe authentication token")
	}
	pageURL, extract := s.statementSource(kind)
	page, err := s.fetch(ctx, pageURL, map[string]string{"Accept": "text/html"})
	if err != nil {
		return nil, fmt.Errorf("adobe registration page: %s", redact(err.Error(), s.password, s.user))
	}
	if page.Status < 200 || page.Status >= 300 {
		return nil, s.signInPageError(page)
	}
	statement, err := extract(page.Body)
	if err != nil {
		return nil, err
	}
	sess := &adobeSession{Requestor: requestor}
	if err := s.registerAdobe(ctx, sess, statement); err != nil {
		return nil, err
	}
	sess.AuthURL = s.adobeBaseURL() + "/adobe-services/authenticate/saml?" + url.Values{
		"noflash":      {"true"},
		"mso_id":       {"DTV"},
		"requestor_id": {requestor},
		"no_iframe":    {"false"},
		"domain_name":  {"adobe.com"},
		"redirect_url": {redirectFor(requestor)},
		"reg_code":     {sess.Code},
	}.Encode()
	return sess, nil
}

func (s *Service) statementSource(kind string) (string, func([]byte) (string, error)) {
	switch kind {
	case "tnt":
		u := s.turnerStatementURL
		if u == "" {
			u = "https://www.tntdrama.com/watchtnt/east"
		}
		return u, extractTurnerStatement
	default:
		u := s.historyStatementURL
		if u == "" {
			u = "https://play.history.com/live"
		}
		return u, extractHistoryStatement
	}
}

func redirectFor(requestor string) string {
	switch requestor {
	case "TNT":
		return "https://www.tntdrama.com/"
	default:
		return "https://www.history.com/"
	}
}

func (s *Service) registerAdobe(ctx context.Context, sess *adobeSession, statement string) error {
	secrets := []string{s.password, s.user, statement}
	regBody, _ := json.Marshal(map[string]string{"software_statement": statement})
	reg, err := s.adobeSend(ctx, http.MethodPost, "/o/client/register", "application/json", regBody, nil, secrets)
	if err != nil {
		return err
	}
	var registered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(reg.Body, &registered); err != nil || registered.ClientID == "" || registered.ClientSecret == "" {
		return s.signInPageError(reg)
	}
	sess.ClientID = registered.ClientID
	sess.ClientSecret = registered.ClientSecret
	secrets = append(secrets, sess.ClientID, sess.ClientSecret)

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {sess.ClientID},
		"client_secret": {sess.ClientSecret},
	}
	tok, err := s.adobeSend(ctx, http.MethodPost, "/o/client/token", "application/x-www-form-urlencoded", []byte(form.Encode()), nil, secrets)
	if err != nil {
		return err
	}
	var token struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(tok.Body, &token); err != nil || token.AccessToken == "" {
		return s.signInPageError(tok)
	}
	sess.AccessToken = token.AccessToken
	secs, _ := jsonInt(token.ExpiresIn)
	sess.AccessExpires = time.Now().Add(accessLifetime(secs)).UTC()
	secrets = append(secrets, sess.AccessToken)

	fp, err := randomHex(16)
	if err != nil {
		return err
	}
	sess.Fingerprint = fp
	devBody, _ := json.Marshal(map[string]string{"fingerprint": fp})
	dev, err := s.adobeSend(ctx, http.MethodPost, "/indiv/devices", "application/json", devBody, sess, secrets)
	if err != nil {
		return err
	}
	var device struct {
		DeviceID string `json:"deviceId"`
	}
	if err := json.Unmarshal(dev.Body, &device); err != nil || device.DeviceID == "" {
		return s.signInPageError(dev)
	}
	sess.DeviceID = device.DeviceID
	sess.PassSFP = dev.Header.Get("pass_sfp")
	secrets = append(secrets, sess.DeviceID, sess.PassSFP)

	codeForm := url.Values{
		"requestor": {sess.Requestor},
		"deviceId":  {sess.DeviceID},
		"format":    {"json"},
	}
	code, err := s.adobeSend(ctx, http.MethodPost, "/reggie/v1/"+url.PathEscape(sess.Requestor)+"/regcode", "application/x-www-form-urlencoded", []byte(codeForm.Encode()), sess, secrets)
	if err != nil {
		return err
	}
	var regcode struct {
		Code    string          `json:"code"`
		Expires json.RawMessage `json:"expires"`
	}
	if err := json.Unmarshal(code.Body, &regcode); err != nil || regcode.Code == "" {
		return s.signInPageError(code)
	}
	sess.Code = regcode.Code
	sess.CodeExpires = unixFlexible(regcode.Expires)
	if sess.CodeExpires.IsZero() {
		sess.CodeExpires = time.Now().Add(30 * time.Minute)
	}
	return nil
}

func (s *Service) fetchSignInPage(ctx context.Context) (string, string, *adobeSession, error) {
	sess, err := s.openAdobe(ctx)
	if err != nil {
		return "", "", nil, err
	}
	got, err := s.fetch(ctx, sess.AuthURL, s.adobeHeaders(sess, "text/html"))
	if err != nil {
		return "", "", nil, fmt.Errorf("directv sign-in did not start: %s", redact(err.Error(), s.password, s.user, sess.AccessToken, sess.Code))
	}
	if got.Status >= 400 || !bytes.Contains(bytes.ToLower(got.Body), []byte("<form")) {
		return "", "", nil, s.signInPageError(withSecrets(got, []string{sess.AccessToken, sess.ClientSecret, sess.Code}))
	}
	final := sess.AuthURL
	if got.URL != "" {
		final = got.URL
	}
	return string(got.Body), final, sess, nil
}

func (s *Service) pollAdobe(ctx context.Context, sess *adobeSession) (string, bool, error) {
	form := url.Values{
		"_method":      {"GET"},
		"reg_code":     {sess.Code},
		"requestor_id": {sess.Requestor},
	}
	secrets := []string{s.password, s.user, sess.AccessToken, sess.ClientSecret, sess.ClientID, sess.Code, sess.PassSFP}
	got, err := s.adobeExchange(ctx, http.MethodPost, "/adobe-services/session", "application/x-www-form-urlencoded", []byte(form.Encode()), sess, secrets)
	if err != nil {
		return "", false, err
	}
	if token, ok := extractAuthnToken(got.Body); ok {
		return token, false, nil
	}
	text := strings.ToLower(string(got.Body))
	if got.Status == http.StatusUnauthorized || got.Status == http.StatusBadRequest || strings.Contains(text, "pendinglogout") || strings.Contains(text, "<reason>") {
		return "", true, nil
	}
	if got.Status >= 400 {
		return "", false, s.signInPageError(got)
	}
	return "", true, nil
}

func (s *Service) adobeBaseURL() string {
	if s.adobeBase != "" {
		return strings.TrimRight(s.adobeBase, "/")
	}
	return "https://sp.auth.adobe.com"
}

func (s *Service) adobeHeaders(sess *adobeSession, accept string) map[string]string {
	h := map[string]string{"Accept": accept}
	if sess == nil {
		return h
	}
	if sess.AccessToken != "" {
		h["Authorization"] = "Bearer " + sess.AccessToken
	}
	if sess.DeviceID != "" {
		h["Ap_21"] = sess.DeviceID
		h["ap_42"] = "anonymous"
		h["ap_11"] = "Mac OS"
		h["ap_z"] = browserUA
	}
	if sess.PassSFP != "" {
		h["pass_sfp"] = sess.PassSFP
	}
	return h
}

func (s *Service) adobeSend(ctx context.Context, method, path, contentType string, body []byte, sess *adobeSession, secrets []string) (fetched, error) {
	got, err := s.adobeExchange(ctx, method, path, contentType, body, sess, secrets)
	if err != nil {
		return got, err
	}
	if got.Status < 200 || got.Status >= 300 {
		return got, s.signInPageError(withSecrets(got, secrets))
	}
	return got, nil
}

func (s *Service) adobeExchange(ctx context.Context, method, path, contentType string, body []byte, sess *adobeSession, secrets []string) (fetched, error) {
	rawURL := s.adobeBaseURL() + path
	if strings.HasPrefix(path, "http") {
		rawURL = path
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return fetched{}, err
	}
	for k, v := range s.adobeHeaders(sess, "*/*") {
		req.Header.Set(k, v)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := s.http().Do(req)
	if err != nil {
		return fetched{}, fmt.Errorf("adobe: %s", redact(err.Error(), secrets...))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	got := fetched{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        raw,
		Header:      resp.Header.Clone(),
		URL:         rawURL,
	}
	if resp.Request != nil && resp.Request.URL != nil {
		got.URL = resp.Request.URL.String()
	}
	return got, nil
}

type secretFetch struct {
	fetched
	secrets []string
}

func withSecrets(f fetched, secrets []string) secretFetch {
	return secretFetch{fetched: f, secrets: secrets}
}

func (s *Service) signInPageError(v any) error {
	var f fetched
	var secrets []string
	switch n := v.(type) {
	case secretFetch:
		f = n.fetched
		secrets = n.secrets
	case fetched:
		f = n
	default:
		return fmt.Errorf("directv sign-in page failed")
	}
	secrets = append(secrets, s.password, s.user)
	ct := f.ContentType
	if ct == "" {
		ct = "unknown"
	}
	snip := snippet(f.Body, secrets...)
	slog.Error("directv sign-in page failed", "status", f.Status, "content_type", ct, "body", snip, "url", safeURL(f.URL))
	return fmt.Errorf("directv sign-in page failed (HTTP %d, content-type %s, body %q)", f.Status, ct, snip)
}

func extractHistoryStatement(body []byte) (string, error) {
	raw, ok := scriptBody(body, "__NEXT_DATA__")
	if !ok {
		return "", fmt.Errorf("history page has no Adobe registration document")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("history registration document was not json")
	}
	stmt := nestedString(doc, "props", "config", "adobeSoftwareStatement", "history")
	if !strings.HasPrefix(stmt, "eyJ") {
		return "", fmt.Errorf("history page has no Adobe registration")
	}
	return stmt, nil
}

func extractTurnerStatement(body []byte) (string, error) {
	raw, ok := scriptBody(body, "drupal-settings-json")
	if !ok {
		return "", fmt.Errorf("turner page has no Adobe registration document")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("turner registration document was not json")
	}
	top, _ := doc["top2"].(map[string]any)
	stmt := strings.TrimSpace(asString(top["softwareStatement"]))
	if !strings.HasPrefix(stmt, "eyJ") {
		return "", fmt.Errorf("turner page has no Adobe registration")
	}
	return stmt, nil
}

func extractAuthnToken(body []byte) (string, bool) {
	s := string(body)
	lower := strings.ToLower(s)
	i := strings.Index(lower, "<authntoken>")
	if i < 0 {
		return "", false
	}
	j := strings.Index(lower[i:], "</authntoken>")
	if j < 0 {
		return "", false
	}
	token := html.UnescapeString(s[i : i+j+len("</authntoken>")])
	if !strings.Contains(strings.ToLower(token), "simpletoken") && !strings.Contains(strings.ToLower(token), "authntoken") {
		return "", false
	}
	return token, true
}

// tokenExpiry reads the MVPD authentication lifetime. Adobe puts a calendar
// time in simpleTokenExpires (a "GMT" or "_GMT" suffix is common, and the
// element may be HTML-escaped, namespaced, attributed, or wrapped in CDATA).
// A bare unix integer, in seconds or milliseconds, is also accepted. A
// missing or unreadable value is the zero time. Callers must not substitute
// a 12-hour lifetime for that: the previous substitution is what made a
// fresh browser sign-in look like it died the same day.
func tokenExpiry(token string) time.Time {
	if t, ok := parseAdobeTime(tagText(token, "simpleTokenExpires")); ok {
		return t
	}
	return time.Time{}
}

func tagText(raw, name string) string {
	raw = unescapeToken(raw)
	// The open and close names are the same literal. RE2 has no
	// backreference, and a generic close tag would end the match early.
	re := regexp.MustCompile(`(?is)<(?:[a-z0-9_.-]+:)?` + regexp.QuoteMeta(name) + `\b[^>]*>(.*?)</(?:[a-z0-9_.-]+:)?` + regexp.QuoteMeta(name) + `>`)
	m := re.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return cleanTagValue(m[1])
}

func unescapeToken(s string) string {
	for i := 0; i < 3; i++ {
		next := html.UnescapeString(s)
		if next == s {
			return s
		}
		s = next
	}
	return s
}

func cleanTagValue(raw string) string {
	raw = strings.TrimSpace(unescapeToken(raw))
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "<![cdata[") && strings.HasSuffix(lower, "]]>") {
		raw = strings.TrimSpace(raw[len("<![CDATA[") : len(raw)-len("]]>")])
	}
	return strings.TrimSpace(raw)
}

var gmtSuffix = regexp.MustCompile(`(?i)[_ ]gmt\b`)

func parseAdobeTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	if t, ok := parseAdobeTimeOnce(v); ok {
		return t, true
	}
	stripped := strings.TrimSpace(gmtSuffix.ReplaceAllString(v, ""))
	if stripped != v {
		return parseAdobeTimeOnce(stripped)
	}
	return time.Time{}, false
}

func parseAdobeTimeOnce(v string) (time.Time, bool) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		t := unixInt(n)
		if t.Year() < 2000 {
			return time.Time{}, false
		}
		return t, true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
		t := unixInt(int64(f))
		if t.Year() >= 2000 {
			return t, true
		}
	}
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006/01/02 15:04:05",
		"2006/01/02T15:04:05",
		"01/02/2006 15:04:05",
		"1/2/2006 15:04:05",
		"02 Jan 2006 15:04:05",
		"2 Jan 2006 15:04:05",
		"Jan 2 2006 15:04:05",
		"January 2 2006 15:04:05",
		time.RFC1123,
		time.RFC1123Z,
		"Mon Jan 2 15:04:05 MST 2006",
		"Mon Jan 02 15:04:05 MST 2006",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func jsonInt(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f), true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// accessLifetime is how long an Adobe dynamic-client access token is used.
// The token response's expires_in is seconds. A missing or absurd value
// uses 6 hours, which is the lifetime observed for this grant. This is the
// client credential, not the DirecTV authentication.
func accessLifetime(seconds int64) time.Duration {
	if seconds < 60 || seconds > 48*60*60 {
		return 6 * time.Hour
	}
	return time.Duration(seconds) * time.Second
}

func unixFlexible(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return time.Time{}
		}
		n = int64(f)
	}
	return unixInt(n)
}

func unixInt(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	if n > 1e12 {
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
