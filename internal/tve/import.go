package tve

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// sessionMaterial accepts an Adobe authn token, a Netscape cookies.txt
// export, or a JSON cookie array. DirecTV website cookies that do not
// carry an authn token are rejected: this process cannot replay them
// through the identity provider's CDN.
func sessionMaterial(raw string) (string, time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", time.Time{}, fmt.Errorf("paste an Adobe authentication token or a cookie export")
	}
	if len(raw) > 1<<20 {
		return "", time.Time{}, fmt.Errorf("that sign-in export is too large")
	}
	if token, exp, err := normalizeAuthnToken(raw); err == nil {
		return token, exp, nil
	} else if directToken(raw) {
		return "", time.Time{}, err
	}
	values := cookieValues(raw)
	var expired error
	for _, value := range values {
		token, exp, err := normalizeAuthnToken(value)
		if err == nil {
			return token, exp, nil
		}
		if err != nil && strings.Contains(err.Error(), "expired") {
			expired = err
		}
		if unescaped, err := url.QueryUnescape(value); err == nil && unescaped != value {
			token, exp, err := normalizeAuthnToken(unescaped)
			if err == nil {
				return token, exp, nil
			}
			if err != nil && strings.Contains(err.Error(), "expired") {
				expired = err
			}
		}
	}
	if expired != nil {
		return "", time.Time{}, expired
	}
	if len(values) == 0 {
		return "", time.Time{}, fmt.Errorf("that text is not an Adobe authentication token or a Netscape or JSON cookie export")
	}
	return "", time.Time{}, fmt.Errorf("those cookies do not include an Adobe authentication token. DirecTV website cookies cannot be replayed from this server. Use Safari or Firefox with the activation link, or paste the Adobe token")
}

func directToken(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "<authntoken>") || strings.Contains(lower, "simpletokenauthenticationguid")
}

func normalizeAuthnToken(raw string) (string, time.Time, error) {
	raw = strings.TrimSpace(raw)
	token, ok := extractAuthnToken([]byte(raw))
	if !ok {
		if strings.Contains(strings.ToLower(raw), "simpletokenauthenticationguid") {
			token = raw
		} else {
			return "", time.Time{}, fmt.Errorf("that text is not an Adobe authentication token")
		}
	}
	exp := tokenExpiry(token)
	if exp.IsZero() {
		return "", time.Time{}, fmt.Errorf("that Adobe token has no expiry, so it was not stored")
	}
	if !exp.After(time.Now().Add(time.Minute)) {
		return "", time.Time{}, fmt.Errorf("that Adobe token expired at %s", clock(exp))
	}
	return token, exp, nil
}

func cookieValues(raw string) []string {
	trim := strings.TrimSpace(raw)
	if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
		return jsonCookieValues(trim)
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		out = append(out, strings.Join(fields[6:], "\t"))
	}
	return out
}

func jsonCookieValues(raw string) []string {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case []any:
			for _, item := range n {
				walk(item)
			}
		case map[string]any:
			if value, ok := n["value"].(string); ok && value != "" {
				out = append(out, value)
			}
			if nested, ok := n["cookies"]; ok {
				walk(nested)
			}
		}
	}
	walk(parsed)
	return out
}

func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}
