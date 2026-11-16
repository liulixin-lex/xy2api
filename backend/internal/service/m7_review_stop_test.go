//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"testing"
)

func TestM7ReviewNativeAnthropicStop(t *testing.T) {
	for _, stop := range []string{`["END"]`, `["\n"]`} {
		t.Run(stop, func(t *testing.T) {
			body := []byte(`{"model":"k3","messages":[{"role":"user","content":"hi"}],"stop":` + stop + `}`)
			c, rec := compatStreamContext("/v1/chat/completions", string(body))
			upstream := &httpUpstreamRecorder{resp: nativeAnthropicStreamResponse()}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			result, err := svc.forwardChatCompletionsViaNativeAnthropic(c.Request.Context(), c, nativeAnthropicTestAccount(), body, "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 200, rec.Code)
			require.Len(t, upstream.requests, 1)
			if stop == `["END"]` {
				require.Equal(t, "END", gjson.GetBytes(upstream.lastBody, "stop_sequences.0").String())
			}
		})
	}
}
