package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamTraceMetadataDoesNotBecomeExecutionIdentity(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "same-http-trace")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "same-ingress-trace")
	first := controlledRequest(NewControlledRequestContext(ctx, "responses"))
	second := controlledRequest(NewControlledRequestContext(ctx, "responses"))
	require.NotEqual(t, first.ID, second.ID)
	for _, request := range []*ControlledRequest{first, second} {
		metrics := map[string]any{}
		request.addTraceMetrics(metrics)
		require.Equal(t, "same-http-trace", metrics["http_request_id"])
		require.Equal(t, "same-ingress-trace", metrics["client_request_id"])
		require.Equal(t, request.Started.UTC().Format(time.RFC3339Nano), metrics["request_started_at"])
		require.NotContains(t, metrics, "request_id")
	}
}

func TestNativeStreamTraceMetadataBoundsIngressLabels(t *testing.T) {
	for _, value := range []any{"", strings.Repeat("x", 65), string([]byte{0xff}), 42} {
		ctx := context.WithValue(context.Background(), ctxkey.RequestID, value)
		ctx = context.WithValue(ctx, ctxkey.ClientRequestID, value)
		request := controlledRequest(NewControlledRequestContext(ctx, "responses"))
		metrics := map[string]any{}
		request.addTraceMetrics(metrics)
		require.NotContains(t, metrics, "http_request_id")
		require.NotContains(t, metrics, "client_request_id")
		require.Contains(t, metrics, "request_started_at")
	}
}
