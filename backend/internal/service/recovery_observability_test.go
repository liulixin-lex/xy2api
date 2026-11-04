//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestEndToEndFirstOutputIncludesQueueAndFailedAttempt(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	// Three seconds queued, a 20-second failed attempt, then five seconds TTFT.
	start := time.Now().Add(-28 * time.Second)
	st.InitDelivery(c.Request.Context(), start, deliveryWriterFrom(c).deliveredBytes)
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 1, AttemptStart: start.Add(3 * time.Second)})
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 2, AttemptStart: start.Add(23 * time.Second)})
	_, err := c.Writer.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	require.NoError(t, err)
	require.InDelta(t, 28000, *st.EndToEndFirstOutputMs(), 100)
	first := *st.EndToEndFirstOutputMs()
	require.Equal(t, first-23000, *st.AttemptFirstOutputMs())
	_, err = c.Writer.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"again\"}\n\n")
	require.NoError(t, err)
	require.Equal(t, first, *st.EndToEndFirstOutputMs())
	require.Equal(t, first-23000, *st.AttemptFirstOutputMs())
	ttft := 5000
	result := &ForwardResult{Model: "gpt-5", FirstTokenMs: &ttft}
	log := (&GatewayService{}).buildRecordUsageLog(c.Request.Context(), &recordUsageCoreInput{}, result,
		&APIKey{}, &User{}, &Account{}, nil, "gpt-5", 1, 1, 1, 0, false, nil)
	require.Equal(t, 5000, *log.FirstTokenMs)
	require.Equal(t, first, *log.EndToEndFirstOutputMs)
	require.Equal(t, first-23000, *log.AttemptFirstOutputMs)
}

func TestRecordUsageAttemptFirstOutput(t *testing.T) {
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		state    bool
		attempt  bool
		semantic bool
		failover bool
		want     *int64
	}{
		{name: "no request state"},
		{name: "no semantic output", state: true, attempt: true},
		{name: "no attempt start", state: true, semantic: true},
		{name: "semantic output", state: true, attempt: true, semantic: true, want: new(int64(25000))},
		{name: "final attempt after failover", state: true, attempt: true, semantic: true, failover: true, want: new(int64(5000))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var endToEnd *int64
			if tc.state {
				st := resilience.NewRequestState()
				st.InitDelivery(ctx, start, nil)
				ctx = resilience.WithRequestState(ctx, st)
				if tc.attempt {
					st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 1, AttemptStart: start.Add(3 * time.Second)})
				}
				if tc.failover {
					st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 2, AttemptStart: start.Add(23 * time.Second)})
				}
				if tc.semantic {
					st.NoteSemanticOutput(start.Add(28 * time.Second))
					endToEnd = new(int64(28000))
				}
			}
			firstToken := 42
			generic := (&GatewayService{}).buildRecordUsageLog(ctx, &recordUsageCoreInput{},
				&ForwardResult{Model: "gpt-5.1", FirstTokenMs: &firstToken}, &APIKey{}, &User{}, &Account{},
				nil, "gpt-5.1", 1, 1, 1, 0, false, nil)
			require.Equal(t, tc.want, generic.AttemptFirstOutputMs)
			require.Equal(t, endToEnd, generic.EndToEndFirstOutputMs)
			require.Equal(t, firstToken, *generic.FirstTokenMs)

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo,
				&openAIRecordUsageBillingRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result:  &OpenAIForwardResult{RequestID: "attempt-first-output", Model: "gpt-5.1", Stream: true, FirstTokenMs: &firstToken},
				APIKey:  &APIKey{ID: 1, Group: &Group{RateMultiplier: 1}},
				User:    &User{ID: 2},
				Account: &Account{ID: 3, Type: AccountTypeAPIKey},
			})
			require.NoError(t, err)
			require.Equal(t, 1, usageRepo.calls)
			require.Equal(t, tc.want, usageRepo.lastLog.AttemptFirstOutputMs)
			require.Equal(t, endToEnd, usageRepo.lastLog.EndToEndFirstOutputMs)
			require.Equal(t, firstToken, *usageRepo.lastLog.FirstTokenMs)
		})
	}
}

func TestEndToEndFirstOutputExcludesProtocolOnlyDelivery(t *testing.T) {
	for name, payload := range map[string]string{
		"heartbeat":       ": ping\n\n",
		"failed":          "data: {\"type\":\"response.failed\",\"response\":{\"output\":[]}}\n\n",
		"done":            "data: [DONE]\n\n",
		"empty success":   "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n",
		"chat finish":     "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"messages finish": "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"opening":         "data: {\"type\":\"response.created\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, st := newSSEDeliveryContext(t, "text/event-stream")
			st.SetAttemptProfile(resilience.AttemptProfile{AttemptStart: time.Now().Add(-time.Second)})
			_, err := c.Writer.WriteString(payload)
			require.NoError(t, err)
			require.Nil(t, st.EndToEndFirstOutputMs())
			require.Nil(t, st.AttemptFirstOutputMs())
			if name == "failed" || name == "done" {
				require.Positive(t, st.DeliveredBytes(), "billing criterion is unchanged")
			}
		})
	}
	for _, payload := range []string{`{"data":[]}`, `{"output":[]}`, `{"candidates":[{"finishReason":"STOP"}]}`,
		`{"choices":[{"message":{"role":"assistant","content":[]},"finish_reason":"stop"}]}`,
		`{"choices":[{"message":{"content":{}}}]}`,
		`{"error":{"message":"failed"},"choices":[{"message":{"content":"hello"}}]}`} {
		c, st := newSSEDeliveryContext(t, "application/json")
		st.SetAttemptProfile(resilience.AttemptProfile{AttemptStart: time.Now().Add(-time.Second)})
		_, err := c.Writer.WriteString(payload)
		require.NoError(t, err)
		require.Nil(t, st.EndToEndFirstOutputMs())
		require.Nil(t, st.AttemptFirstOutputMs())
	}
}

func TestEndToEndFirstOutputAcceptsPlaceholderErrors(t *testing.T) {
	for _, tc := range []struct{ contentType, payload string }{
		{"application/json", `{"status":"completed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`},
		{"text/event-stream", "data: {\"error\":null,\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"},
	} {
		c, st := newSSEDeliveryContext(t, tc.contentType)
		st.SetAttemptProfile(resilience.AttemptProfile{AttemptStart: time.Now().Add(-time.Second)})
		_, err := c.Writer.WriteString(tc.payload)
		require.NoError(t, err)
		require.NotNil(t, st.EndToEndFirstOutputMs())
		require.NotNil(t, st.AttemptFirstOutputMs())
	}
}

func TestEndToEndFirstOutputAcrossProtocols(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`,
		`{"type":"content_block_delta","delta":{"text":"hi"}}`,
		`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}`,
	} {
		c, st := newSSEDeliveryContext(t, "text/event-stream")
		st.SetAttemptProfile(resilience.AttemptProfile{AttemptStart: time.Now().Add(-time.Second)})
		_, err := c.Writer.WriteString("data: " + payload + "\n\n")
		require.NoError(t, err)
		require.NotNil(t, st.EndToEndFirstOutputMs(), payload)
		require.NotNil(t, st.AttemptFirstOutputMs(), payload)
	}
}

func TestFirstOutputTimeoutPersistsOnlyFailedAttemptProfile(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	start := time.Now().Add(-20 * time.Second)
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 1, Model: "gpt-5.5", Warm: false, BodyBytes: 256 << 10, AttemptStart: start})
	svc := &OpenAIGatewayService{}
	svc.newOpenAIFirstOutputTimeoutError(c.Request.Context(), c, &Account{ID: 1, Platform: PlatformOpenAI}, nil, "", start, "gpt-5.5", "high", 20*time.Second, "semantic_output", nil)
	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events := raw.([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Warm)
	require.False(t, *events[0].Warm)
	require.Equal(t, 256<<10, events[0].BodyBytes)
	require.Equal(t, "high", events[0].Effort)
	require.InDelta(t, 20000, events[0].AttemptElapsedMs, 100)
	require.Equal(t, int64(20000), events[0].TimeoutMs)
	entry := &OpsInsertErrorLogInput{UpstreamErrors: events}
	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	stored, err := ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.NotNil(t, stored[0].Warm)
	require.False(t, *stored[0].Warm)
	require.Equal(t, events[0].BodyBytes, stored[0].BodyBytes)
}
