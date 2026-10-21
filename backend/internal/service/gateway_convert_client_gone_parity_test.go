//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Review DELTA-P2-2, PM instruction 2: a heartbeat that cannot be written
// while the opening is staged, with the request context still alive, is a
// departed client as on the OpenAI compatibility paths (PM7, 766a54fc8 and
// c7290e396): no switch, ClientDisconnect set, the upstream read to its end
// for complete usage, and no account failure. Whether the user is charged
// is left to the delivery rules, the same for every path. Only a cancelled
// request context bills nothing.

type clientGoneOutcome struct {
	failover         bool
	err              bool
	clientDisconnect bool
	inputTokens      int
	outputTokens     int
	accountFailed    bool
	turnFailed       bool
	opsReported      bool
	bodyEmpty        bool
	failedWrites     int
}

func TestStagedHeartbeatFailureOutcomeMatchesCompatPath(t *testing.T) {
	withResilienceSettings(t, nil)
	compat := runStagedHeartbeatFailure(t, "openai_compat_chat")
	require.Equal(t, clientGoneOutcome{clientDisconnect: true, inputTokens: 13, outputTokens: 21, bodyEmpty: true, failedWrites: 1}, compat,
		"0.4.4 compatibility path reference")
	for _, path := range []string{"gateway_chat", "gateway_responses"} {
		t.Run(path, func(t *testing.T) {
			require.Equal(t, compat, runStagedHeartbeatFailure(t, path))
		})
	}
}

// runStagedHeartbeatFailure: the opening, a heartbeat that fails, content
// before the 2 s first-output deadline and completion after it.
func runStagedHeartbeatFailure(t *testing.T, path string) clientGoneOutcome {
	t.Helper()
	c, rec := compatStreamContext("/v1/chat/completions", "")
	if path == "gateway_responses" {
		c.Request.URL.Path = "/v1/responses"
	}
	state := resilience.NewRequestState()
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
	failedHeartbeat := make(chan struct{})
	var once sync.Once
	writer := &openAICompatFailingWriter{ResponseWriter: c.Writer, onFailure: func() { once.Do(func() { close(failedHeartbeat) }) }}
	c.Writer = writer
	started := time.Now()

	opening, content, completion := keepaliveStallCreated,
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":13,\"output_tokens\":21}}}\n\n"
	if path != "openai_compat_chat" {
		opening = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_gone\",\"usage\":{\"input_tokens\":13}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
		content = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
		completion = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":21}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}

	var upstreamCtx context.Context
	var release func()
	compatSvc := compatStreamService(nil, 2, 30, 1)
	if path == "openai_compat_chat" {
		ctx, guard := compatSvc.armOpenAICompatFirstEvent(c.Request.Context(), context.Background(), rawChatCompletionsTestAccount(), true, "model", "", started)
		upstreamCtx, release = ctx, guard.close
	} else {
		ctx, guard := newFirstEventGuard(context.Background(), 2*time.Second)
		upstreamCtx, release = ctx, guard.Release
	}
	defer release()

	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	go func() {
		completeAt := time.NewTimer(time.Until(started.Add(2200 * time.Millisecond)))
		defer completeAt.Stop()
		_, err := io.WriteString(pw, opening)
		for _, step := range []struct {
			wait  <-chan struct{}
			after <-chan time.Time
			frame string
		}{{wait: failedHeartbeat, frame: content}, {after: completeAt.C, frame: completion}} {
			if err != nil {
				break
			}
			select {
			case <-step.wait:
			case <-step.after:
			case <-upstreamCtx.Done():
				err = upstreamCtx.Err()
				continue
			}
			_, err = io.WriteString(pw, step.frame)
		}
		_ = pw.CloseWithError(err)
	}()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr, Request: c.Request.WithContext(upstreamCtx)}

	var out clientGoneOutcome
	var err error
	switch path {
	case "openai_compat_chat":
		var result *OpenAIForwardResult
		result, err = compatSvc.handleChatStreamingResponse(resp, c, rawChatCompletionsTestAccount(), "model", "model", "model", started, 0)
		if result != nil {
			out.clientDisconnect, out.inputTokens, out.outputTokens = result.ClientDisconnect, result.Usage.InputTokens, result.Usage.OutputTokens
		}
	default:
		svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1, StreamDataIntervalTimeout: 30}}}
		var result *ForwardResult
		if path == "gateway_chat" {
			result, err = svc.handleCCStreamingFromAnthropic(resp, c, gwTestAccount(), "model", "model", nil, started)
		} else {
			result, err = svc.handleResponsesStreamingResponse(resp, c, gwTestAccount(), "model", "model", nil, started, apicompat.ResponsesClientToolMapping{})
		}
		if result != nil {
			out.clientDisconnect, out.inputTokens, out.outputTokens = result.ClientDisconnect, result.Usage.InputTokens, result.Usage.OutputTokens
		}
	}
	require.NoError(t, c.Request.Context().Err(), "the request context stays alive")
	var failover *UpstreamFailoverError
	out.failover = errors.As(err, &failover)
	out.err = err != nil
	out.accountFailed = state.AccountFailed(rawChatCompletionsTestAccount().ID) || state.AccountFailed(gwTestAccount().ID)
	out.turnFailed = state.TurnFailed()
	_, out.opsReported = c.Get(OpsUpstreamErrorsKey)
	out.bodyEmpty = rec.Body.Len() == 0
	out.failedWrites = writer.failedWrites
	return out
}
