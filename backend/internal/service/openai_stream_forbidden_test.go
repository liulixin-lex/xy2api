//go:build unit

package service

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamForbiddenUsesSoftCooldownWithoutHTTP403Penalty(t *testing.T) {
	withResilienceSettings(t, nil)
	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"permission_error","code":"forbidden","message":"workspace forbidden","status":403}}}`)
	message := "workspace forbidden"
	require.True(t, openAIStreamFailedEventShouldFailover(payload, message))
	require.True(t, openAIStreamErrorEventShouldFailover(payload, message))
	require.False(t, openAIStream403AccountFailure(payload, message))
	require.False(t, openAIStream403TransientForbidden([]byte(`{"error":{"code":"invalid_api_key","status":403}}`), "invalid api key"))
	require.False(t, openAIStream403TransientForbidden([]byte(`{"error":{"code":"content_policy_violation","status":403}}`), "policy refusal"))

	req := newWebSearchReplayRequest(t)
	account := webSearchReplayAccount(false)
	repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
	rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(string(payload))}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.rateLimitService = rl
	_, err := svc.Forward(req.ctx, req.c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":"hello"}`))
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, OpenAIStreamForbiddenReason, failoverErr.Reason)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Len(t, upstream.bodies, 1)

	entry, ok := svc.openaiModelTransient.readEntry(account.ID, "gpt-5.5", time.Now())
	require.True(t, ok)
	require.Equal(t, 1, entry.failureStreak)
	require.Zero(t, repo.errorCalls, "stream 403 must not enter RateLimitService's permanent 403 counter")
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.modelRateLimitCalls)

	classified := ClassifyFailoverError(failoverErr)
	require.Equal(t, resilience.CategoryTransient, classified.Category)
	require.Equal(t, string(OpenAIStreamForbiddenReason), classified.Reason)
	require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
}
