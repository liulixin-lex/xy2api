//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestErrclassWrappedTransportAndExit(t *testing.T) {
	for _, exit := range []bool{false, true} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			st := resilience.NewRequestState()
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
			ctx := resilience.WithRequestState(context.Background(), st)
			rec := &errclassRecorder{}
			original := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("synthetic connection refused")}
			svc := &OpenAIGatewayService{schedAttemptRecorder: rec, httpUpstream: &httpUpstreamRecorder{err: original}}
			req, err := http.NewRequestWithContext(ctx, "POST", "https://synthetic.invalid/v1/responses", nil)
			require.NoError(t, err)
			var capture *schedAttemptCapture
			_, err = svc.sendSchedHTTP(ctx, &Account{ID: 1, Platform: PlatformOpenAI}, req, "", nil, false, &capture)
			require.ErrorIs(t, err, original)
			if exit {
				FinishSchedAttemptAtHandlerExit(ctx)
			} else {
				// The forwarding layer returns this projection instead of the original
				// dial error; the capture must retain the transport's evidence.
				func() {
					defer capture.finish(nil, &UpstreamFailoverError{StatusCode: 502, ResponseBody: openAITransportFailoverBody})
				}()
			}
			require.Len(t, rec.finishes, 1)
			require.Equal(t, schedquality.ClassUpstream, rec.finishes[0].Class)
			if exit {
				require.Zero(t, rec.finishes[0].HTTPStatus)
				require.Equal(t, schedquality.OriginNone, rec.finishes[0].StatusOrigin)
			} else {
				require.Equal(t, 502, rec.finishes[0].HTTPStatus)
				require.Equal(t, schedquality.OriginGateway, rec.finishes[0].StatusOrigin)
			}
		})
	}
}

func TestErrclassRemainingFinishBoundaries(t *testing.T) {
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
	ctx := resilience.WithRequestState(context.Background(), st)
	rec := &errclassRecorder{}
	svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	a := svc.beginSchedAttempt(ctx, account, nil, 0, false)
	a.finish(nil, &UpstreamFailoverError{StatusCode: 503, PreDispatchSelectionInvalidated: true})
	require.Equal(t, schedquality.ClassGateway, rec.finishes[0].Class)
	require.Equal(t, schedquality.OriginGateway, rec.finishes[0].StatusOrigin)
	turns := &schedWSTurnCapture{}
	turns.begin(svc, ctx, account, nil)
	st.NoteClientOutputBytes(13)
	turns.active.MarkOutput(OutputValid, time.Now())
	turns.begin(svc, ctx, account, nil)
	require.Len(t, rec.finishes, 2)
	require.Equal(t, schedquality.ClassInterrupted, rec.finishes[1].Class)
	require.Equal(t, 13, rec.finishes[1].OutputBytes)
	turns.cancel()
}

// Templates are from the local attribution ledger. Placeholder contents are
// synthetic and opaque; the fixed words establish the rule.
func TestErrclassReviewTemplates(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   schedquality.ErrorClass
	}{
		{429, `{"error":{"message":"all available accounts are synthetic-state please retry synthetic-later"}}`, schedquality.ClassAccountCapacity},
		{429, `{"error":{"message":"synthetic-id account synthetic-account limit reached"}}`, schedquality.ClassAccountCapacity},
		{429, `{"error":{"message":"all credentials for model synthetic-model are cooling down via provider synthetic-provider"}}`, schedquality.ClassAccountCapacity},
		{429, `{"error":{"message":"all credentials for model synthetic-model are synthetic-state"}}`, schedquality.ClassAccountCapacity},
		{429, `{"error":{"message":"concurrency limit exceeded for account, please retry later"}}`, schedquality.ClassAccountCapacity},
		{400, `{"error":{"message":"invalid request parameter"}}`, schedquality.ClassRequest},
		{502, `{"error":{"message":"invalid request parameter"}}`, schedquality.ClassUpstream},
		{500, `{"error":{"message":"invalid type: synthetic"}}`, schedquality.ClassUpstream},
		{503, `{"error":{"message":"request too large"}}`, schedquality.ClassUpstream},
		{502, `{"error":{"type":"invalid_request_error","message":"synthetic request restriction"}}`, schedquality.ClassRequest},
		{400, `{"error":{"message":"synthetic frontend requests synthetic-input are not supported on this synthetic-site"}}`, schedquality.ClassCapability},
		{400, `{"error":{"message":"synthetic supports text and https input_image content only synthetic-suffix"}}`, schedquality.ClassCapability},
		{400, `{"error":{"message":"function_call_output synthetic-state synthetic-id on http requests; synthetic previous_response_id is only supported on responses websocket synthetic-suffix"}}`, schedquality.ClassMigration400},
		{400, `{"error":{"message":"function_call_output requires a synthetic-id matching the synthetic tool call; provide the complete synthetic-history"}}`, schedquality.ClassMigration400},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			got := classifySchedAttempt(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault}, &resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: tc.status, Body: []byte(tc.body)}, false)
			require.Equal(t, tc.want, got.Class)
		})
	}
}

func TestErrclassWebSearchActualReplay(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, streamError := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%v/stream=%v", passthrough, streamError), func(t *testing.T) {
				withResilienceSettings(t, nil)
				resetWebSearchDeclarations(t)
				refusal := webSearchRejectionHTTP()
				if streamError {
					refusal = webSearchRejectionStream()
				}
				up := &httpUpstreamRecorder{responses: []*http.Response{refusal, webSearchSuccessStream("resp_synthetic")}}
				svc := newOpenAIImageGenerationControlTestService(up)
				account := webSearchReplayAccount(passthrough)
				req := newWebSearchReplayRequest(t)
				st := resilience.StateFrom(req.ctx)
				st.EnableGatewayRequest(time.Now())
				st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
				rec := &errclassRecorder{}
				svc.SetSchedAttemptRecorder(rec)
				result, err := svc.Forward(req.ctx, req.c, account, []byte(openAIWebSearchHistoryCompactionBody))
				require.NoError(t, err)
				require.True(t, result.CompletedForDelivery())
				require.Len(t, up.requests, 2)
				require.Len(t, rec.finishes, 2)
				require.Equal(t, schedquality.ClassRepairStep, rec.finishes[0].Class)
				require.Equal(t, schedquality.OriginUpstream, rec.finishes[0].StatusOrigin)
				require.Equal(t, rec.starts[0].RequestID, rec.starts[1].RequestID)
				st.CompleteGatewayForward(true, false)
				meta, _ := st.ClaimGatewayRequest(true)
				require.Equal(t, resilience.RequestRescued, meta.Result)
			})
		}
	}
}

func TestErrclassStreamReadCauseSurvivesProjection(t *testing.T) {
	for _, target := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, output := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/output=%v", target, output), func(t *testing.T) {
				withResilienceSettings(t, nil)
				st := resilience.NewRequestState()
				st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
				ctx := resilience.WithRequestState(context.Background(), st)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
				account := newOpenAIRejectedFieldTestAccount()
				account.ID = 1
				if target == "passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				wire := ""
				if output {
					wire = m7ReviewWire(m7ReviewCreated, `{"type":"response.output_text.delta","delta":"synthetic"}`)
				}
				up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &errclassFailingReadBody{reader: strings.NewReader(wire)}}}
				svc := newOpenAIRejectedFieldTestService(up)
				rec := &errclassRecorder{}
				svc.SetSchedAttemptRecorder(rec)
				body := []byte(`{"model":"synthetic-model","input":"synthetic","messages":[{"role":"user","content":"synthetic"}],"stream":true,"max_tokens":100}`)
				var err error
				if target == "responses" || target == "passthrough" {
					_, err = svc.Forward(ctx, c, account, body)
				} else {
					_, err = captureForward(svc, c, account, body, target)
				}
				require.Error(t, err)
				FinishSchedAttemptAtHandlerExit(ctx)
				require.Len(t, rec.finishes, 1)
				want := schedquality.ClassUpstream
				if output {
					want = schedquality.ClassInterrupted
				}
				require.Equal(t, want, rec.finishes[0].Class)
				require.Equal(t, 200, rec.finishes[0].HTTPStatus)
				require.Equal(t, schedquality.OriginUpstream, rec.finishes[0].StatusOrigin)
			})
		}
	}
}

type errclassFailingReadBody struct{ reader io.Reader }

func (r *errclassFailingReadBody) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		return n, errors.New("synthetic stream reset")
	}
	return n, err
}
func (*errclassFailingReadBody) Close() error { return nil }
