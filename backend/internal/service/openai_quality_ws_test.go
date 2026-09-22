package service

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIQualityWSPassthroughUnsentTurn(t *testing.T) {
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 10
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 10
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 10
	s := newPassthroughLifecycleService(cfg, upstream)
	account := passthroughLifecycleAccount()
	c, _, _ := qualityTestContext(s, 501, 0, "gpt-6-astra", "")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	var completed atomic.Int64
	server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, s, account, func(*gin.Context) *OpenAIWSIngressHooks {
		return &OpenAIWSIngressHooks{
			InitialRequestModel: "gpt-6-astra",
			BeforeRequest: func(turn int, payload []byte, model string) error {
				if turn > 1 {
					return s.AdvanceOpenAIQualityTurn(ctx, account, payload, model)
				}
				return nil
			},
			AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
				if result != nil && err == nil {
					completed.Add(1)
				}
			},
		}
	})
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	first := []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"role":"user","content":"hello"}]}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, first))
	select {
	case <-upstream.writes:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	upstream.Send(`{"type":"response.created","response":{"id":"resp_bad","model":"gpt-5.6-luna"}}`)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_bad","model":"gpt-5.6-luna","output":[],"usage":{"input_tokens":10,"output_tokens":1}}}`)
	for i := 0; i < 2; i++ {
		_, _, err = client.Read(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"role":"user","content":"next complete request"}]}`)))
	select {
	case err := <-serverErr:
		var move *OpenAIQualityRerouteError
		require.ErrorAs(t, err, &move)
		require.Contains(t, string(move.Payload), "next complete request")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Equal(t, int64(1), completed.Load(), "completed response billed exactly once")
	select {
	case wire := <-upstream.writes:
		t.Fatalf("next turn must remain unsent: %s", wire)
	default:
	}
}
