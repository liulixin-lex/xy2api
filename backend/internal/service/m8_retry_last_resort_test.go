//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestM8LastResortCannotSendBeforeRecovery(t *testing.T) {
	withResilienceSettings(t, nil)
	until := time.Now().Add(45 * time.Second)
	accounts := []Account{{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &until,
		Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{}}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	failure := newOpenAIUpstreamErrorResponse(503, `{"error":{"code":"server_is_overloaded","message":"Server is overloaded; original capacity failure"}}`)
	failure.Header.Set("Retry-After", "2")
	upstream := &httpUpstreamRecorder{responses: []*http.Response{failure,
		newOpenAIUpstreamErrorResponse(200, `{"id":"resp_recovered","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}}
	svc := &OpenAIGatewayService{
		accountRepo: openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:       &schedulerTestGatewayCache{}, cfg: cfg, httpUpstream: upstream,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	c.Request = c.Request.WithContext(ctx)
	groupID := int64(9)
	selectAccount := func() *AccountSelectionResult {
		t.Helper()
		selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.Equal(t, int64(71), st.LastResort())
		return selection
	}
	first := selectAccount()
	_, err := svc.Forward(ctx, c, first.Account, []byte(`{"model":"gpt-5.5","stream":false,"input":"hi"}`))
	if first.ReleaseFunc != nil {
		first.ReleaseFunc()
	}
	var original *UpstreamFailoverError
	require.ErrorAs(t, err, &original)
	require.NotNil(t, original.UpstreamRecoveryHint)
	st.SetLastUpstreamFailure(original)
	st.NoteAccountRetryNotBefore(71, *original.UpstreamRecoveryHint)
	second, _, selectionErr := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	if selectionErr == nil {
		require.Equal(t, int64(71), st.LastResort())
		_, err = svc.Forward(ctx, c, second.Account, []byte(`{"model":"gpt-5.5","stream":false,"input":"hi"}`))
		if second.ReleaseFunc != nil {
			second.ReleaseFunc()
		}
		require.ErrorIs(t, err, original)
	} else {
		// M9 may reject this candidate during selection; both gates must
		// preserve the same no-send boundary until the original hint is due.
		require.ErrorIs(t, selectionErr, ErrNoAvailableAccounts)
	}
	require.Len(t, upstream.requests, 1, "last resort must not bypass the send boundary")
	require.Empty(t, rec.Body.String())
	time.Sleep(time.Until(original.UpstreamRecoveryHint.NotBefore))
	third := selectAccount()
	sentAt := time.Now()
	_, err = svc.Forward(ctx, c, third.Account, []byte(`{"model":"gpt-5.5","stream":false,"input":"hi"}`))
	if third.ReleaseFunc != nil {
		third.ReleaseFunc()
	}
	require.NoError(t, err)
	require.False(t, sentAt.Before(original.UpstreamRecoveryHint.NotBefore))
	require.Len(t, upstream.requests, 2)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "resp_recovered")
}
