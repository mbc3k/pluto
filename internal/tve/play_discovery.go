package tve

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

func (s *Service) playDiscovery(ctx context.Context, st State, spec playSpec) (StreamTarget, error) {
	ch := discoveryChannels[spec.channel]
	secrets := s.playbackSecrets(st)
	if _, err := s.mediaToken(ctx, st, spec.requestor, spec.resource); err != nil {
		return StreamTarget{}, err
	}
	device := st.DeviceID
	if device == "" {
		device = "pluto-tve"
	}
	headers := map[string]string{
		"Accept":         "application/json",
		"Origin":         "https://watch.hgtv.com",
		"Referer":        "https://watch.hgtv.com/",
		"x-disco-client": "WEB:UNKNOWN:hgtv:27.43.0",
		"x-disco-params": "realm=go,siteLookupKey=hgtv",
		"x-device-info":  "hgtv/27.43.0 (desktop/desktop; unknown/unknown; " + device + ")",
	}
	tokenURL := discoveryAPI + "/token?" + url.Values{
		"realm":      {"go"},
		"deviceId":   {device},
		"shortlived": {"true"},
	}.Encode()
	issued, err := s.fetch(ctx, tokenURL, headers)
	if fault := s.httpResult("discovery token", issued, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	if cookie := cookieHeader(issued.Header); cookie != "" {
		headers["Cookie"] = cookie
	}
	payload := map[string]any{
		"channelId": ch.id,
		"deviceInfo": map[string]any{
			"adBlocker":         false,
			"drmSupported":      true,
			"hdrCapabilities":   []string{"SDR"},
			"player":            map[string]int{"width": 1920, "height": 1080},
			"soundCapabilities": []string{"STEREO"},
		},
		"wisteriaProperties": map[string]any{
			"platform":       "desktop",
			"product":        "hgtv",
			"siteId":         "hgtv",
			"playbackId":     device,
			"sessionId":      device,
			"gdpr":           0,
			"streamProvider": map[string]any{"hlsVersion": 7, "version": "1.0.0"},
		},
	}
	played, err := s.postJSON(ctx, discoveryAPI+"/playback/v3/channelPlaybackInfo", payload, headers, secrets)
	if fault := s.httpResult("discovery playback", played, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	doc, err := decodeJSON(played.Body)
	if err != nil {
		return StreamTarget{}, playFail("discovery playback", http.StatusBadGateway, "response was not JSON", true)
	}
	hls := discoveryHLS(doc)
	if hls == "" {
		hls = findHLS(doc)
	}
	if hls == "" {
		return StreamTarget{}, playFail("discovery playback", http.StatusBadGateway, "response did not include an HLS URL", false)
	}
	return StreamTarget{Redirect: hls}, nil
}

func cookieHeader(h http.Header) string {
	var parts []string
	for _, raw := range h.Values("Set-Cookie") {
		pair := raw
		if i := strings.Index(pair, ";"); i >= 0 {
			pair = pair[:i]
		}
		pair = strings.TrimSpace(pair)
		if pair == "" || !strings.Contains(pair, "=") {
			continue
		}
		parts = append(parts, pair)
	}
	return strings.Join(parts, "; ")
}

func discoveryHLS(v any) string {
	root, _ := v.(map[string]any)
	data, _ := root["data"].(map[string]any)
	attrs, _ := data["attributes"].(map[string]any)
	streams, _ := attrs["streaming"].([]any)
	var drm string
	for _, item := range streams {
		row, _ := item.(map[string]any)
		if row == nil {
			continue
		}
		u := jsonString(row, "url")
		if !strings.Contains(u, ".m3u8") {
			continue
		}
		protection, _ := row["protection"].(map[string]any)
		if enabled, _ := protection["drmEnabled"].(bool); enabled {
			if drm == "" {
				drm = u
			}
			continue
		}
		if jsonString(row, "type") == "hls" || jsonString(row, "type") == "" {
			return u
		}
	}
	return drm
}
