//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCapabilityProvisionalRefusalWaitsForAttemptOutcome(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovered", false: "failed"}[success], func(t *testing.T) {
			c, _ := compatStreamContext("/v1/responses", "")
			state := resilience.NewRequestState()
			state.SetSchedSelection(resilience.SchedSelection{AccountID: 11, Model: "fixture"})
			ctx := resilience.WithRequestState(c.Request.Context(), state)
			c.Request = c.Request.WithContext(ctx)
			account := capabilityTestAccount(11)
			account.Type = AccountTypeOAuth
			repo := &capabilityRepo{accounts: map[int64]*Account{11: account}}
			recorder := &errclassRecorder{}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), accountRepo: repo, schedAttemptRecorder: recorder}
			capture := svc.beginSchedAttempt(ctx, account, nil, 0, true)
			capture.expectTerminal = true
			capture.learnCapability = svc.capabilityFailureLearner(ctx, account, []byte(`{"model":"fixture","stream":true}`), "responses")
			errorFrame := `{"type":"error","error":{"type":"invalid_request_error","code":"unsupported_protocol","message":"unsupported transport"}}`
			capture.observe([]byte(errorFrame))
			require.Zero(t, repo.writes, "a provisional frame must never write a mark")
			if success {
				response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(captureResponsesWire()))}
				_, err := svc.handleStreamingResponse(ctx, response, c, account, time.Now(), "fixture", "fixture")
				require.NoError(t, err)
				capture.finish(&OpenAIForwardResult{}, nil)
			} else {
				capture.finish(nil, errors.New("synthetic stream ended without recovery"))
			}
			require.Len(t, recorder.finishes, 1)
			if success {
				require.Zero(t, repo.writes)
				require.Equal(t, schedquality.OutcomeOK, recorder.finishes[0].Outcome)
			} else {
				require.Equal(t, 1, repo.writes)
				require.Equal(t, schedquality.ClassCapability, recorder.finishes[0].Class)
			}
			require.Equal(t, !success, account.CapabilityMarked("fixture", "responses", "stream", time.Now()) != nil)
		})
	}
}

func TestCapabilityProvisionalWebSocketRefusalWaitsForOutcome(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovered", false: "failed"}[success], func(t *testing.T) {
			account := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			pending := &capabilityStreamFailure{learn: svc.capabilityWSFailureLearner(context.Background(), account, []byte(`{"model":"fixture"}`))}
			pending.observe([]byte(`{"type":"error","error":{"code":"unsupported_protocol"}}`))
			require.Zero(t, repo.writes)
			if success {
				pending.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`))
				pending.finish(capabilityStreamFinish{})
			} else {
				pending.finish(capabilityStreamFinish{UpstreamFailure: true})
			}
			require.Equal(t, !success, account.CapabilityMarked("fixture", "responses", "stream", time.Now()) != nil)
		})
	}
}

type capabilityUpdateCounter struct {
	*capabilityRepo
	calls int
}

func (r *capabilityUpdateCounter) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.calls++
	return r.capabilityRepo.UpdateExtra(ctx, id, updates)
}

func TestCapabilitySameKeyHourlyThrottleSkipsWriteTransaction(t *testing.T) {
	account := capabilityTestAccount(1)
	repo := &capabilityUpdateCounter{capabilityRepo: &capabilityRepo{accounts: map[int64]*Account{1: account}}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := context.Background()
	stale, err := repo.GetByID(ctx, 1)
	require.NoError(t, err)
	mark := syntheticCapabilityMark()
	require.NoError(t, svc.LearnCapabilityMark(ctx, stale, mark))
	first := account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now())
	require.NotNil(t, first)
	require.Equal(t, 1, repo.calls)
	require.NoError(t, svc.LearnCapabilityMark(ctx, stale, mark))
	require.NoError(t, svc.LearnCapabilityMark(ctx, account, mark))
	require.Equal(t, 1, repo.calls, "recent evidence must not call UpdateExtra or open its transaction, including with a stale caller snapshot")
	require.Equal(t, first.ExpiresAt, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()).ExpiresAt)
	aged := *first
	aged.ExpiresAt = time.Now().Add(CapabilityMarkTTL - 2*time.Hour)
	account.Extra[capabilityMarkExtraKey(mark.Model, mark.Protocol, mark.Shape)] = aged
	require.NoError(t, svc.LearnCapabilityMark(ctx, stale, mark))
	require.Equal(t, 2, repo.calls)
	require.True(t, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()).ExpiresAt.After(aged.ExpiresAt))
	boundary := aged
	boundary.Fingerprint = capabilityFingerprint(account)
	now := time.Now()
	boundary.ExpiresAt = now.Add(CapabilityMarkTTL - CapabilityMarkWriteInterval)
	incoming := mark
	incoming.Fingerprint = boundary.Fingerprint
	require.False(t, capabilityMarkRecentlyWritten(boundary, incoming, now), "exactly one hour permits a new observation")
}

func TestCapabilityFullMarksEvictEarliestExpiry(t *testing.T) {
	account := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	for i := 0; i < CapabilityMarkLimit; i++ {
		mark := syntheticCapabilityMark()
		mark.Model = string(rune('a'+i)) + "-fixture"
		mark.Fingerprint = capabilityFingerprint(account)
		mark.ExpiresAt = time.Now().Add(time.Duration(i+1) * time.Minute)
		account.Extra[capabilityMarkExtraKey(mark.Model, mark.Protocol, mark.Shape)] = mark
	}
	incoming := syntheticCapabilityMark()
	incoming.Model = "actual-model-fixture"
	require.NoError(t, svc.LearnCapabilityMark(context.Background(), account, incoming))
	require.NotNil(t, account.CapabilityMarked(incoming.Model, incoming.Protocol, incoming.Shape, time.Now()))
	require.Nil(t, account.CapabilityMarked("a-fixture", incoming.Protocol, incoming.Shape, time.Now()))
	require.NotNil(t, account.CapabilityMarked("b-fixture", incoming.Protocol, incoming.Shape, time.Now()))
	count := 0
	for key := range account.Extra {
		if strings.HasPrefix(key, CapabilityMarksExtraKey+"_") {
			count++
		}
	}
	require.Equal(t, CapabilityMarkLimit, count)
}

func TestCapabilityFeatureScopesAndHTTP401AccountQualification(t *testing.T) {
	for _, message := range []string{"This upstream supports text and https input_image content only", "Desktop frontend requests native-client are not supported on this service"} {
		account := capabilityTestAccount(1)
		repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
		svc := &OpenAIGatewayService{accountRepo: repo}
		body := []byte(`{"error":{"message":"` + message + `"}}`)
		svc.capabilityFailureLearner(context.Background(), account, []byte(`{"model":"fixture","stream":true}`), "responses")(400, body)
		require.Equal(t, UpstreamCapabilityRequestFeature, ClassifyUpstreamCapabilityFailure(account, 400, body))
		classified := classifySchedAttempt(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault}, &resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: 400, Body: body}, false, false)
		require.Equal(t, schedquality.ClassCapability, classified.Class)
		require.Zero(t, repo.writes, message)
	}
	for _, kind := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		t.Run(kind, func(t *testing.T) {
			account := capabilityTestAccount(1)
			account.Type = kind
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			svc.capabilityFailureLearner(context.Background(), account, []byte(`{"model":"fixture","stream":false}`), "responses")(401, []byte(`{"error":{"code":"model_not_found"}}`))
			require.Equal(t, kind == AccountTypeAPIKey, account.CapabilityMarked("fixture", "responses", "non_stream", time.Now()) != nil)
		})
	}
}
