//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type errclassRecorder struct {
	starts   []SchedAttemptStart
	finishes []SchedAttemptFinish
}
type errclassAttempt struct{ recorder *errclassRecorder }

func (r *errclassRecorder) Begin(in SchedAttemptStart) SchedAttempt {
	r.starts = append(r.starts, in)
	return &errclassAttempt{r}
}
func (*errclassRecorder) SetObserver(func(schedquality.AttemptSample)) {}
func (*errclassRecorder) Flush(context.Context) error                  { return nil }
func (*errclassAttempt) MarkOutput(SchedOutputKind, time.Time)         {}
func (a *errclassAttempt) Finish(in SchedAttemptFinish) {
	a.recorder.finishes = append(a.recorder.finishes, in)
}

// This observable capture test also runs unchanged against the pristine and
// rollback copies; it exercises the actual Finish boundary, not just a helper.
func TestErrclassCaptureBehavior(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		output  bool
		timeout bool
		want    schedquality.ErrorClass
		origin  schedquality.StatusOrigin
	}{
		{"upstream_502", 502, `{"error":{"message":"Synthetic failure"}}`, false, false, schedquality.ClassUpstream, schedquality.OriginUpstream},
		{"gateway_504", 0, "", false, true, schedquality.ClassFirstOutputTimeout, schedquality.OriginGateway},
		{"interrupted", 200, `{"error":{"message":"Synthetic break"}}`, true, false, schedquality.ClassInterrupted, schedquality.OriginUpstream},
		{"credential", 401, `{"error":{"code":"invalid_api_key"}}`, false, false, schedquality.ClassAccountAccess, schedquality.OriginUpstream},
		{"balance", 503, `{"error":{"code":"insufficient_quota"}}`, false, false, schedquality.ClassAccountAccess, schedquality.OriginUpstream},
		{"account_limit", 429, `{"error":{"message":"Account limit reached"}}`, false, false, schedquality.ClassAccountCapacity, schedquality.OriginUpstream},
		{"credential_pool", 429, `{"error":{"message":"All credentials are exhausted"}}`, false, false, schedquality.ClassAccountCapacity, schedquality.OriginUpstream},
		{"model", 404, `{"error":{"code":"model_not_found"}}`, false, false, schedquality.ClassCapability, schedquality.OriginUpstream},
		{"stream_only", 400, `{"error":{"message":"Only supports streaming requests"}}`, false, false, schedquality.ClassCapability, schedquality.OriginUpstream},
		{"protocol", 400, `{"error":{"code":"unsupported_protocol"}}`, false, false, schedquality.ClassCapability, schedquality.OriginUpstream},
		{"migration", 400, `{"error":{"code":"tool_output_missing_call_id"}}`, false, false, schedquality.ClassMigration400, schedquality.OriginUpstream},
		{"continuation", 400, `{"error":{"code":"http_continuation_unsupported"}}`, false, false, schedquality.ClassMigration400, schedquality.OriginUpstream},
		{"parameter_type", 400, `{"error":{"type":"invalid_request_error","message":"Expected object, got string"}}`, false, false, schedquality.ClassRequest, schedquality.OriginUpstream},
		{"request_restriction", 503, `{"error":{"type":"invalid_request_error","code":"unsupported_feature"}}`, false, false, schedquality.ClassRequest, schedquality.OriginUpstream},
		{"unknown_400", 400, `{"error":{"message":"Synthetic rejection"}}`, false, false, schedquality.ClassUnknown, schedquality.OriginUpstream},
		{"unknown_429", 429, `{"error":{"message":"Synthetic rejection"}}`, false, false, schedquality.ClassUnknown, schedquality.OriginUpstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
			var delivered int64
			st.InitDelivery(context.Background(), time.Now(), func() int64 { return delivered })
			ctx := resilience.WithRequestState(context.Background(), st)
			rec := &errclassRecorder{}
			svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
			a := svc.beginSchedAttempt(ctx, &Account{ID: 1, Platform: PlatformOpenAI}, nil, 0, true)
			if tc.output {
				delivered = 17
				st.NoteClientOutputBytes(17)
				a.MarkOutput(OutputValid, time.Now())
			}
			if tc.timeout {
				a.timeout(resilience.TimeoutPlan{})
			} else {
				a.finishHTTP(&http.Response{StatusCode: tc.status}, []byte(tc.body))
				a.finish(nil, nil)
			}
			require.Len(t, rec.finishes, 1)
			got := rec.finishes[0]
			require.Equal(t, tc.want, got.Class)
			require.Equal(t, tc.origin, got.StatusOrigin)
			require.EqualValues(t, delivered, got.OutputBytes)
			require.Equal(t, st.GatewayRequestID(), rec.starts[0].RequestID)
			if tc.timeout {
				require.Equal(t, 504, got.HTTPStatus)
			} else {
				require.Equal(t, tc.status, got.HTTPStatus)
			}
		})
	}
}

func TestErrclassFinishFailurePaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish func(*schedAttemptCapture)
		want   schedquality.ErrorClass
	}{
		{"client", func(a *schedAttemptCapture) { a.cancel() }, schedquality.ClassClientCancel},
		{"network", func(a *schedAttemptCapture) {
			a.finish(nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("synthetic connection refused")})
		}, schedquality.ClassUpstream},
		{"unfinished", func(a *schedAttemptCapture) { a.finishUnfinished() }, schedquality.ClassUnknown},
		{"missing_terminal", func(a *schedAttemptCapture) { a.expectTerminal = true; a.finish(nil, nil) }, schedquality.ClassUnknown},
		{"timeout_after_output", func(a *schedAttemptCapture) {
			a.MarkOutput(OutputValid, time.Now())
			a.timeout(resilience.TimeoutPlan{})
		}, schedquality.ClassInterrupted},
		{"timeout_client_cancel", func(a *schedAttemptCapture) {
			ctx, cancel := context.WithCancel(context.Background())
			a.state.InitDelivery(ctx, time.Now(), nil)
			cancel()
			a.timeout(resilience.TimeoutPlan{})
		}, schedquality.ClassClientCancel},
		{"timeout_budget_cut", func(a *schedAttemptCapture) {
			cfg := resilience.Defaults()
			cfg.Budget.BudgetCutAttribution = resilience.BudgetAttributionEnforce
			start := time.Now().Add(-235 * time.Second)
			budget := resilience.NewBudget(context.Background(), &cfg, resilience.KindText, start)
			plan := budget.PlanAttemptTimeout(30*time.Second, false, start.Add(235*time.Second))
			require.True(t, plan.Applied())
			a.timeout(plan)
		}, schedquality.ClassGateway},
		{"unknown_projected_502", func(a *schedAttemptCapture) { a.finish(nil, &UpstreamFailoverError{StatusCode: 502}) }, schedquality.ClassUnknown},
		{"projected_502_after_http_200", func(a *schedAttemptCapture) {
			a.httpStatus = 200
			a.finish(nil, &UpstreamFailoverError{StatusCode: 502})
		}, schedquality.ClassUnknown},
		{"local_budget", func(a *schedAttemptCapture) {
			a.finish(nil, &UpstreamFailoverError{StatusCode: 504, BudgetCutPlan: &resilience.TimeoutPlan{}})
		}, schedquality.ClassGateway},
		{"stream_failure", func(a *schedAttemptCapture) {
			a.observe([]byte(`{"type":"response.failed","response":{"error":{"code":"model_not_found"}}}`))
			a.finish(nil, nil)
		}, schedquality.ClassCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := resilience.NewRequestState()
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 1})
			rec := &errclassRecorder{}
			svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
			a := svc.beginSchedAttempt(resilience.WithRequestState(context.Background(), st), &Account{ID: 1, Platform: PlatformOpenAI}, nil, 0, true)
			tc.finish(a)
			require.Len(t, rec.finishes, 1)
			require.Equal(t, tc.want, rec.finishes[0].Class)
			require.NotEqual(t, schedquality.ClassNone, rec.finishes[0].Class)
		})
	}
}

func TestErrclassRepairResendSharesRequest(t *testing.T) {
	for _, changed := range []bool{true, false} {
		t.Run(map[bool]string{true: "repair", false: "ordinary retry"}[changed], func(t *testing.T) {
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
			ctx := resilience.WithRequestState(context.Background(), st)
			rec := &errclassRecorder{}
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{}, Body: http.NoBody}}
			svc := &OpenAIGatewayService{httpUpstream: up, schedAttemptRecorder: rec}
			account := &Account{ID: 1, Platform: PlatformOpenAI}
			original := []byte(`{"input":"synthetic","encrypted_content":"synthetic-state"}`)
			req, err := http.NewRequestWithContext(ctx, "POST", "https://synthetic.invalid/v1/responses", nil)
			require.NoError(t, err)
			var first, second *schedAttemptCapture
			st.NoteGatewaySend(account.ID)
			resp, err := svc.sendSchedHTTP(ctx, account, req, "", original, false, &first)
			require.NoError(t, err)
			first.finishHTTP(resp, []byte(`{"error":{"code":"invalid_encrypted_content"}}`))
			require.Empty(t, rec.finishes, "a refusal alone does not establish a repair")
			next := original
			if changed {
				next = []byte(`{"input":"synthetic"}`)
			}
			up.resp = &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}
			_, err = svc.sendSchedHTTP(ctx, account, req, "", next, false, &second)
			require.NoError(t, err)
			st.NoteGatewaySend(account.ID)
			second.finish(nil, nil)
			first.finish(nil, errors.New("late cleanup"))
			require.Len(t, rec.finishes, 2)
			want := schedquality.ClassMigration400
			if changed {
				want = schedquality.ClassRepairStep
			}
			require.Equal(t, want, rec.finishes[0].Class)
			require.Equal(t, schedquality.OriginUpstream, rec.finishes[0].StatusOrigin)
			require.Equal(t, 400, rec.finishes[0].HTTPStatus)
			require.Equal(t, schedquality.OutcomeOK, rec.finishes[1].Outcome)
			require.Equal(t, schedquality.ClassNone, rec.finishes[1].Class)
			require.Equal(t, rec.starts[0].RequestID, rec.starts[1].RequestID)
			require.NotEmpty(t, rec.starts[0].RequestID)
			st.CompleteGatewayForward(true, false)
			meta, _ := st.ClaimGatewayRequest(true)
			require.Equal(t, resilience.RequestRescued, meta.Result)
		})
	}
}

func TestErrclassOutputBytesMeasureWrites(t *testing.T) {
	st := resilience.NewRequestState()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	writer := &deliveryWriter{ResponseWriter: c.Writer, state: st}
	writer.Header().Set("Content-Type", "text/event-stream")
	st.InitDelivery(context.Background(), time.Now(), writer.deliveredBytes)
	opening := "data: {\"type\":\"response.created\"}\n\n"
	n, err := writer.WriteString(opening)
	require.NoError(t, err)
	require.Equal(t, len(opening), n)
	require.Zero(t, st.DeliveredBytes(), "opening events are not semantic delivery")
	require.EqualValues(t, len(opening), st.ClientOutputBytes(), "opening bytes were nevertheless written")
	writer.ResponseWriter = &errclassShortWriter{ResponseWriter: c.Writer}
	n, err = writer.Write([]byte("synthetic partial write"))
	require.Error(t, err)
	require.Equal(t, 5, n)
	require.EqualValues(t, len(opening)+5, st.ClientOutputBytes())
	ctx := resilience.WithRequestState(context.Background(), st)
	frame := []byte(`{"type":"response.output_text.delta","delta":"synthetic"}`)
	noteGatewayWSOutput(ctx, frame)
	require.EqualValues(t, len(opening)+5+len(frame), st.ClientOutputBytes())
}

type errclassShortWriter struct{ gin.ResponseWriter }

func (*errclassShortWriter) Write([]byte) (int, error) { return 5, errors.New("synthetic broken pipe") }

func TestErrclassPreparedInbandRepairIsNotAResend(t *testing.T) {
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
	ctx := resilience.WithRequestState(context.Background(), st)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	repair := armOpenAIRequestRepair(c, account, nil)
	repair.setBody([]byte(`{"input":"synthetic"}`))
	rec := &errclassRecorder{}
	svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
	a := svc.beginSchedAttempt(ctx, account, repair.body, len(repair.body), true)
	a.observe([]byte(`{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content"}}}`))
	signal := &openAIRequestRepairSignal{kind: "synthetic_repair", next: []byte(`{"input":"repaired"}`)}
	a.finish(nil, signal) // Some stream owners freeze before the caller handles repair.
	require.Empty(t, rec.finishes)
	_, _, ok := svc.openAIRequestRepairAfterSignal(ctx, c, account, nil, signal)
	require.True(t, ok)
	require.Empty(t, rec.finishes)
	FinishSchedAttemptAtHandlerExit(ctx) // No second send actually started.
	require.Len(t, rec.finishes, 1)
	require.Equal(t, schedquality.ClassMigration400, rec.finishes[0].Class)
}
