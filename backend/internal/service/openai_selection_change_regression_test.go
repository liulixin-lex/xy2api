//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestStickyRecoveredForwardCommitsSuccessfulTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := selectionNewSessionFixture()
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	selected.Account.Credentials = map[string]any{"api_key": "synthetic-test", "base_url": "https://synthetic.invalid"}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"synthetic"}],"stream":false}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"not found"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"synthetic","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":1}}`))},
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	result, err := svc.ForwardAsChatCompletions(ctx, c, selected.Account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 2)
	require.Contains(t, rec.Body.String(), `"content":"ok"`)
	FinishOpenAIStickyForward(ctx, selected.Account.ID, result, err)
	avoid, readErr := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, readErr)
	require.Equal(t, selected.Account.ID, f.binding.owner, "the target delivered this request successfully after protocol fallback")
	require.Empty(t, avoid)
}

func TestStickyLateFailureCannotAvoidNewlyCommittedOwner(t *testing.T) {
	f := selectionNewSessionFixture()
	one := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	two := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	first := f.session.selectOwner(one, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	second := f.session.selectOwner(two, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, first)
	require.NotNil(t, second)
	defer first.ReleaseFunc()
	defer second.ReleaseFunc()
	require.Equal(t, first.Account.ID, second.Account.ID)
	CompleteOpenAIStickyChange(two, second.Account.ID, true, false)
	require.Equal(t, second.Account.ID, f.binding.owner)
	CompleteOpenAIStickyChange(one, first.Account.ID, false, false)
	avoid, err := f.session.store.SessionAvoid(two, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, err)
	require.NotContains(t, avoid, second.Account.ID, "late failure belongs to a superseded binding generation")
}

func TestStickyLegacyFallbackDoesNotEagerlyBindReplacement(t *testing.T) {
	owner := selectionFixtureAccount(10, 5)
	owner.Schedulable = false
	f := newSelectionRuntimeFixture(t, owner, selectionFixtureAccount(1, 1))
	f.cache.sessionBindings["openai:legacy"] = 10
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	ctx = withOpenAILegacySessionHash(ctx, "legacy")
	group := int64(7)
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	require.EqualValues(t, 1, selected.Account.ID)
	bound, err := f.svc.getStickySessionAccountID(ctx, &group, "session")
	require.NoError(t, err)
	require.EqualValues(t, 10, bound, "legacy owner must survive until replacement delivers")
}

func TestStickySupersededSendCannotOverwriteFinalAttemptEvidence(t *testing.T) {
	f := selectionNewSessionFixture()
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	svc := &OpenAIGatewayService{}
	earlier := svc.beginSchedAttempt(ctx, selected.Account, nil, 0, false)
	final := svc.beginSchedAttempt(ctx, selected.Account, nil, 0, false)
	result := &OpenAIForwardResult{}
	final.finish(result, nil)
	earlier.finish(nil, io.ErrUnexpectedEOF)
	FinishOpenAIStickyForward(ctx, selected.Account.ID, result, nil)
	require.Equal(t, selected.Account.ID, f.binding.owner)
	avoided, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, err)
	require.Empty(t, avoided)
}
