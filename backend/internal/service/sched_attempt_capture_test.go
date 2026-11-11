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
func (a *captureServiceAttempt) finish(outcome schedquality.Outcome) {
	if a.done {
		panic("duplicate finish")
	}
	a.done = true
	row := schedquality.AttemptSample{AccountID: a.in.AccountID, Outcome: outcome}
	if a.in.Test != nil {
		a.in.Test.Sink(row)
	} else {
		a.r.samples = append(a.r.samples, row)
	}
}
func (a *captureServiceAttempt) FinishOK(*SchedAttemptUsage) { a.finish(schedquality.OutcomeOK) }
func (a *captureServiceAttempt) FinishFault(string)          { a.finish(schedquality.OutcomeAccountFault) }
func (a *captureServiceAttempt) FinishOther(string)          { a.finish(schedquality.OutcomeOther) }
func (a *captureServiceAttempt) FinishTimeout(time.Duration) { a.finish(schedquality.OutcomeTimeout) }

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
