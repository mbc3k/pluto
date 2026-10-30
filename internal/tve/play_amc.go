package tve

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
)

const amcAPI = "https://gw.cds.amcn.com"

func (s *Service) playAMC(ctx context.Context, st State, spec playSpec) (StreamTarget, error) {
	ch := amcChannels[spec.channel]
	secrets := s.playbackSecrets(st)
	short, err := s.mediaTokenPreferDecision(ctx, st, spec.requestor, spec.resource, false)
	if err != nil {
		return StreamTarget{}, err
	}
	secrets = append(secrets, short)
	adobeID := tagText(st.AuthnToken, "simpleSamlNameID")
	mso := tagText(st.AuthnToken, "simpleTokenMsoID")
	if mso == "" {
		mso = "DTV"
	}
	device := st.DeviceID
	if device == "" {
		device = "pluto-tve"
	}
	headers := amcHeaders(ch, spec.channel, device, adobeID, mso, "unauth", "")
	unauth, err := s.postJSON(ctx, amcAPI+"/auth-orchestration-id/api/v1/unauth", map[string]any{}, headers, secrets)
	if fault := s.httpResult("amc unauth", unauth, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	unauthData := jsonData(unauth.Body)
	unauthToken := jsonString(unauthData, "access_token")
	if unauthToken == "" {
		return StreamTarget{}, playFail("amc unauth", http.StatusBadGateway, "response did not include an access token", true)
	}
	if id := jsonString(unauthData, "device-id"); id != "" {
		device = id
	}
	refreshHeaders := amcHeaders(ch, spec.channel, device, adobeID, mso, "unauth", unauthToken)
	refreshed, err := s.postJSON(ctx, amcAPI+"/auth-orchestration-id/api/v1/refresh", map[string]any{}, refreshHeaders, secrets)
	if fault := s.httpResult("amc refresh", refreshed, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	bearer := jsonString(jsonData(refreshed.Body), "access_token")
	if bearer == "" {
		bearer = unauthToken
	}
	authHeaders := amcHeaders(ch, spec.channel, device, adobeID, mso, "mvpd-auth", bearer)
	authed, err := s.postJSON(ctx, amcAPI+"/auth-orchestration-id/api/v1/mvpd-auth", map[string]any{}, authHeaders, secrets)
	if fault := s.httpResult("amc mvpd-auth", authed, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	mvpdToken := jsonString(jsonData(authed.Body), "access_token")
	if mvpdToken == "" {
		return StreamTarget{}, playFail("amc mvpd-auth", http.StatusBadGateway, "response did not include an access token", true)
	}
	playHeaders := amcHeaders(ch, spec.channel, device, adobeID, mso, "mvpd-auth", mvpdToken)
	body := map[string]any{
		"adobeShortMediaToken": short,
		"hba":                  false,
		"adtags": map[string]any{
			"lat":          0,
			"url":          "https://www." + ch.domain + "/live",
			"playerWidth":  1920,
			"playerHeight": 1080,
		},
	}
	played, err := s.postJSON(ctx, amcAPI+"/playback-id/api/v1/livestream/"+ch.liveID, body, playHeaders, secrets)
	if fault := s.httpResult("amc playback", played, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	doc, err := decodeJSON(played.Body)
	if err != nil {
		return StreamTarget{}, playFail("amc playback", http.StatusBadGateway, "response was not JSON", true)
	}
	hls := findHLS(doc)
	if hls == "" {
		return StreamTarget{}, playFail("amc playback", http.StatusBadGateway, "response did not include an HLS URL", true)
	}
	return StreamTarget{Redirect: hls}, nil
}

func amcHeaders(ch amcChannel, slug, device, adobeID, mso, entitlement, token string) map[string]string {
	origin := "https://www." + ch.domain
	h := map[string]string{
		"Origin":                  origin,
		"Referer":                 origin + "/",
		"x-amcn-adobe-id":         adobeID,
		"x-amcn-app-version":      "3.79.1",
		"x-amcn-country":          "us",
		"x-amcn-device-id":        device,
		"x-amcn-language":         "en-us",
		"x-amcn-mvpd":             mso,
		"x-amcn-network":          slug,
		"x-amcn-platform":         "web",
		"x-amcn-service-group-id": ch.group,
		"x-amcn-service-id":       slug,
		"x-amcn-tenant":           "amcn",
		"x-amcn-cache-hash":       base64.StdEncoding.EncodeToString([]byte("t=amcn;n=" + slug + ";p=web;e=" + entitlement + ";c=us;l=en-us;av=3.79.1")),
		"x-amcn-user-cache-hash":  base64.StdEncoding.EncodeToString([]byte("a=" + adobeID + ";d=" + device + ";c=us;l=en-us;av=3.79.1")),
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return h
}

func jsonData(body []byte) map[string]any {
	doc, err := decodeJSON(body)
	if err != nil {
		return nil
	}
	root, _ := doc.(map[string]any)
	if data, ok := root["data"].(map[string]any); ok {
		return data
	}
	return root
}

func jsonString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(asText(m[key]))
}
