package tve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

func (s *Service) playWarner(ctx context.Context, st State, spec playSpec) (StreamTarget, error) {
	ch := warnerChannels[spec.channel]
	secrets := s.playbackSecrets(st)
	requestor, appID, err := s.warnerConfig(ctx, ch)
	if err != nil {
		return StreamTarget{}, err
	}
	spec.requestor = requestor
	spec.resource = requestor
	var short string
	if ch.brand == "tcm" {
		short, err = s.mediaTokenPreferDecision(ctx, st, requestor, requestor, true)
	} else {
		short, err = s.mediaToken(ctx, st, requestor, requestor)
	}
	if err != nil {
		return StreamTarget{}, err
	}
	secrets = append(secrets, short)
	q := url.Values{
		"appId":           {appID},
		"accessTokenType": {"adobe"},
		"accessToken":     {short},
		"format":          {"json"},
	}
	var ngtv fetched
	if ch.brand == "tcm" {
		ngtv, err = s.postJSON(ctx, "https://token.ngtv.io/token/token_ngtv?"+q.Encode(), map[string]any{}, nil, secrets)
	} else {
		q.Set("fname", "ngtv")
		ngtv, err = s.fetch(ctx, "https://token.ngtv.io/token/token_ngtv?"+q.Encode(), nil)
	}
	if err != nil {
		return StreamTarget{}, playFail("warner token", http.StatusBadGateway, redact(err.Error(), secrets...), true)
	}
	if fault := s.httpResult("warner token", ngtv, nil, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	if !warnerTokenOK(ngtv.Body) {
		return StreamTarget{}, playFail("warner token", http.StatusBadGateway, "response did not include an ngtv token", true)
	}
	desktop, err := s.fetch(ctx, "https://medium.ngtv.io/v2/media/"+url.PathEscape(ch.mediaID)+"/desktop?"+url.Values{"appId": {appID}}.Encode(), nil)
	if fault := s.httpResult("warner media", desktop, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	doc, err := decodeJSON(desktop.Body)
	if err != nil {
		return StreamTarget{}, playFail("warner media", http.StatusBadGateway, "response was not JSON", true)
	}
	hls := findHLS(doc)
	if hls == "" {
		return StreamTarget{}, playFail("warner media", http.StatusBadGateway, "response did not include an HLS URL", true)
	}
	return StreamTarget{Redirect: hls}, nil
}

func (s *Service) warnerConfig(ctx context.Context, ch warnerChannel) (string, string, error) {
	got, err := s.fetch(ctx, ch.page, map[string]string{"Accept": "text/html"})
	if fault := s.httpResult("warner config", got, err, nil); fault != nil {
		return "", "", fault
	}
	requestor, appID := parseWarnerConfig(got.Body, ch.brand)
	if requestor == "" || appID == "" {
		return "", "", playFail("warner config", http.StatusBadGateway, "the public page did not include an Adobe requestor and app id", true)
	}
	return requestor, appID, nil
}

func parseWarnerConfig(body []byte, brand string) (string, string) {
	raw := string(body)
	var requestor, appID string
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// The page is HTML. Pull the first JSON object that mentions the app id.
		for _, marker := range []string{"drupal-settings-json", "__NEXT_DATA__"} {
			if i := strings.Index(raw, marker); i >= 0 {
				chunk := raw[i:]
				if j := strings.Index(chunk, ">"); j >= 0 {
					chunk = chunk[j+1:]
				}
				if k := strings.Index(chunk, "</script>"); k >= 0 {
					chunk = chunk[:k]
				}
				if err := json.Unmarshal([]byte(strings.TrimSpace(chunk)), &v); err == nil {
					break
				}
			}
		}
	}
	walkWarner(v, &requestor, &appID)
	if requestor == "" {
		requestor = strings.ToUpper(brand)
		if brand == "trutv" {
			requestor = "truTV"
		}
	}
	return requestor, appID
}

func walkWarner(v any, requestor, appID *string) {
	switch n := v.(type) {
	case map[string]any:
		for _, key := range []string{"serviceAppId", "mediaAppId"} {
			if s := strings.TrimSpace(asText(n[key])); s != "" {
				*appID = s
			}
		}
		if s := strings.TrimSpace(asText(n["authBrand"])); s != "" {
			*requestor = s
		}
		if s := strings.TrimSpace(asText(n["brand"])); s != "" && strings.TrimSpace(asText(n["mediaAppId"])) != "" {
			*requestor = s
		}
		for _, child := range n {
			walkWarner(child, requestor, appID)
		}
	case []any:
		for _, child := range n {
			walkWarner(child, requestor, appID)
		}
	}
}

func warnerTokenOK(body []byte) bool {
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return false
	}
	auth, _ := doc["auth"].(map[string]any)
	return strings.TrimSpace(asText(auth["ngtv_token"])) != ""
}
