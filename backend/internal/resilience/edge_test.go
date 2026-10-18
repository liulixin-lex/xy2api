package resilience

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Production 2026-10-18 (design §7.1): Cloudflare origin requests carry
// CF-Ray and "CDN-Loop: cloudflare; loops=N", Alibaba Cloud ESA origin
// requests "CDN-Loop: esa;loop=N", direct requests neither.
func TestEdgeFromCDNHeaders(t *testing.T) {
	edges := []string{"cf", "esa"}
	for _, tc := range []struct {
		name    string
		headers map[string][]string
		allowed []string
		want    string
	}{
		{"CF-Ray", map[string][]string{"Cf-Ray": {"9a1b2c3d4e5f6a7b-LAX"}}, edges, "cf"},
		{"CDN-Loop cloudflare", map[string][]string{"Cdn-Loop": {"cloudflare"}}, edges, "cf"},
		{"CDN-Loop cloudflare with loops", map[string][]string{"Cdn-Loop": {"cloudflare; loops=1"}}, edges, "cf"},
		{"CDN-Loop esa", map[string][]string{"Cdn-Loop": {"esa;loop=1"}}, edges, "esa"},
		{"case-insensitive", map[string][]string{"Cdn-Loop": {" ESA ; loop=2"}}, edges, "esa"},
		{"nearest CDN wins", map[string][]string{"Cdn-Loop": {"cloudflare; loops=1, esa;loop=1"}}, edges, "esa"},
		{"nearest allowed CDN wins", map[string][]string{"Cdn-Loop": {"cloudflare; loops=1", "esa;loop=1"}}, []string{"cf"}, "cf"},
		{"CDN-Loop over CF-Ray", map[string][]string{"Cf-Ray": {"x-LAX"}, "Cdn-Loop": {"esa;loop=1"}}, edges, "esa"},
		{"cf not in edges", map[string][]string{"Cf-Ray": {"x-LAX"}, "Cdn-Loop": {"cloudflare; loops=1"}}, []string{"esa"}, ""},
		{"esa not in edges", map[string][]string{"Cdn-Loop": {"esa;loop=1"}}, []string{"cf"}, ""},
		{"unknown CDN", map[string][]string{"Cdn-Loop": {"akamai"}}, edges, ""},
		{"no CDN headers", map[string][]string{"User-Agent": {"GGUU-AI-Image/0.0.16"}}, edges, ""},
		{"no edges at all", map[string][]string{"Cf-Ray": {"x-LAX"}}, nil, ""},
	} {
		h := http.Header{}
		for k, vs := range tc.headers {
			for _, v := range vs {
				h.Add(k, v)
			}
		}
		require.Equal(t, tc.want, EdgeFromCDNHeaders(h, tc.allowed), tc.name)
	}
}
