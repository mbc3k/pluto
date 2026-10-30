package tve

import (
	"context"
	"encoding/base64"
	"log/slog"
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
	headers := amcHeaders(ch, spec.channel, device, adobeID, mso, "unauth", "", "", "")
	unauthURL := amcAPI + "/auth-orchestration-id/api/v1/unauth"
	unauth, err := s.postJSON(ctx, unauthURL, map[string]any{}, headers, secrets)
	s.logStep(spec, "amc unauth", http.MethodPost, unauthURL, unauth.Status)
	if fault := s.httpResult("amc unauth", unauth, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	session, ok := amcSession(unauth.Body, device)
	if !ok {
		return StreamTarget{}, playFail("amc unauth", http.StatusBadGateway, "response did not include an access token", true)
	}
	device = session.device
	secrets = append(secrets, session.refresh)
	if session.refresh != "" {
		refreshURL := amcAPI + "/auth-orchestration-id/api/v1/refresh"
		refreshHeaders := amcHeaders(ch, spec.channel, device, adobeID, mso, "unauth", session.refresh, session.cache, session.user)
		refreshed, err := s.postJSON(ctx, refreshURL, map[string]any{}, refreshHeaders, secrets)
		s.logStep(spec, "amc refresh", http.MethodPost, refreshURL, refreshed.Status)
		if fault := s.httpResult("amc refresh", refreshed, err, secrets); fault != nil {
			if fault.Status != http.StatusUnauthorized && fault.Status != http.StatusForbidden {
				return StreamTarget{}, fault
			}
			slog.Warn("tve playback step", "source", spec.source, "channel", spec.channel, "step", "amc refresh", "status", fault.Status, "detail", "continuing with the unauth token")
		} else if next, ok := amcSession(refreshed.Body, device); ok {
			session = next
			secrets = append(secrets, session.refresh)
		}
	}
	authURL := amcAPI + "/auth-orchestration-id/api/v1/mvpd-auth"
	authHeaders := amcHeaders(ch, spec.channel, device, adobeID, mso, "mvpd-auth", session.token, session.cache, session.user)
	authed, err := s.postJSON(ctx, authURL, map[string]any{}, authHeaders, secrets)
	s.logStep(spec, "amc mvpd-auth", http.MethodPost, authURL, authed.Status)
	if fault := s.httpResult("amc mvpd-auth", authed, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	mvpd, ok := amcSession(authed.Body, device)
	if !ok {
		return StreamTarget{}, playFail("amc mvpd-auth", http.StatusBadGateway, "response did not include an access token", true)
	}
	device = mvpd.device
	playURL := amcAPI + "/playback-id/api/v1/livestream/" + ch.liveID
	playHeaders := amcHeaders(ch, spec.channel, device, adobeID, mso, "mvpd-auth", mvpd.token, mvpd.cache, mvpd.user)
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
	played, err := s.postJSON(ctx, playURL, body, playHeaders, secrets)
	s.logStep(spec, "amc playback", http.MethodPost, playURL, played.Status)
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

type amcCreds struct {
	token   string
	refresh string
	cache   string
	user    string
	device  string
}

func amcSession(body []byte, device string) (amcCreds, bool) {
	data := jsonData(body)
	token := jsonString(data, "access_token")
	if token == "" {
		return amcCreds{}, false
	}
	if id := jsonString(data, "device-id"); id != "" {
		device = id
	} else if id, ok := jwtClaim(token, "device-id"); ok && id != "" {
		device = id
	}
	cache := jsonString(data, "cache_hash")
	user := jsonString(data, "user_cache_hash")
	if cache == "" {
		cache = jsonString(data, "cacheHash")
	}
	return amcCreds{
		token:   token,
		refresh: jsonString(data, "refresh_token"),
		cache:   cache,
		user:    user,
		device:  device,
	}, true
}

func amcHeaders(ch amcChannel, slug, device, adobeID, mso, entitlement, token, cacheHash, userHash string) map[string]string {
	origin := "https://www." + ch.domain
	if cacheHash == "" {
		cacheHash = base64.StdEncoding.EncodeToString([]byte("t=amcn;n=" + slug + ";p=web;e=" + entitlement + ";c=us;l=en-us;av=3.79.1"))
	}
	if userHash == "" {
		userHash = base64.StdEncoding.EncodeToString([]byte("d=" + device + ";c=us;l=en-us;av=3.79.1"))
	}
	h := map[string]string{
		"Origin":                  origin,
		"Referer":                 origin + "/",
		"x-amcn-adobe-id":         adobeID,
		"x-amcn-app-version":      "3.79.1",
		"x-amcn-audience-id":      "",
		"x-amcn-country":          "us",
		"x-amcn-device-ad-id":     device,
		"x-amcn-device-id":        device,
		"x-amcn-language":         "en-us",
		"x-amcn-mvpd":             mso,
		"x-amcn-network":          slug,
		"x-amcn-platform":         "web",
		"x-amcn-service-group-id": ch.group,
		"x-amcn-service-id":       slug,
		"x-amcn-tenant":           "amcn",
		"x-amcn-cache-hash":       cacheHash,
		"x-amcn-user-cache-hash":  userHash,
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
