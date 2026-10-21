package tve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	defaultAuthURL      = "https://identity.directv.com/am/IdPwdAuth"
	defaultAuthorizeURL = "https://identity.directv.com/authorize"
	forgeRockClientID   = "fr_idp_02"
	browserUA           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// Endpoints are the DirecTV identity calls. Tests point them at a local server.
type Endpoints struct {
	AuthURL      string
	AuthorizeURL string
}

func (e Endpoints) auth() string {
	if e.AuthURL != "" {
		return e.AuthURL
	}
	return defaultAuthURL
}

func (e Endpoints) authorize() string {
	if e.AuthorizeURL != "" {
		return e.AuthorizeURL
	}
	return defaultAuthorizeURL
}

// Pending is an in-progress DirecTV login that needs a code or menu choice
// from a person. The callback body stays in the session file; the status
// page only shows Prompt.
type Pending struct {
	Prompt         string         `json:"prompt"`
	Kind           string         `json:"kind"`
	PageURL        string         `json:"page_url"`
	Callback       []byte         `json:"callback"`
	AuthorizeQuery url.Values     `json:"authorize_query"`
	Cookies        []StoredCookie `json:"cookies,omitempty"`
}

// Outcome is either a finished SAML round-trip or a pause for a code.
type Outcome struct {
	FinalURL string
	Pending  *Pending
}

// Login walks DirecTV's ForgeRock callback API and finishes the PKCE hop
// that Adobe's SAML form started. username and password are sent as callback
// values and are never written into the returned error.
func Login(ctx context.Context, client *http.Client, ep Endpoints, startHTML, startURL, username, password string) (*Outcome, error) {
	if client == nil {
		client = &http.Client{}
	}
	pageURL, err := postHiddenForm(ctx, client, startHTML, startURL, password, username)
	if err != nil {
		return nil, err
	}
	q := pageURL.Query()
	if q.Get("code_challenge") == "" || q.Get("redirect_uri") == "" {
		return nil, fmt.Errorf("directv sign-in page is missing the PKCE challenge")
	}
	authHeaders := forgeRockHeaders(pageURL.String())

	doc, err := postJSON(ctx, client, ep.auth(), authHeaders, []byte("{}"), password, username)
	if err != nil {
		return nil, err
	}
	if !hasCallback(doc, "NameCallback") {
		return nil, fmt.Errorf("directv username challenge was not offered")
	}
	body, err := fillCallback(doc, username)
	if err != nil {
		return nil, err
	}
	doc, err = postJSON(ctx, client, ep.auth(), authHeaders, body, password, username)
	if err != nil {
		return nil, err
	}
	if tokenID(doc) == "" && !hasCallback(doc, "PasswordCallback") {
		return pause(client, pageURL, doc), nil
	}
	if tokenID(doc) == "" {
		body, err = fillCallback(doc, password)
		if err != nil {
			return nil, err
		}
		doc, err = postJSON(ctx, client, ep.auth(), authHeaders, body, password, username)
		if err != nil {
			return nil, err
		}
	}
	if tokenID(doc) == "" {
		return pause(client, pageURL, doc), nil
	}
	final, err := finishAuthorize(ctx, client, ep, pageURL, password, username)
	if err != nil {
		return nil, err
	}
	return &Outcome{FinalURL: final}, nil
}

// Resume answers a pending code or menu challenge and finishes the same
// PKCE hop Login would have finished.
func Resume(ctx context.Context, client *http.Client, ep Endpoints, pending *Pending, code, password, username string) (*Outcome, error) {
	if pending == nil || len(pending.Callback) == 0 {
		return nil, fmt.Errorf("no directv sign-in is waiting for a code")
	}
	if client == nil {
		client = &http.Client{}
	}
	restoreCookies(client, pending.Cookies)
	body, err := fillCallback(pending.Callback, code)
	if err != nil {
		return nil, err
	}
	pageURL, err := url.Parse(pending.PageURL)
	if err != nil {
		return nil, fmt.Errorf("stored directv sign-in page is invalid")
	}
	doc, err := postJSON(ctx, client, ep.auth(), forgeRockHeaders(pending.PageURL), body, password, username)
	if err != nil {
		return nil, err
	}
	if tokenID(doc) == "" {
		next := pause(client, pageURL, doc)
		return next, nil
	}
	if len(pending.AuthorizeQuery) > 0 {
		pageURL.RawQuery = pending.AuthorizeQuery.Encode()
	}
	final, err := finishAuthorize(ctx, client, ep, pageURL, password, username)
	if err != nil {
		return nil, err
	}
	return &Outcome{FinalURL: final}, nil
}

func pause(client *http.Client, pageURL *url.URL, doc []byte) *Outcome {
	return &Outcome{Pending: &Pending{
		Prompt:         promptText(doc),
		Kind:           firstKind(doc),
		PageURL:        pageURL.String(),
		Callback:       append([]byte(nil), doc...),
		AuthorizeQuery: pageURL.Query(),
		Cookies:        exportCookies(client, pageURL.String()),
	}}
}

func finishAuthorize(ctx context.Context, client *http.Client, ep Endpoints, pageURL *url.URL, secrets ...string) (string, error) {
	q := pageURL.Query()
	params := url.Values{
		"scope":                 {"read"},
		"response_type":         {firstQuery(q, "response_type", "code")},
		"client_id":             {firstQuery(q, "fr_client_id", forgeRockClientID)},
		"redirect_uri":          {q.Get("redirect_uri")},
		"code_challenge":        {q.Get("code_challenge")},
		"code_challenge_method": {firstQuery(q, "code_challenge_method", "S256")},
		"state":                 {q.Get("state")},
	}
	if params.Get("redirect_uri") == "" || params.Get("code_challenge") == "" {
		return "", fmt.Errorf("directv sign-in is missing the PKCE challenge")
	}
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.authorize()+"?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Referer", pageURL.String())
	req.Header.Set("User-Agent", browserUA)
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", fmt.Errorf("directv authorize: %s", redact(err.Error(), secrets...))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
		return "", fmt.Errorf("directv authorize did not return a code (HTTP %d)", resp.StatusCode)
	}
	next, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("directv authorize location")
	}
	if base, err := url.Parse(ep.authorize()); err == nil {
		next = base.ResolveReference(next)
	}
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, next.String(), nil)
	if err != nil {
		return "", err
	}
	req2.Header.Set("User-Agent", browserUA)
	resp2, err := client.Do(req2)
	if err != nil {
		return "", fmt.Errorf("directv code redirect: %s", redact(err.Error(), secrets...))
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	if resp2.StatusCode >= 400 {
		return "", fmt.Errorf("directv code redirect failed (HTTP %d)", resp2.StatusCode)
	}
	action, fields, err := HiddenForm(string(body), resp2.Request.URL.String())
	if err != nil {
		return "", fmt.Errorf("directv confirmation form: %w", err)
	}
	return postForm(ctx, client, action, fields, secrets...)
}

func postHiddenForm(ctx context.Context, client *http.Client, doc, pageURL string, secrets ...string) (*url.URL, error) {
	action, fields, err := HiddenForm(doc, pageURL)
	if err != nil {
		return nil, err
	}
	final, err := postForm(ctx, client, action, fields, secrets...)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(final)
	if err != nil {
		return nil, fmt.Errorf("directv login page url")
	}
	return u, nil
}

func postForm(ctx context.Context, client *http.Client, action string, fields url.Values, secrets ...string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, action, strings.NewReader(fields.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", browserUA)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("directv form: %s", redact(err.Error(), secrets...))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("directv form failed (HTTP %d)", resp.StatusCode)
	}
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String(), nil
	}
	return action, nil
}

func firstQuery(q url.Values, key, fallback string) string {
	if v := strings.TrimSpace(q.Get(key)); v != "" {
		return v
	}
	return fallback
}

func forgeRockHeaders(referer string) http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("Accept-API-Version", "resource=2.0, protocol=1.0")
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("client_id", forgeRockClientID)
	h.Set("Origin", "https://identity.directv.com")
	h.Set("Referer", referer)
	h.Set("User-Agent", browserUA)
	return h
}

func postJSON(ctx context.Context, client *http.Client, endpoint string, headers http.Header, body []byte, secrets ...string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("directv challenge: %s", redact(err.Error(), secrets...))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("directv challenge failed (HTTP %d): %s", resp.StatusCode, redact(clip(raw), secrets...))
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("directv challenge returned non-JSON (HTTP %d)", resp.StatusCode)
	}
	return raw, nil
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

func tokenID(doc []byte) string {
	var probe struct {
		TokenID string `json:"tokenId"`
	}
	_ = json.Unmarshal(doc, &probe)
	return probe.TokenID
}

func hasCallback(doc []byte, kind string) bool {
	var probe struct {
		Callbacks []struct {
			Type string `json:"type"`
		} `json:"callbacks"`
	}
	if err := json.Unmarshal(doc, &probe); err != nil {
		return false
	}
	for _, c := range probe.Callbacks {
		if c.Type == kind {
			return true
		}
	}
	return false
}

func firstKind(doc []byte) string {
	var probe struct {
		Callbacks []struct {
			Type string `json:"type"`
		} `json:"callbacks"`
	}
	_ = json.Unmarshal(doc, &probe)
	for _, c := range probe.Callbacks {
		switch c.Type {
		case "NameCallback", "PasswordCallback", "ChoiceCallback", "ConfirmationCallback", "TextInputCallback", "OneTimePasswordCallback":
			return c.Type
		}
	}
	if len(probe.Callbacks) > 0 {
		return probe.Callbacks[0].Type
	}
	return "callback"
}

func promptText(doc []byte) string {
	var probe struct {
		Callbacks []struct {
			Type   string `json:"type"`
			Output []struct {
				Value any `json:"value"`
			} `json:"output"`
		} `json:"callbacks"`
	}
	if err := json.Unmarshal(doc, &probe); err != nil {
		return "DirecTV needs another step before sign-in can finish."
	}
	var parts []string
	for _, c := range probe.Callbacks {
		for _, out := range c.Output {
			s := strings.TrimSpace(fmt.Sprint(out.Value))
			if s == "" || s == "<nil>" {
				continue
			}
			parts = append(parts, s)
		}
		if len(parts) > 0 {
			break
		}
	}
	if len(parts) == 0 {
		return "DirecTV needs a code or menu choice to finish sign-in."
	}
	return strings.Join(parts, " ")
}

func fillCallback(doc []byte, value string) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("directv challenge was not JSON")
	}
	raw, _ := root["callbacks"].([]any)
	filled := false
	for _, item := range raw {
		cb, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := cb["type"].(string)
		switch kind {
		case "NameCallback", "PasswordCallback", "ChoiceCallback", "ConfirmationCallback", "TextInputCallback", "OneTimePasswordCallback":
		default:
			continue
		}
		inputs, _ := cb["input"].([]any)
		if len(inputs) == 0 {
			continue
		}
		in, ok := inputs[0].(map[string]any)
		if !ok {
			continue
		}
		if _, isNum := in["value"].(float64); isNum {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				in["value"] = float64(n)
			} else {
				in["value"] = value
			}
		} else {
			in["value"] = value
		}
		filled = true
		break
	}
	if !filled {
		return nil, fmt.Errorf("directv challenge had nowhere to enter a value")
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return out, nil
}
