package resilience

import "strings"

// EdgeHeader is set by the reverse proxy, from the peer address only, on
// requests that arrived through a CDN (design §7.1).
const EdgeHeader = "X-XY-Edge"

// EdgeOf returns the normalised edge marker of a request, or "" when the
// request did not come through a trusted proxy or carries no known marker.
// trustedPeer must be true only when the TCP peer is a configured trusted
// proxy; otherwise a client could forge the header.
func EdgeOf(headerValue string, trustedPeer bool, allowed []string) string {
	if !trustedPeer {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(headerValue))
	if v == "" {
		return ""
	}
	for _, e := range allowed {
		if v == e {
			return v
		}
	}
	return ""
}
