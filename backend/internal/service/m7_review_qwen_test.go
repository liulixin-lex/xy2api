//go:build unit

package service

import "testing"

func TestM7ReviewQwenSmallBudget(t *testing.T) {
	body := []byte(`{"model":"qwen3.8-max","input":"hi","max_output_tokens":32,"stream":false}`)
	c, rec := compatStreamContext("/v1/responses", string(body))
	account := nativeAnthropicTestAccount()
	account.Platform = PlatformOpenCodeGo
	upstream := &httpUpstreamRecorder{resp: nativeAnthropicStreamResponse()}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	result, err := svc.forwardResponsesViaNativeAnthropic(c.Request.Context(), c, account, body, "")
	t.Logf("err=%v status=%d sends=%d result_nil=%t", err, rec.Code, len(upstream.requests), result == nil)
	if err != nil || rec.Code != 200 || len(upstream.requests) != 1 {
		t.Error("ordinary small-budget Qwen request no longer reaches upstream")
	}
}
