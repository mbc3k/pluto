package tve

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

// StoredCookie is the subset of a cookie the session file keeps so a later
// code submission can resume the same DirecTV login. Values stay in the file
// and out of logs.
type StoredCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HttpOnly bool      `json:"http_only,omitempty"`
	Host     string    `json:"host,omitempty"`
}

func exportCookies(client *http.Client, extra ...string) []StoredCookie {
	if client == nil || client.Jar == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []StoredCookie
	pages := append([]string{
		"https://identity.directv.com/",
		"https://idp.dtvce.com/",
		"https://sp.auth.adobe.com/",
		"https://api.cld.dtvce.com/",
	}, extra...)
	for _, raw := range pages {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		for _, c := range client.Jar.Cookies(u) {
			key := c.Name + "\x00" + c.Domain + "\x00" + c.Path
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			path := c.Path
			if path == "" {
				path = "/"
			}
			out = append(out, StoredCookie{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Host: u.Host, Path: path,
				Expires: c.Expires, Secure: c.Secure, HttpOnly: c.HttpOnly,
			})
		}
	}
	return out
}

func restoreCookies(client *http.Client, cookies []StoredCookie) {
	if client == nil || len(cookies) == 0 {
		return
	}
	if client.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return
		}
		client.Jar = jar
	}
	for _, c := range cookies {
		if c.Name == "" {
			continue
		}
		host := c.Host
		if host == "" {
			host = c.Domain
		}
		if host == "" {
			continue
		}
		scheme := "https"
		if !c.Secure {
			scheme = "http"
		}
		client.Jar.SetCookies(&url.URL{Scheme: scheme, Host: host, Path: "/"}, []*http.Cookie{{
			Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain,
			Expires: c.Expires, Secure: c.Secure, HttpOnly: c.HttpOnly,
		}})
	}
}

func newBrowserClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}
}
