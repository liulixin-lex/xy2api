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
	"testing/synctest"
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
func (*captureServiceAttempt) MarkOutput(SchedOutputKind, time.Time)         {}
func (a *captureServiceAttempt) Finish(in SchedAttemptFinish) {
	if a.done {
		panic("duplicate finish")
	}
	a.done = true
	row := schedquality.AttemptSample{AccountID: a.in.AccountID, Outcome: in.Outcome, Detail: in.Detail, Warm: in.Warm, FullTierFirstOutputTimeout: in.FullTierFirstOutputTimeout}
	a.r.samples = append(a.r.samples, row)
}

type captureTimeoutUpstream struct {
	silentUpstream
	dispatched bool
}

func (u *captureTimeoutUpstream) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	if u.dispatched {
		RecordOpsUpstreamSendBoundary(req.Context(), account)
	}
	return u.silentUpstream.Do(req, proxy, account, concurrency)
}

func TestSchedCaptureOwnerTimeoutRequiresPhysicalSend(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, dispatched := range []bool{true, false} {
		t.Run(fmt.Sprintf("dispatched=%t", dispatched), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := `{"model":"wire-model","input":"hello","stream":true}`
				c, _ := compatStreamContext("/v1/responses", body)
				account := rawChatCompletionsTestAccount()
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(c.Request.Context(), st)
				st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
				st.SetSession(7, "session", nil)
				st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "wire-model", StickyWarm: true, HTTPStreamText: true})
				st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, Model: "wire-model", Warm: true, Stream: true, AttemptStart: time.Now()})
				c.Request = c.Request.WithContext(ctx)
				recorder := &captureServiceRecorder{}
				svc := compatStreamService(&captureTimeoutUpstream{dispatched: dispatched}, 1, 0, 0)
				svc.SetSchedAttemptRecorder(recorder)
				_, err := svc.Forward(ctx, c, account, []byte(body))
				requireFirstOutputFailover(t, err)
				require.Len(t, recorder.starts, 1)
				require.False(t, recorder.starts[0].Warm)
				require.Len(t, recorder.samples, 1)
				row := recorder.samples[0]
				require.Equal(t, schedquality.OutcomeTimeout, row.Outcome)
				require.Equal(t, dispatched, row.Warm)
				require.Equal(t, dispatched, row.FullTierFirstOutputTimeout)
				if !dispatched {
					return
				}

				// The request retains the original cutoff while another account delivers.
				rescue := rawChatCompletionsTestAccount()
				rescue.ID = account.ID + 1
				st.SetSchedSelection(resilience.SchedSelection{AccountID: rescue.ID, Model: "wire-model", HTTPStreamText: true})
				svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}}
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)).WithContext(ctx)
				var attempt *schedAttemptCapture
				resp, err := svc.sendSchedHTTP(ctx, rescue, req, "", []byte(body), true, &attempt)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				attempt.finish(nil, nil)
				fact, ok := st.OwnerCutoff()
				require.True(t, ok)
				require.Equal(t, account.ID, fact.AccountID)
				require.Len(t, recorder.samples, 2)
				row = recorder.samples[1]
				require.Equal(t, rescue.ID, row.AccountID)
				require.Equal(t, schedquality.OutcomeOK, row.Outcome)
				require.False(t, row.Warm)
				require.False(t, row.FullTierFirstOutputTimeout)
			})
		})
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
	require.Equal(t, schedquality.OutcomeAccountFault, rec.samples[0].Outcome)
	require.Equal(t, string(resilience.CategoryTransient), rec.samples[0].Detail)
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
	_, err := svc.sendCCUpstreamRequest(ctx, c, account, "://invalid", body, false, "test", "", "", nil, &owned)
	require.Error(t, err)
	require.Nil(t, owned)
	owned.finish(nil, err)
	require.Same(t, prior, st.CurrentAttempt())
	require.Len(t, rec.starts, 1)
	require.Empty(t, rec.samples)
	prior.finishUnfinished()
	require.Len(t, rec.samples, 1)
	require.Equal(t, string(resilience.CategoryTransient), rec.samples[0].Detail)
}

func TestM8ActualUpstreamTarget(t *testing.T) {
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up}
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://UPSTREAM.example:443"}}
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	req, err := svc.buildUpstreamRequestOpenAIPassthrough(ctx, c, account, body, "test")
	require.NoError(t, err)
	var capture *schedAttemptCapture
	_, err = svc.sendSchedHTTP(ctx, account, req, "", body, false, &capture)
	require.NoError(t, err)
	require.Nil(t, capture, "target recording does not require attempt recorder")
	first := st.UpstreamAttemptTarget()
	require.Equal(t, &resilience.UpstreamAttemptTarget{AccountID: 11, Protocol: "responses", Origin: "https://upstream.example"}, first)
	hint := resilience.UpstreamRecoveryHint{NotBefore: time.Now().Add(time.Minute)}
	st.SetUpstreamRecoveryHint(&hint)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://user:pass@[2001:db8::1]:8080/v1/chat/completions?token=secret", nil)
	require.NoError(t, err)
	account.ID = 12
	_, err = svc.sendSchedHTTP(ctx, account, req, "", nil, false, &capture)
	require.NoError(t, err)
	require.Equal(t, &resilience.UpstreamAttemptTarget{AccountID: 12, Protocol: "chat", Origin: "http://[2001:db8::1]:8080"}, st.UpstreamAttemptTarget())
	require.Nil(t, st.UpstreamRecoveryHint())
	require.Equal(t, int64(11), first.AccountID)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "/relative/responses", nil)
	require.NoError(t, err)
	_, err = svc.sendSchedHTTP(ctx, account, req, "", nil, false, &capture)
	require.NoError(t, err)
	require.Nil(t, st.UpstreamAttemptTarget(), "unknown target clears earlier origin")
}
