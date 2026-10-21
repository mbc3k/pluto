package tve

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginHappyPath(t *testing.T) {
	const user = "viewer@example.com"
	const pass = "s3cret-password"
	srv := newDirecTVServer(t, user, pass, false)
	defer srv.Close()

	client := newBrowserClient()
	out, err := Login(context.Background(), client, testEndpoints(srv), startForm(srv), "https://sp.auth.adobe.com/api/v2/HISTORY/sessions?mso_id=DTV", user, pass)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || out.Pending != nil || !strings.Contains(out.FinalURL, "/acs") {
		t.Fatalf("%+v", out)
	}
}

func TestLoginPausesForCodeAndResume(t *testing.T) {
	const user = "viewer@example.com"
	const pass = "s3cret-password"
	srv := newDirecTVServer(t, user, pass, true)
	defer srv.Close()

	client := newBrowserClient()
	out, err := Login(context.Background(), client, testEndpoints(srv), startForm(srv), "https://sp.auth.adobe.com/start", user, pass)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || out.Pending == nil {
		t.Fatalf("expected a pause, got %+v", out)
	}
	if !strings.Contains(out.Pending.Prompt, "Enter the code sent to your phone") {
		t.Fatalf("prompt %q", out.Pending.Prompt)
	}
	if out.Pending.Kind != "OneTimePasswordCallback" {
		t.Fatalf("kind %s", out.Pending.Kind)
	}
	var kept string
	for _, c := range out.Pending.Cookies {
		if c.Name == "sid" {
			kept = c.Value
		}
	}
	if kept != "session-token" {
		t.Fatalf("cookies %#v", out.Pending.Cookies)
	}

	path := filepath.Join(t.TempDir(), "session.json")
	if err := saveState(path, State{Pending: out.Pending}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, pass) || strings.Contains(text, user) || strings.Contains(text, `"password"`) {
		t.Fatalf("session file leaked credentials:\n%s", text)
	}
	loaded, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Pending == nil || loaded.Pending.Prompt != out.Pending.Prompt {
		t.Fatalf("loaded %#v", loaded.Pending)
	}

	resumed, err := Resume(context.Background(), newBrowserClient(), testEndpoints(srv), loaded.Pending, "123456", pass, user)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Pending != nil || !strings.Contains(resumed.FinalURL, "/acs") {
		t.Fatalf("%+v", resumed)
	}
	saved := State{Signed: true, FinalURL: resumed.FinalURL}
	if err := saveState(path, saved); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pass) {
		t.Fatal("signed session stored the password")
	}
}

func TestLoginRedactsCredentials(t *testing.T) {
	const user = "viewer@example.com"
	const pass = "s3cret-password"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/saml":
			http.Redirect(w, r, srv.URL+"/weblogin/authorize?code_challenge=chal&redirect_uri="+url.QueryEscape(srv.URL+"/cb"), http.StatusFound)
		case "/weblogin/authorize":
			fmt.Fprint(w, "ok")
		default:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "rejected %s %s", user, pass)
		}
	}))
	defer srv.Close()

	_, err := Login(context.Background(), newBrowserClient(), testEndpoints(srv), startForm(srv), "https://sp.auth.adobe.com/start", user, pass)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), pass) || strings.Contains(err.Error(), user) {
		t.Fatalf("error leaked credentials: %v", err)
	}
}

func testEndpoints(srv *httptest.Server) Endpoints {
	return Endpoints{AuthURL: srv.URL + "/am/IdPwdAuth", AuthorizeURL: srv.URL + "/authorize"}
}

func startForm(srv *httptest.Server) string {
	return fmt.Sprintf(`<form method="post" action="%s/saml"><input type="hidden" name="SAMLRequest" value="req&amp;x"></form>`, srv.URL)
}

func newDirecTVServer(t *testing.T, user, pass string, pause bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/saml":
			if err := r.ParseForm(); err != nil {
				t.Errorf("saml form: %v", err)
			}
			if r.PostForm.Get("SAMLRequest") != "req&x" {
				t.Errorf("SAMLRequest %q", r.PostForm.Get("SAMLRequest"))
			}
			dest := srv.URL + "/weblogin/authorize?code_challenge=chal&redirect_uri=" + url.QueryEscape(srv.URL+"/cb") + "&response_type=code&state=st&fr_client_id=fr_idp_02"
			http.Redirect(w, r, dest, http.StatusFound)
		case "/weblogin/authorize":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "session-token", Path: "/"})
			fmt.Fprint(w, "login")
		case "/am/IdPwdAuth":
			if r.Header.Get("client_id") != forgeRockClientID {
				t.Errorf("client_id %q", r.Header.Get("client_id"))
			}
			if r.Header.Get("Accept-API-Version") == "" {
				t.Error("missing Accept-API-Version")
			}
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			switch {
			case text == "{}":
				fmt.Fprint(w, `{"callbacks":[{"type":"NameCallback","output":[{"name":"prompt","value":"User Name"}],"input":[{"name":"IDToken1","value":""}]}]}`)
			case strings.Contains(text, user) && !strings.Contains(text, pass):
				fmt.Fprint(w, `{"callbacks":[{"type":"PasswordCallback","output":[{"name":"prompt","value":"Password"}],"input":[{"name":"IDToken2","value":""}]}]}`)
			case strings.Contains(text, pass) && pause && !strings.Contains(text, "123456"):
				fmt.Fprint(w, `{"authId":"a","callbacks":[{"type":"OneTimePasswordCallback","output":[{"name":"prompt","value":"Enter the code sent to your phone"}],"input":[{"name":"IDToken1","value":""}]}]}`)
			case strings.Contains(text, "123456") || strings.Contains(text, pass):
				if c, err := r.Cookie("sid"); err != nil || c.Value != "session-token" {
					if strings.Contains(text, "123456") {
						t.Errorf("resume did not send the stored cookie: %v", err)
					}
				}
				fmt.Fprint(w, `{"tokenId":"tid"}`)
			default:
				t.Errorf("unexpected challenge %s", text)
				http.Error(w, "no", http.StatusBadRequest)
			}
		case "/authorize":
			q := r.URL.Query()
			if q.Get("code_challenge") != "chal" || q.Get("client_id") != forgeRockClientID || q.Get("code_challenge_method") != "S256" || q.Get("state") != "st" {
				t.Errorf("authorize query %s", r.URL.RawQuery)
			}
			if strings.Contains(r.URL.RawQuery, pass) || strings.Contains(r.URL.RawQuery, user) {
				t.Error("credentials leaked into the authorize URL")
			}
			http.Redirect(w, r, srv.URL+"/consume", http.StatusFound)
		case "/consume":
			fmt.Fprintf(w, `<html><form action="%s/acs" method="post"><input type="hidden" name="SAMLResponse" value="saml&amp;ok"></form></html>`, srv.URL)
		case "/acs":
			if err := r.ParseForm(); err != nil {
				t.Errorf("acs form: %v", err)
			}
			if r.PostForm.Get("SAMLResponse") != "saml&ok" {
				t.Errorf("SAMLResponse %q", r.PostForm.Get("SAMLResponse"))
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}
