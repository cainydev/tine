package gateway

import (
	"net/http"
	"strings"
)

// bearerToken extracts a token from the Authorization header.
//
// This header authenticates the caller to tine (Surface A) and is never
// forwarded upstream; upstream auth comes from the instance's own credential.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "

	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}
