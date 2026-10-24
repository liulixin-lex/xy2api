//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cacheProviderMessagesGolden struct {
	Wire    string
	Usage   OpenAIUsage
	Billing any
}

// This fixture also compiles on unmodified main16a. Record there first;
// candidate output must never replace the baseline.
func cacheProviderMessagesForward(t *testing.T, stream bool, configure func(*OpenAIGatewayService)) (*cacheTraceContractFixture, *OpenAIForwardResult, string) {
	t.Helper()
	f := cacheTraceNewContractFixture(t, "messages-bridge", "responses")
	f.s.cachePolicies = NewCachePolicyService(nil, f.s.cfg)
	f.body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"ctc-private-bridge-input"}],"stream":%t}`, stream))
	f.c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(f.body))
	f.c.Request.Header.Set("Content-Type", "application/json")
	f.c.Request.Header.Set("Session_id", "ctc-private-bridge-session")
	f.c.Request.Header.Set("X-Client-Request-Id", "00000000-0000-4000-8000-000000000001")
	f.c.Request.Header.Set("User-Agent", "claude-code/synthetic")
	if configure != nil {
		configure(f.s)
	}
	response := traceForwardOK()
	response.Header.Set("X-Request-Id", "ctc-private-bridge-request")
	recorder := &httpUpstreamRecorder{resp: response}
	f.s.httpUpstream = recorder
	f.s.GenerateSessionHash(f.c, f.body)
	result, err := f.s.ForwardAsAnthropic(f.c.Request.Context(), f.c, f.account, f.body, f.s.ExtractSessionID(f.c, f.body), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, recorder.lastReq)
	body, err := recorder.lastReq.GetBody()
	require.NoError(t, err)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	return f, result, cacheTraceWireSnapshot(t, recorder.lastReq, raw)
}

func cacheProviderMessagesBilling(t *testing.T, f *cacheTraceContractFixture, result *OpenAIForwardResult) any {
	t.Helper()
	// Only wall-clock billing metadata is normalized, never token counts.
	result.Duration = time.Second
	result.FirstTokenMs = nil
	err := f.s.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: result, APIKey: f.api, User: f.user, Account: f.account,
		PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	return cacheTraceContractBillingSnapshot(t, f.rows, f.billing, err)
}

func TestCacheProviderMessagesWireBaseline(t *testing.T) {
	snapshots := map[string]cacheProviderMessagesGolden{}
	for _, stream := range []bool{false, true} {
		f, result, wire := cacheProviderMessagesForward(t, stream, nil)
		require.Nil(t, result.CacheTrace)
		require.Nil(t, result.cachePanel)
		snapshots[fmt.Sprint(stream)] = cacheProviderMessagesGolden{Wire: wire, Usage: result.Usage, Billing: cacheProviderMessagesBilling(t, f, result)}
	}
	if out := os.Getenv("CACHE_PROVIDER_MESSAGES_RECORD"); out != "" {
		data, err := json.MarshalIndent(snapshots, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, data, 0600))
		return
	}
	raw, err := os.ReadFile(cacheProviderMessagesGoldenPath())
	require.NoError(t, err)
	want := map[string]cacheProviderMessagesGolden{}
	require.NoError(t, json.Unmarshal(raw, &want))
	require.Equal(t, want, snapshots)
}

func cacheProviderMessagesUsageSSE(usage string) string {
	return "data: " + `{"type":"response.completed","response":{"id":"resp_fixture","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic"}]}],"usage":` + strings.TrimSpace(usage) + "}}\n\n"
}

func cacheProviderMessagesGoldenPath() string {
	if path := os.Getenv("CACHE_PROVIDER_MESSAGES_GOLDEN"); path != "" {
		return path
	}
	return "testdata/cache_provider_messages_main16a.json"
}
