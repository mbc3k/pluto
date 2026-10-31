package server

import "runtime/debug"

// Commit is the VCS revision recorded by the Go toolchain. A build that did
// not record one reports "unknown". The ldflags version can still be "dev"
// when the binary was not built through make.
func Commit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev := ""
	dirty := ""
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev + dirty
}
