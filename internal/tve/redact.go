package tve

import "strings"

// redact removes credential material from a string before it is logged or
// returned to a client. Cookie values and the DirecTV password never belong
// in an error.
func redact(msg string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < 3 {
			continue
		}
		msg = strings.ReplaceAll(msg, secret, "REDACTED")
	}
	return msg
}
