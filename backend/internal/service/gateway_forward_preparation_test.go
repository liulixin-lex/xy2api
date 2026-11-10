//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type slowPreparationIdentityCache struct{ *stubIdentityCache }

func (r *slowPreparationIdentityCache) GetFingerprint(ctx context.Context, id int64) (*Fingerprint, error) {
	time.Sleep(10100 * time.Millisecond)
	return r.stubIdentityCache.GetFingerprint(ctx, id)
}

func TestAnthropicPreparationDoesNotPenalizeAccount(t *testing.T) {
	withResilienceSettings(t, nil)
	c, recorder := compatStreamContext("/v1/messages", "{}")
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.220 (external, cli)")
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(c.Request.Context(), st)
	ctx = SetClaudeCodeClient(ctx, true)
	ctx = WithAnthropicAttemptWarm(ctx, &AccountSelectionResult{stickySessionHit: true}, false)
	c.Request = c.Request.WithContext(ctx)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
	InstallDeliveryTracking(c, time.Now())
	events := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-review\",\"usage\":{\"input_tokens\":7}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(events))}}
	// The identity-cache read in buildUpstreamRequest is deliberately slow. In 0.5.11
	// AttemptProfile must start immediately before Do, independently of the timer.
	svc := newForwardPartialUsageServiceForTest(upstream)
	svc.settingService = &SettingService{settingRepo: newStubSettingRepo()}
	svc.identityService = NewIdentityService(&slowPreparationIdentityCache{stubIdentityCache: &stubIdentityCache{fingerprint: &Fingerprint{UserAgent: "claude-cli/2.1.220 (external, cli)", UpdatedAt: time.Now().Unix()}}})
	account := newAnthropicOAuthAccountForPartialUsageTest()
	const model = "claude-sonnet-4-5-20250929"
	// Below the existing hold threshold (2/7 < 35%). A genuinely fast eighth
	// upstream response must not turn this into a slow-account hold.
	now := time.Now()
	anthropicTestSamples(svc, account, model, time.Second, 5, now)
	anthropicTestSamples(svc, account, model, 11*time.Second, 2, now)
	require.False(t, svc.anthropicLatencyHoldSnapshot(account, model, now).Held)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5-20250929","stream":true,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	result, err := svc.Forward(ctx, c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, recorder.Body.String(), "hello")
	require.False(t, st.SemanticOutputAt().IsZero())
	svc.ReportAnthropicAttemptSuccess(ctx, account, result)
	profile := st.AttemptProfile()
	measured := st.SemanticOutputAt().Sub(profile.AttemptStart)
	held := svc.anthropicLatencyHoldSnapshot(account, model, time.Now()).Held
	t.Logf("sample_ms=%d warm=%t stream=%t held=%t status=%d", measured.Milliseconds(), profile.Warm, profile.Stream, held, recorder.Code)
	require.False(t, held, "arrival/observe must preserve the existing latency-hold decision")
	guard := firstEventGuardFrom(upstream.lastReq.Context())
	require.NotNil(t, guard)
	require.Equal(t, profile.TimerOwnerStart, guard.plan.Start())
	require.GreaterOrEqual(t, profile.AttemptStart.Sub(guard.plan.Start()), 10100*time.Millisecond,
		"the guard keeps its pre-builder plan while the sample starts at send")
	require.Less(t, measured, time.Second)
	require.EqualValues(t, measured.Milliseconds(), *st.AttemptFirstOutputMs())
}
