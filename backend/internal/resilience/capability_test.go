package resilience

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestStreamOnlyFailureRequiresDeterministicError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"group message", 400, `{"error":{"message":"This group only accepts streaming requests"}}`, true},
		{"stream true", 400, `{"error":{"message":"stream must be true"}}`, true},
		{"quoted stream", 400, `{"error":{"message":"\"stream\" must be true"}}`, true},
		{"structured code", 400, `{"error":{"code":"streaming_required"}}`, true},
		{"nested response", 400, `{"response":{"error":{"message":"Only streaming requests are supported"}}}`, true},
		{"plain text", 400, "This model only supports streaming requests", true},
		{"wrong status", 500, `{"error":{"message":"stream must be true"}}`, false},
		{"echoed user content", 400, `{"error":{"message":"invalid request"},"request":{"input":"stream must be true"}}`, false},
		{"truncated json", 400, `{"request":{"input":"stream must be true"`, false},
		{"opposite mode", 400, `{"error":{"message":"stream must be false"}}`, false},
		{"ordinary 400", 400, `{"error":{"message":"unknown parameter stream_options"}}`, false},
		{"content refusal", 400, `{"error":{"code":"content_policy_violation","message":"stream must be true"}}`, false},
		{"balance takes precedence", 400, `{"error":{"code":"insufficient_quota","message":"stream must be true"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, IsStreamOnlyFailure(tc.status, []byte(tc.body))) })
	}
}

func TestCapabilityRequestScopeIsExplicit(t *testing.T) {
	_, _, ok := CapabilityRequestFrom(context.Background())
	require.False(t, ok)
	for _, protocol := range []string{"responses", "chat"} {
		for _, stream := range []bool{false, true} {
			ctx := WithCapabilityRequest(context.Background(), protocol, stream)
			gotProtocol, gotStream, ok := CapabilityRequestFrom(ctx)
			require.True(t, ok)
			require.Equal(t, protocol, gotProtocol)
			require.Equal(t, stream, gotStream)
		}
	}
	_, _, ok = CapabilityRequestFrom(WithCapabilityRequest(context.Background(), "messages", false))
	require.False(t, ok)
}

func TestCapabilityExclusionsAreRequestLocal(t *testing.T) {
	first := WithCapabilityRequest(context.Background(), "responses", false)
	second := WithCapabilityRequest(context.Background(), "responses", false)
	NoteCapabilityExcluded(first, 7)
	require.True(t, CapabilityExcluded(first, 7))
	require.True(t, CapabilityExcluded(first, 0))
	require.False(t, CapabilityExcluded(second, 7))
	require.False(t, CapabilityExcluded(context.Background(), 7))
}

func TestStreamOnlyFailureRejectsOtherParameterAndRequestEcho(t *testing.T) {
	for _, tc := range []struct {
		name, response, outbound string
		want                     bool
	}{
		{"different parameter", `{"error":{"message":"Invalid value: 'only accepts streaming requests'","param":"reasoning.effort"}}`, "", false},
		{"stream options", `{"error":{"message":"stream must be true","param":"stream_options"}}`, "", false},
		{"stream parameter", `{"error":{"message":"stream must be true","param":"stream"}}`, "", true},
		{"null parameter", `{"error":{"code":"streaming_required","param":null}}`, "", false},
		{"response envelope parameter", `{"response":{"error":{"code":"streaming_required","param":"input"}}}`, "", false},
		{"case insensitive echo", `{"error":{"message":"only accepts streaming requests"}}`, `{"input":"ONLY ACCEPTS STREAMING REQUESTS","stream":false}`, false},
		{"escaped echo", `{"error":{"message":"only accepts streaming requests"}}`, `{"input":"only accepts \u0073treaming requests","stream":false}`, false},
		{"quoted stream echo", `{"error":{"message":"only accepts streaming requests"}}`, `{"input":"\"stream\" must be true","stream":false}`, false},
		{"ordinary request", `{"error":{"message":"only accepts streaming requests"}}`, `{"input":"hello","stream":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsStreamOnlyFailure(400, []byte(tc.response), []byte(tc.outbound)))
		})
	}
}
