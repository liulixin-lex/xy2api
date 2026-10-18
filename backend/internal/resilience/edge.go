package resilience

import (
	"net/http"
	"slices"
	"strings"
)

// EdgeHeader is set by the reverse proxy, from the peer address only, on
// requests that arrived through a CDN (design §7.1).
const EdgeHeader = "X-XY-Edge"

// Headers that CDNs add to the requests they forward to the origin (design
// §7.1). In production (2026-10-18) every Cloudflare origin request carried
// CF-Ray and "CDN-Loop: cloudflare; loops=N", every Alibaba Cloud ESA one
// "CDN-Loop: esa;loop=N", and direct requests neither.
const (
	CFRayHeader   = "CF-Ray"
	CDNLoopHeader = "CDN-Loop"
)

// cdnLoopEdges maps CDN-Loop identifiers to edge names.
var cdnLoopEdges = map[string]string{"cloudflare": "cf", "esa": "esa"}

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

// EdgeFromCDNHeaders infers the edge from the headers the CDN itself adds
// to origin requests, for requests without a valid EdgeHeader: a
// "cloudflare" CDN-Loop entry or CF-Ray is cf, an "esa" entry is esa.
// CDN-Loop lists the CDNs in the order the request passed them (RFC 8586),
// so the allowed one nearest the origin wins, before CF-Ray. The name must
// be in allowed: removing it from keepalive.edges turns its detection off.
// No trusted peer is needed: forging these headers only changes how the
// forger's own request is answered.
func EdgeFromCDNHeaders(h http.Header, allowed []string) string {
	var seen []string
	if h.Get(CFRayHeader) != "" {
		seen = append(seen, "cf")
	}
	for _, v := range h.Values(CDNLoopHeader) {
		for _, entry := range strings.Split(v, ",") {
			id, _, _ := strings.Cut(entry, ";")
			if edge := cdnLoopEdges[strings.ToLower(strings.TrimSpace(id))]; edge != "" {
				seen = append(seen, edge)
			}
		}
	}
	for i := len(seen) - 1; i >= 0; i-- {
		if slices.Contains(allowed, seen[i]) {
			return seen[i]
		}
	}
	return ""
}
