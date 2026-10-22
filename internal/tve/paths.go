package tve

import "strings"

// ProtectedPath reports paths that carry a TV Everywhere playlist, guide,
// stream redirect, or the DirecTV sign-in form. The HTTP gate calls this
// alongside the Pluto playlist and EPG paths, so one ACCESS_TOKEN check
// covers both. Health, metrics, status, and the Pluto tuner routes are
// not included here.
func ProtectedPath(path string) bool {
	switch path {
	case "/tve/directv", "/tve/directv/code", "/tve/directv/activate", "/tve/directv/import":
		return true
	}
	if !strings.HasPrefix(path, "/tve/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/tve/")
	if rest == "" || strings.Contains(rest, "..") {
		return false
	}
	if strings.HasSuffix(rest, ".m3u") || strings.HasSuffix(rest, ".xml") {
		return !strings.Contains(rest, "/")
	}
	// /tve/{source}/{channel}/stream
	parts := strings.Split(rest, "/")
	return len(parts) == 3 && parts[2] == "stream" && parts[0] != "" && parts[1] != ""
}
