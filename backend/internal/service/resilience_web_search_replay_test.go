//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Production (R3): an API-key relay in front of the ChatGPT Codex backend
// failed one Codex Desktop session 65 times in a row in 70 minutes with the
// in-stream error "response protection is unavailable"; the OAuth-only
// web_search declaration fix (#7927) did not cover relays.

const webSearchRejectionEvent = "event: error\ndata: {\"error\":{\"message\":\"response protection is unavailable\",\"type\":\"internal_error\"}}\n\n"

func webSearchRejectionStream() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_rejected"}},
		Body:       io.NopCloser(strings.NewReader(webSearchRejectionEvent)),
	}
}

func webSearchRejectionHTTP() *http.Response {
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Response protection is unavailable","type":"internal_error"}}`)),
	}
}

func webSearchSuccessStream(id string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"" + id + "\",\"object\":\"response\",\"status\":\"in_progress\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + id + "\",\"object\":\"response\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n",
		)),
	}
}

type webSearchSessionRecorder struct {
	failures []resilience.Category
}

func (r *webSearchSessionRecorder) Failure(_ context.Context, _ int64, _ string, _ int64, c resilience.Category) {
	r.failures = append(r.failures, c)
}

func (r *webSearchSessionRecorder) Success(context.Context, int64, string, int64) {}

// webSearchReplayRequest is one client request with a request budget and a
// sticky session.
type webSearchReplayRequest struct {
	ctx     context.Context
	c       *gin.Context
	rec     *httptest.ResponseRecorder
	budget  *resilience.Budget
	session *webSearchSessionRecorder
}

func newWebSearchReplayRequest(t *testing.T) *webSearchReplayRequest {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	st := resilience.NewRequestState()
	budget := resilience.NewBudget(context.Background(), resilience.Current(), resilience.KindText, time.Now())
	st.SetBudget(budget)
	session := &webSearchSessionRecorder{}
	st.SetSession(18, "sess", session)
	ctx := resilience.WithRequestState(context.Background(), st)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.158.0")
	return &webSearchReplayRequest{ctx: ctx, c: c, rec: rec, budget: budget, session: session}
}

func webSearchReplayAccount(passthrough bool) *Account {
	return &Account{
		ID:          378,
		Name:        "relay",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 6,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://relay.example", "pool_mode": true},
		Extra:       map[string]any{"openai_passthrough": passthrough},
	}
}

// resetWebSearchDeclarations gives the test an empty process memory.
func resetWebSearchDeclarations(t *testing.T) {
	t.Helper()
	orig := openAIWebSearchDeclared
	openAIWebSearchDeclared = &openAIWebSearchDeclarations{}
	t.Cleanup(func() { openAIWebSearchDeclared = orig })
}

func requireWebSearchDeclared(t *testing.T, body []byte) {
	t.Helper()
	require.Equal(t, "web_search_call", gjson.GetBytes(body, "input.1.type").String())
	tools := gjson.GetBytes(body, "tools").Array()
	require.Len(t, tools, 1)
	require.Equal(t, "web_search", tools[0].Get("type").String())
	require.True(t, tools[0].Get("external_web_access").Exists())
	require.False(t, tools[0].Get("external_web_access").Bool())
	require.Equal(t, "none", gjson.GetBytes(body, "tool_choice").String())
}

func requireWebSearchNotDeclared(t *testing.T, body []byte) {
	t.Helper()
	require.False(t, gjsonToolsContainWebSearch(gjson.GetBytes(body, "tools")), "body: %s", body)
}

func TestOpenAIWebSearchHistoryRejectionWording(t *testing.T) {
	require.True(t, isOpenAIWebSearchHistoryRejection("response protection is unavailable", nil))
	require.True(t, isOpenAIWebSearchHistoryRejection("", []byte(`{"error":{"message":"Response Protection is unavailable","type":"internal_error"}}`)))
	require.True(t, isOpenAIWebSearchHistoryRejection("", []byte(`{"type":"response.failed","response":{"error":{"message":"response protection is unavailable"}}}`)))
	require.False(t, isOpenAIWebSearchHistoryRejection("Upstream request failed", []byte(`{"error":{"message":"internal error"}}`)))
	require.False(t, isOpenAIWebSearchHistoryRejection("", nil))
}

func TestForwardWebSearchHistoryRejectionReplaysAndRemembersAccount(t *testing.T) {
	for _, tc := range []struct {
		name        string
		passthrough bool
		rejection   func() *http.Response
	}{
		{"stream event", false, webSearchRejectionStream},
		{"http error", false, webSearchRejectionHTTP},
		{"passthrough stream event", true, webSearchRejectionStream},
		{"passthrough http error", true, webSearchRejectionHTTP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			resetWebSearchDeclarations(t)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				tc.rejection(), webSearchSuccessStream("resp_replayed"), webSearchSuccessStream("resp_next"),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			account := webSearchReplayAccount(tc.passthrough)

			// First rejected, replayed once with the OAuth fix's declaration.
			req := newWebSearchReplayRequest(t)
			result, err := svc.Forward(req.ctx, req.c, account, []byte(openAIWebSearchHistoryCompactionBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.bodies, 2)
			requireWebSearchNotDeclared(t, upstream.bodies[0])
			requireWebSearchDeclared(t, upstream.bodies[1])
			require.Contains(t, req.rec.Body.String(), "hello")
			require.NotContains(t, req.rec.Body.String(), "response protection")
			require.Equal(t, 1, req.budget.Attempts(), "the replay draws from the request budget")
			require.Empty(t, req.session.failures)

			// Learned: the next request of the kind is declared up front.
			req = newWebSearchReplayRequest(t)
			_, err = svc.Forward(req.ctx, req.c, account, []byte(openAIWebSearchHistoryCompactionBody))
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 3)
			requireWebSearchDeclared(t, upstream.bodies[2])
			require.Zero(t, req.budget.Attempts())
		})
	}
}

func TestForwardWebSearchHistoryReplayFailsOnlyOnce(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		withResilienceSettings(t, nil)
		resetWebSearchDeclarations(t)
		upstream := &httpUpstreamRecorder{responses: []*http.Response{
			webSearchRejectionStream(), webSearchRejectionStream(), webSearchSuccessStream("resp_unused"),
		}}
		svc := newOpenAIImageGenerationControlTestService(upstream)
		account := webSearchReplayAccount(passthrough)
		req := newWebSearchReplayRequest(t)

		_, err := svc.Forward(req.ctx, req.c, account, []byte(openAIWebSearchHistoryCompactionBody))
		var failoverErr *UpstreamFailoverError
		require.True(t, errors.As(err, &failoverErr), "passthrough=%v err=%v", passthrough, err)
		require.Len(t, upstream.bodies, 2, "one replay, no loop")
		require.Equal(t, OpenAIWebSearchHistoryRejectedReason, failoverErr.Reason)
		require.False(t, failoverErr.ShouldRetryNextAccount(), "a relay of the same kind fails the same way")
		require.False(t, failoverErr.RetryableOnSameAccount)
		require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
		require.Contains(t, failoverErr.ClientMessage, "response protection is unavailable")
		require.NotContains(t, failoverErr.ClientMessage, "administrator")
		require.Empty(t, req.rec.Body.String(), "nothing reached the client; the handler answers")
		require.Equal(t, []resilience.Category{resilience.CategoryTransient}, req.session.failures, "counted once for the session")
		require.False(t, openAIWebSearchDeclared.needed(account.ID, time.Now()), "a failed replay is not remembered")

		// A remembered account declared up front is not replayed again.
		openAIWebSearchDeclared.remember(account.ID, time.Now())
		upstream.responses = []*http.Response{webSearchRejectionStream(), webSearchSuccessStream("resp_unused")}
		req = newWebSearchReplayRequest(t)
		_, err = svc.Forward(req.ctx, req.c, account, []byte(openAIWebSearchHistoryCompactionBody))
		require.True(t, errors.As(err, &failoverErr))
		require.Equal(t, OpenAIWebSearchHistoryRejectedReason, failoverErr.Reason)
		require.Len(t, upstream.bodies, 3)
		requireWebSearchDeclared(t, upstream.bodies[2])
	}
}

func TestForwardWebSearchHistoryRejectionWithoutBudgetIsNotReplayed(t *testing.T) {
	withResilienceSettings(t, nil)
	resetWebSearchDeclarations(t)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{webSearchRejectionStream(), webSearchSuccessStream("resp_unused")}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	req := newWebSearchReplayRequest(t)
	for i := 1; i < req.budget.MaxAttempts(); i++ {
		req.budget.RecordAttemptAt(time.Now())
	}

	_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(openAIWebSearchHistoryCompactionBody))
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, OpenAIWebSearchHistoryRejectedReason, failoverErr.Reason)
	require.Len(t, upstream.bodies, 1)
}

func TestForwardWebSearchReplayNeedsUndeclaredHistory(t *testing.T) {
	for name, body := range map[string]string{
		"no web_search_call": `{"model":"gpt-5.5","instructions":"x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[],"stream":true,"store":false}`,
		"already declared": strings.Replace(openAIWebSearchHistoryCompactionBody, `"tools":[]`,
			`"tools":[{"type":"web_search"}]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			resetWebSearchDeclarations(t)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{webSearchRejectionStream(), webSearchSuccessStream("resp_unused")}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			req := newWebSearchReplayRequest(t)

			_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(body))
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr) && failoverErr.Reason == OpenAIWebSearchHistoryRejectedReason)
			require.Len(t, upstream.bodies, 1, "not replayed")
			require.Contains(t, req.rec.Body.String(), "response protection is unavailable", "the 0.3.0 relay of the error")
		})
	}
}

func TestForwardWebSearchReplayLeavesOAuthToItsFix(t *testing.T) {
	withResilienceSettings(t, nil)
	resetWebSearchDeclarations(t)
	s := newAstraOAuthSetup(t, false)
	s.upstream.responses = []*http.Response{webSearchRejectionStream(), webSearchSuccessStream("resp_unused")}

	_, err := s.svc.Forward(context.Background(), s.c, s.account, []byte(openAIWebSearchHistoryCompactionBody))
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr) && failoverErr.Reason == OpenAIWebSearchHistoryRejectedReason)
	require.Len(t, s.upstream.bodies, 1, "OAuth declares up front and is not replayed")
	requireWebSearchDeclared(t, s.upstream.bodies[0])
	require.False(t, openAIWebSearchDeclared.needed(s.account.ID, time.Now()))
}

func TestForwardWebSearchReplayFollowsTheSwitch(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	resetWebSearchDeclarations(t)
	account := webSearchReplayAccount(false)
	openAIWebSearchDeclared.remember(account.ID, time.Now())
	upstream := &httpUpstreamRecorder{responses: []*http.Response{webSearchRejectionStream(), webSearchSuccessStream("resp_unused")}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	req := newWebSearchReplayRequest(t)

	_, err := svc.Forward(req.ctx, req.c, account, []byte(openAIWebSearchHistoryCompactionBody))
	require.Error(t, err)
	require.Len(t, upstream.bodies, 1, "switch off: no replay")
	requireWebSearchNotDeclared(t, upstream.bodies[0])
	require.Contains(t, req.rec.Body.String(), "response protection is unavailable")
	require.Empty(t, req.session.failures)
}

func TestOpenAIWebSearchDeclarationsExpireAndStayBounded(t *testing.T) {
	m := &openAIWebSearchDeclarations{}
	now := time.Now()
	m.remember(1, now)
	require.True(t, m.needed(1, now.Add(openAIWebSearchDeclarationTTL-time.Second)))
	require.False(t, m.needed(1, now.Add(openAIWebSearchDeclarationTTL)), "expired")
	require.Empty(t, m.until, "an expired entry is dropped when read")

	m.remember(2, now)
	later := now.Add(openAIWebSearchDeclarationTTL + time.Second)
	m.remember(3, later)
	require.NotContains(t, m.until, int64(2), "expired entries are swept on write")

	for i := 0; i < openAIWebSearchDeclarationMax+10; i++ {
		m.remember(int64(100+i), later.Add(time.Duration(i)*time.Millisecond))
	}
	require.Len(t, m.until, openAIWebSearchDeclarationMax)
	require.False(t, m.needed(3, later), "the entry closest to expiry made room")
	require.True(t, m.needed(int64(100+openAIWebSearchDeclarationMax+9), later))
}
