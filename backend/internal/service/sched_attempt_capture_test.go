//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type captureServiceRecorder struct {
	starts  []SchedAttemptStart
	samples []schedquality.AttemptSample
}
type captureServiceAttempt struct {
	r    *captureServiceRecorder
	in   SchedAttemptStart
	done bool
}

func (r *captureServiceRecorder) Begin(in SchedAttemptStart) SchedAttempt {
	r.starts = append(r.starts, in)
	return &captureServiceAttempt{r: r, in: in}
}
func (*captureServiceRecorder) SetObserver(func(schedquality.AttemptSample)) {}
func (*captureServiceRecorder) Flush(context.Context) error                  { return nil }
func (*captureServiceAttempt) MarkFirstOutput(time.Time)                     {}
func (a *captureServiceAttempt) finish(outcome schedquality.Outcome, detail string) {
	if a.done {
		panic("duplicate finish")
	}
	a.done = true
	row := schedquality.AttemptSample{AccountID: a.in.AccountID, Outcome: outcome, Detail: detail}
	if a.in.Test != nil {
		a.in.Test.Sink(row)
	} else {
		a.r.samples = append(a.r.samples, row)
	}
}
func (a *captureServiceAttempt) FinishOK(*SchedAttemptUsage) { a.finish(schedquality.OutcomeOK, "") }
func (a *captureServiceAttempt) FinishFault(detail string) {
	a.finish(schedquality.OutcomeAccountFault, detail)
}
func (a *captureServiceAttempt) FinishOther(detail string) {
	a.finish(schedquality.OutcomeOther, detail)
}
func (a *captureServiceAttempt) FinishTimeout(time.Duration) {
	a.finish(schedquality.OutcomeTimeout, "")
}

func TestSchedCaptureTestMarkerIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, withKey := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("key=%t/failure=%t", withKey, failure), func(t *testing.T) {
				st := resilience.NewRequestState()
				st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-5.1"})
				var sink []schedquality.AttemptSample
				st.SetSchedTest(&SchedTestAttempt{Sink: func(row schedquality.AttemptSample) { sink = append(sink, row) }, Warm: true, PrevPromptTokens: 42})
				ctx := resilience.WithRequestState(context.Background(), st)
				body := []byte(`{"model":"gpt-5.1","input":"hello","stream":false}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
				if withKey {
					groupID := int64(4201)
					c.Set("api_key", &APIKey{ID: 501, GroupID: &groupID})
					SetOpenAIHTTPResponseOwner(c, 601, 501)
				}
				response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: http.NoBody}
				payload := `{"id":"resp_capture_test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":10,"output_tokens":1}}`
				if failure {
					response.StatusCode = 502
					payload = `{"error":{"message":"Bad gateway"}}`
				}
				response.Body = io.NopCloser(strings.NewReader(payload))
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
				cfg := &config.Config{}
				cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
				cfg.JWT.Secret = "capture-test-secret"
				recorder := &captureServiceRecorder{}
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, schedAttemptRecorder: recorder}
				if withKey {
					require.NotNil(t, svc.newCacheDiagnostic(c, body, "control"))
				}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}}
				result, err := svc.Forward(ctx, c, account, body)
				if failure {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Nil(t, result.CacheDiagnostic)
				}
				require.Len(t, sink, 1)
				require.Empty(t, recorder.samples)
				require.Len(t, recorder.starts, 1)
				require.True(t, recorder.starts[0].Test.Warm)
				require.Equal(t, 42, recorder.starts[0].Test.PrevPromptTokens)
				require.Nil(t, st.CurrentAttempt())
				require.False(t, st.AccountFailed(1))
				diagnostic, _ := c.Get("openai_cache_diagnostic")
				require.Nil(t, diagnostic)
				groupID := int64(0)
				if withKey {
					groupID = 4201
				}
				owner, _ := svc.getOpenAIWSStateStore().GetResponseAccount(ctx, groupID, "resp_capture_test")
				require.Zero(t, owner)
				if failure {
					events, exists := c.Get(OpsUpstreamErrorsKey)
					require.True(t, exists)
					rows := events.([]*OpsUpstreamErrorEvent)
					require.NotEmpty(t, rows)
					for _, row := range rows {
						require.Equal(t, "sched_test", row.Source)
						require.True(t, row.SkipMonitoring)
					}
				}
			})
		}
	}
}

func TestSchedCaptureA11IQExcluded(t *testing.T) {
	challenge := iqcheck.NewChallenge()
	answer := fmt.Sprintf(`{"answer":%d}`, challenge.Answer())
	transport := &targetProbeTransport{status: 200, body: fmt.Sprintf(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, answer)}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
	recorder := &captureServiceRecorder{}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: transport, schedAttemptRecorder: recorder}
	probe := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport, openaiGatewayService: gateway}}
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-6-astra"})
	result := probe.probe(resilience.WithRequestState(context.Background(), st), account.ID, iqProbeRequest{Challenge: challenge, Profile: iqProbeProfile{Model: "gpt-6-astra", ReasoningEffort: "low"}})
	require.Equal(t, "smart", result.Status, result)
	require.Equal(t, 1, transport.calls)
	require.Empty(t, recorder.starts)
	require.Empty(t, recorder.samples)
}

func TestSchedCaptureUninstalledDoesNotAllocate(t *testing.T) {
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-5.1"})
	ctx := resilience.WithRequestState(context.Background(), st)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	body := []byte(`{"model":"gpt-5.1","input":"` + strings.Repeat("x", 700000) + `","reasoning":{"effort":"high"}}`)
	payload := []byte(`{"delta":"answer"}`)
	var capture *schedAttemptCapture
	allocs := testing.AllocsPerRun(10, func() {
		capture = svc.beginSchedAttempt(ctx, account, body, len(body), true)
		observeSchedPayload(c, payload, "response.output_text.delta")
	})
	require.Nil(t, capture)
	require.Nil(t, st.CurrentAttempt())
	require.Zero(t, allocs)
}

func TestSchedCapturePreparedPayloadWithoutReread(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprint(installed), func(t *testing.T) {
			st := resilience.NewRequestState()
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-5.4"})
			ctx := resilience.WithRequestState(context.Background(), st)
			ctx = WithGroupSystemPrompt(ctx, GroupSystemPromptConfig{Prompt: "additional group instruction"}, "gpt-5.4-high")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			body := []byte(`{"model":"gpt-5.4-high","input":"hi"}`)
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}}
			rec := &captureServiceRecorder{}
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up}
			if installed {
				svc.SetSchedAttemptRecorder(rec)
			}
			req, err := svc.buildUpstreamRequestOpenAIPassthrough(ctx, c, account, body, "test")
			require.NoError(t, err)
			req.GetBody = func() (io.ReadCloser, error) { panic("capture must not reread the request body") }
			var capture *schedAttemptCapture
			_, err = svc.sendSchedHTTP(ctx, account, req, "", body, false, &capture)
			require.NoError(t, err)
			capture.finish(nil, nil)
			require.Contains(t, string(up.lastBody), "additional group instruction")
			if installed {
				require.Len(t, rec.starts, 1)
				require.Equal(t, len(up.lastBody), rec.starts[0].BodyBytes)
				require.Greater(t, rec.starts[0].BodyBytes, len(body))
				require.Equal(t, "high", rec.starts[0].Effort)
				require.Equal(t, "gpt-5.4", rec.starts[0].Model)
			} else {
				require.Nil(t, capture)
				require.Empty(t, rec.starts)
			}
			require.Nil(t, st.CurrentAttempt())
		})
	}
}

func TestSchedCaptureWebSocketFrameKindsAndSupersededTurn(t *testing.T) {
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-5.1"})
	ctx := resilience.WithRequestState(context.Background(), st)
	rec := &captureServiceRecorder{}
	svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	turns := &schedWSTurnCapture{}
	inner := &fakePassthroughFrameConn{}
	deadlineCalls := 0
	conn := &openAIWSPassthroughFirstOutputFrameConn{
		inner:      inner,
		beforeSend: func(payload []byte) { turns.begin(svc, ctx, account, payload) },
		resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
			deadlineCalls++
			return openAIWSPassthroughFirstOutputDeadline{startedAt: time.Now(), timeout: time.Second}
		},
	}
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)
	require.NoError(t, conn.WriteFrame(ctx, coderws.MessageBinary, payload))
	require.Len(t, rec.starts, 1)
	require.Zero(t, deadlineCalls)
	require.False(t, conn.deadlineState().armed)
	first := turns.active
	require.NoError(t, conn.WriteFrame(ctx, coderws.MessageText, payload))
	require.Len(t, inner.writes, 2)
	require.Len(t, rec.starts, 2)
	require.Equal(t, 1, deadlineCalls)
	require.True(t, conn.deadlineState().armed)
	require.Len(t, rec.samples, 1)
	require.Equal(t, schedquality.OutcomeOther, rec.samples[0].Outcome)
	require.Equal(t, "unfinished", rec.samples[0].Detail)
	require.Same(t, turns.active, st.CurrentAttempt())
	first.finish(nil, nil)
	require.Len(t, rec.samples, 1)
	turns.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`))
	turns.finish(&OpenAIForwardResult{UpstreamTerminalEvent: "response.completed"}, nil)
	require.Len(t, rec.samples, 2)
	require.Equal(t, schedquality.OutcomeOK, rec.samples[1].Outcome)
	require.Nil(t, st.CurrentAttempt())
}

func TestSchedCaptureCCBuildFailureDoesNotOwnPriorAttempt(t *testing.T) {
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-5.1"})
	ctx := resilience.WithRequestState(context.Background(), st)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	rec := &captureServiceRecorder{}
	svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	body := []byte(`{"model":"gpt-5.1","messages":[]}`)
	prior := svc.beginSchedAttempt(ctx, account, body, len(body), false)
	owned := prior
	_, err := svc.sendCCUpstreamRequest(ctx, c, account, "://invalid", body, false, "test", "", "", &owned)
	require.Error(t, err)
	require.Nil(t, owned)
	owned.finish(nil, err)
	require.Same(t, prior, st.CurrentAttempt())
	require.Len(t, rec.starts, 1)
	require.Empty(t, rec.samples)
	prior.finishUnfinished()
	require.Len(t, rec.samples, 1)
	require.Equal(t, "unfinished", rec.samples[0].Detail)
}
