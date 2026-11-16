//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsUpstreamCapabilityFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"model code", 502, `{"error":{"code":"MODEL_UNSUPPORTED"}}`, true},
		{"model 404", 404, `{"error":{"message":"model is not supported"}}`, true},
		{"protocol", 400, `{"error":{"code":"unsupported_protocol"}}`, true},
		{"site request", 400, `{"error":{"message":"This site does not support this request"}}`, true},
		{"stream only", 400, `{"error":{"message":"Only supports streaming requests"}}`, true},
		{"nested", 400, `{"response":{"error":{"code":"model_not_found"}}}`, true},
		{"generic 404", 404, `{"error":{"message":"Not found"}}`, false},
		{"parameter", 400, `{"error":{"message":"Unsupported parameter: temperature"}}`, false},
		{"generic feature", 503, `{"error":{"code":"unsupported_feature"}}`, false},
		{"request echo", 400, `{"input":{"error":{"code":"model_not_found"}}}`, false},
		{"successful output", 200, `{"error":{"code":"model_not_found"}}`, false},
		{"malformed", 400, `{"error":{"code":"model_not_found"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, IsUpstreamCapabilityFailure(tc.status, []byte(tc.body))) })
	}
}
