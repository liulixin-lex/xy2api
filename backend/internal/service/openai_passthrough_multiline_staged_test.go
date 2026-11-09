//go:build unit

package service

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const multilineReadyFrame = "event: relay.ready\ndata: {\"type\":\"relay.ready\",\ndata: \"source\":\"synthetic-gateway\",\"model_output\":false}\n\n"

func TestPassthroughMultilineReadyStagedTimeout(t *testing.T) {
	withResilienceSettings(t, nil)
	logs, restore := captureStructuredLog(t)
	defer restore()
	c, rec := compatStreamContext("/v1/responses", `{}`)
	state := resilience.NewRequestState()
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
	InstallDeliveryTracking(c, time.Now())
	reader, writer := io.Pipe()
	closeTimer := time.AfterFunc(1500*time.Millisecond, func() { _ = writer.Close() })
	defer closeTimer.Stop()
	defer func() { _ = writer.Close(); _ = reader.Close() }()
	go func() { _, _ = io.WriteString(writer, multilineReadyFrame) }()
	start := time.Now()
	_, err := runBlankPlaceholderAttempt(compatStreamService(nil, 1, 0, 0), c, rawChatCompletionsTestAccount(), true, reader, start)
	var failover *UpstreamFailoverError
	t.Logf("multiline: failover=%t bytes=%d staged=%t", errors.As(err, &failover), rec.Body.Len(), state.PreludeTrace().StagedAt.Equal(start))
	requireFirstOutputFailover(t, err)
	require.Empty(t, rec.Body.String())
	require.Equal(t, start, state.PreludeTrace().StagedAt)
	require.True(t, logs.ContainsMessage("staged=true"))
}

func TestPassthroughMultilineReadyReplaysWire(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := compatStreamContext("/v1/responses", `{}`)
	body := multilineReadyFrame + strings.TrimPrefix(unknownReadySuccessStream(), unknownReadyFrame)
	_, err := runBlankPlaceholderAttempt(compatStreamService(nil, 0, 0, 0), c, rawChatCompletionsTestAccount(), true,
		io.NopCloser(strings.NewReader(body)), time.Now())
	require.NoError(t, err)
	require.Equal(t, body, rec.Body.String(), "complete-event classification must preserve raw multi-data lines")
}
