package tve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	foxLoginAPI = "https://api3.fox.com/v2.0"
	foxDVPAPI   = "https://prod.api.digitalvideoplatform.com/sports/v3.0/partner-playback/live"
)

func (s *Service) playFoxNews(ctx context.Context, st State, spec playSpec) (StreamTarget, error) {
	ch := foxNewsChannels[spec.channel]
	secrets := s.playbackSecrets(st)
	short, err := s.mediaToken(ctx, st, ch.requestor, ch.resource)
	if err != nil {
		return s.foxNewsReferer(ctx, ch, err)
	}
	secrets = append(secrets, short)
	page := strings.TrimRight(ch.page, "/")
	generated, err := s.postJSON(ctx, ch.generator, map[string]string{"mediaToken": short}, map[string]string{
		"Origin":  page,
		"Referer": page + "/",
	}, secrets)
	if fault := s.httpResult("fox token", generated, err, secrets); fault != nil {
		return s.foxNewsReferer(ctx, ch, fault)
	}
	parsed, err := decodeJSON(generated.Body)
	if err != nil {
		return StreamTarget{}, playFail("fox token", http.StatusBadGateway, "response was not JSON", true)
	}
	root, _ := parsed.(map[string]any)
	token := strings.TrimSpace(asText(root["token"]))
	if !strings.Contains(token, "=") {
		return StreamTarget{}, playFail("fox token", http.StatusBadGateway, "response did not include an HLS token", true)
	}
	signed := ch.master + "?" + token
	secrets = append(secrets, token)
	checked, err := s.fetch(ctx, signed, map[string]string{
		"Accept":  "application/vnd.apple.mpegurl,*/*",
		"Referer": ch.page,
	})
	if fault := s.httpResult("fox manifest", checked, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	if !strings.Contains(string(checked.Body), "#EXTM3U") {
		return StreamTarget{}, playFail("fox manifest", http.StatusBadGateway, "response was not an HLS playlist", true)
	}
	return StreamTarget{Redirect: signed}, nil
}

func (s *Service) foxNewsReferer(ctx context.Context, ch foxNewsChannel, prior error) (StreamTarget, error) {
	got, err := s.fetch(ctx, ch.master, map[string]string{
		"Accept":  "application/vnd.apple.mpegurl,*/*",
		"Referer": ch.page,
		"Origin":  strings.TrimRight(ch.page, "/"),
	})
	if err != nil || got.Status < 200 || got.Status >= 300 || !strings.Contains(string(got.Body), "#EXTM3U") {
		return StreamTarget{}, prior
	}
	return StreamTarget{Body: absolutize(ch.master, got.Body)}, nil
}

func (s *Service) playFoxSports(ctx context.Context, st State, spec playSpec) (StreamTarget, error) {
	secrets := s.playbackSecrets(st)
	if _, err := s.mediaToken(ctx, st, spec.requestor, spec.resource); err != nil {
		return StreamTarget{}, err
	}
	streamID, err := s.foxSportsStream(ctx, spec.channel)
	if err != nil {
		return StreamTarget{}, err
	}
	device := st.DeviceID
	if device == "" {
		device = st.Fingerprint
	}
	if device == "" {
		device = "pluto-tve"
	}
	headers := map[string]string{
		"x-api-key":             foxSportsKey,
		"X-Delegated-Auth-Flow": "true",
	}
	anon, err := s.postJSON(ctx, foxLoginAPI+"/login", map[string]string{"deviceId": device}, headers, secrets)
	if fault := s.httpResult("fox sports login", anon, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	anonToken := jsonString(jsonData(anon.Body), "accessToken")
	if anonToken == "" {
		return StreamTarget{}, playFail("fox sports login", http.StatusBadGateway, "response did not include an access token", true)
	}
	secrets = append(secrets, anonToken)
	checkURL := foxLoginAPI + "/checkadobeauthn/v2?" + url.Values{
		"device_id": {device},
		"requestor": {foxSportsRequestor},
	}.Encode()
	checked, err := s.fetch(ctx, checkURL, map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + anonToken,
		"x-api-key":     foxSportsKey,
	})
	if fault := s.httpResult("fox sports checkadobeauthn", checked, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	access := jsonString(jsonData(checked.Body), "accessToken")
	if access == "" {
		return StreamTarget{}, playFail("fox sports checkadobeauthn", http.StatusBadGateway, "response did not include an access token", true)
	}
	mso := tagText(st.AuthnToken, "simpleTokenMsoID")
	if mso == "" {
		mso = "DTV"
	}
	if claim, ok := jwtClaim(access, "mvpdid"); ok && !strings.EqualFold(claim, mso) {
		return StreamTarget{}, playFail("fox sports checkadobeauthn", http.StatusForbidden, "Fox does not have this device signed in to "+mso, false)
	}
	secrets = append(secrets, access)
	played, err := s.postJSON(ctx, foxDVPAPI, map[string]any{
		"streamId":     streamID,
		"streamType":   "live",
		"capabilities": []string{"drm/widevine", "fsdk/yo"},
		"deviceWidth":  1280,
		"deviceHeight": 720,
		"maxRes":       "720p",
		"os":           "macos",
		"provider": map[string]any{
			"freewheel": map[string]string{"did": device},
		},
		"privacy": map[string]string{"us": "1---"},
	}, map[string]string{
		"x-api-key":      foxSportsKey,
		"x-access-token": "Bearer " + access,
	}, secrets)
	if fault := s.httpResult("fox sports playback", played, err, secrets); fault != nil {
		return StreamTarget{}, fault
	}
	doc, err := decodeJSON(played.Body)
	if err != nil {
		return StreamTarget{}, playFail("fox sports playback", http.StatusBadGateway, "response was not JSON", true)
	}
	hls := findHLS(doc)
	if hls == "" {
		hls = jsonString(jsonData(played.Body), "playbackUrl")
	}
	if hls == "" {
		return StreamTarget{}, playFail("fox sports playback", http.StatusBadGateway, "response did not include a playback URL", true)
	}
	return StreamTarget{Redirect: hls}, nil
}

func jwtClaim(token, name string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 || parts[1] == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return "", false
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return "", false
	}
	value, ok := doc[name]
	if !ok {
		return "", false
	}
	return strings.TrimSpace(asText(value)), true
}

func (s *Service) foxSportsStream(ctx context.Context, channelID string) (string, error) {
	sign := foxSportsCall[channelID]
	now := time.Now().UTC()
	q := url.Values{
		"callsign":   {sign + "," + sign + "-DIGITAL"},
		"start_date": {fmt.Sprint(now.Add(-2 * time.Hour).Unix())},
		"end_date":   {fmt.Sprint(now.Add(6 * time.Hour).Unix())},
		"size":       {"20"},
		"video_type": {"listing"},
	}
	got, err := s.fetch(ctx, foxSportsURL+"?"+q.Encode(), map[string]string{
		"Accept":       "application/json",
		"x-fox-apikey": foxSportsKey,
	})
	if fault := s.httpResult("fox sports listing", got, err, nil); fault != nil {
		return "", fault
	}
	doc, err := decodeJSON(got.Body)
	if err != nil {
		return "", playFail("fox sports listing", http.StatusBadGateway, "response was not JSON", true)
	}
	root, _ := doc.(map[string]any)
	data, _ := root["data"].(map[string]any)
	listings, _ := data["listings"].(map[string]any)
	var chosen string
	for _, item := range asList(listings["items"]) {
		row, _ := item.(map[string]any)
		if row == nil || !strings.EqualFold(asText(row["call_sign"]), sign) && !strings.EqualFold(asText(row["call_sign"]), sign+"-DIGITAL") {
			continue
		}
		start, ok1 := foxTime(row["start_time"])
		stop, ok2 := foxTime(row["end_time"])
		if !ok1 || !ok2 || now.Before(start) || !now.Before(stop) {
			continue
		}
		if id := strings.TrimSpace(asText(row["entity_id"])); id != "" {
			chosen = id
			if strings.EqualFold(asText(row["airing_type"]), "live") {
				return id, nil
			}
		}
	}
	if chosen == "" {
		return "", playFail("fox sports listing", http.StatusBadGateway, "no current listing for "+sign, true)
	}
	return chosen, nil
}
