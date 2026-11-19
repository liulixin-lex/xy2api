//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOwnerGuardPublicVetoOpportunity(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"only_owner_pinned", "quality_owner_only_pass", "independently_rate_limited", "capacity_full", "idle_not_met", "failed_target"} {
		t.Run(mode, func(t *testing.T) {
			members := []Account{selectionFixtureAccount(2, 2)}
			if mode != "only_owner_pinned" {
				members = append(members, selectionFixtureAccount(1, 1))
			}
			f := newSelectionRuntimeFixture(t, members...)
			f.cache.sessionBindings["openai:session"] = 2
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(t.Context(), st)
			var want []string
			switch mode {
			case "only_owner_pinned":
				ctx = withOpenAIStickyMigrationGuard(ctx, "previous-response", OpenAIUpstreamTransportHTTPSSE)
			case "quality_owner_only_pass":
				f.svc.cfg.Gateway.OpenAIQualityRouting.Mode = "enforce"
				ctx = context.WithValue(withOpenAIQualityRouting(ctx), openAIQualityContextKey{}, &openAIQualityRequest{loaded: true, group: 7, session: "session", scope: "synthetic", state: OpenAIQualityState{Generation: 1, Binding: 2, Owner: 2}})
				want = []string{"quality_owned"}
			case "idle_not_met":
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 1, time.Minute))
				f.view.Takeovers[7] = fakeSchedTakeover{Enabled: true, Idle: time.Hour}
				f.cache.remaining = f.svc.openAIStickyBindingTTL(ptrInt64Selection(7))
			case "capacity_full":
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 1, time.Minute))
				f.loads.loadMap = map[int64]*AccountLoadInfo{1: {LoadRate: 100, CurrentConcurrency: 2}}
			case "failed_target":
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 1, time.Minute))
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, stickyChangeAvoidSession("session"), 1, time.Minute))
			case "independently_rate_limited":
				st.NoteAccountRetryNotBefore(1, resilience.UpstreamRecoveryHint{NotBefore: time.Now().Add(time.Minute)})
				require.True(t, accountRetryNotBeforeBlocked(ctx, 1))
				require.NoError(t, f.svc.resilience.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 1, time.Minute))
			}
			logs := captureOpenAIObservationLogs(t)
			selected := selectTrialRuntime(t, f, ctx, "session")
			defer selected.ReleaseFunc()
			require.EqualValues(t, 2, selected.Account.ID)
			var reasons []string
			for _, row := range schedObsLogRecords(t, logs) {
				if row["msg"] == "sched_quality.session_switch" {
					reasons = append(reasons, row["reason"].(string))
				}
			}
			t.Logf("selected=%d reasons=%v", selected.Account.ID, reasons)
			require.Equal(t, want, reasons)
		})
	}
}

func TestConvertedTimeoutPlanIdentity(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		logs := captureOpenAIObservationLogs(t)
		start := time.Now()
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(t.Context(), st)
		st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start))
		st.SetGroup(7)
		account := &Account{ID: 31, Platform: PlatformAnthropic}
		const model = "synthetic-mapped-model"
		st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 31, Model: model, AttemptStart: start})
		send, g := (&GatewayService{}).armGatewayConvertFirstEvent(ctx, ctx, account, true, st.AttemptProfile())
		require.NotNil(t, g)
		defer g.Release()
		before := st.GatewayRequestID()
		RecordOpsUpstreamSendBoundary(send, account.ID)
		RecordOpsUpstreamSendBoundary(send, account.ID)
		<-send.Done()
		st.BeginGatewayTurn(time.Now())
		RecordOpsUpstreamSendBoundary(send, account.ID)
		require.True(t, g.Fired())
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
		g.failoverError(c, account, "response_headers")
		for _, row := range schedObsLogRecords(t, logs) {
			if row["msg"] == "resilience.budget_timeout_plan" {
				t.Logf("seq=%v model=%v", row["attempt_seq"], row["model"])
				require.EqualValues(t, 2, row["attempt_seq"])
				require.Equal(t, model, row["model"])
				require.EqualValues(t, 7, row["group_id"])
				require.Equal(t, before, row["gateway_request_id"])
				return
			}
		}
		t.Fatal("missing timeout event")
	})
}

func TestOpenAIHeaderTimeoutBeforeDispatch(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		logs := captureOpenAIObservationLogs(t)
		body := []byte(`{"model":"wire-model","input":"hello","stream":true}`)
		c, _ := compatStreamContext("/v1/responses", string(body))
		account := rawChatCompletionsTestAccount()
		st, ctx := ownerFactsState(c.Request.Context(), account)
		c.Request = c.Request.WithContext(ctx)
		before := st.GatewayRequestID()
		up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
			<-req.Context().Done()
			st.BeginGatewayTurn(time.Now())
			RecordOpsUpstreamSendBoundary(req.Context(), id)
			return nil, req.Context().Err()
		}}
		_, err := compatStreamService(up, 1, 0, 0).Forward(ctx, c, account, body)
		requireFirstOutputFailover(t, err)
		for _, row := range schedObsLogRecords(t, logs) {
			if row["msg"] == "resilience.budget_timeout_plan" {
				t.Logf("timer fired before send boundary; logged seq=%v", row["attempt_seq"])
				require.Nil(t, row["attempt_seq"])
				require.Equal(t, before, row["gateway_request_id"])
				return
			}
		}
		t.Fatal("missing timeout event")
	})
}

// CheckOwnerGuardRedirectTimeout exercises Forward with the real repository transport.
func CheckOwnerGuardRedirectTimeout(t *testing.T, up HTTPUpstream) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.ModelOverrides = map[string]int{"wire-model": 1} })
	var hops atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			hops.Add(1)
		}
		if r.URL.Path != "/after" {
			http.Redirect(w, r, "/after", http.StatusTemporaryRedirect)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	body := []byte(`{"model":"wire-model","input":"hello","stream":true}`)
	c, _ := compatStreamContext("/v1/responses", string(body))
	logs := captureOpenAIObservationLogs(t)
	account := rawChatCompletionsTestAccount()
	account.Credentials["base_url"] = server.URL
	st, ctx := ownerFactsState(c.Request.Context(), account)
	c.Request = c.Request.WithContext(ctx)
	svc := compatStreamService(up, 1, 0, 0)
	svc.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	_, err := svc.Forward(ctx, c, account, body)
	requireFirstOutputFailover(t, err)
	require.EqualValues(t, 2, hops.Load())
	_, confirmed := st.OwnerCutoff()
	require.False(t, confirmed)
	for _, row := range schedObsLogRecords(t, logs) {
		if row["msg"] == "resilience.budget_timeout_plan" {
			t.Logf("server received %d POSTs, reported attempt_seq=%v", hops.Load(), row["attempt_seq"])
			require.EqualValues(t, 2, row["attempt_seq"])
			return
		}
	}
	t.Fatal("missing timeout event")
}

func TestTimeoutPlanUnknownGroup(t *testing.T) {
	withResilienceSettings(t, nil)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(t.Context(), st)
	now := time.Now()
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, now))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 31, Model: "synthetic", AttemptStart: now})
	logs := captureOpenAIObservationLogs(t)
	noteTimeoutPlan(ctx, &Account{ID: 31}, st.PlanAttemptTimeout(time.Second, false, now), timeoutIdentityAt(ctx, nil))
	rows := schedObsLogRecords(t, logs)
	require.Len(t, rows, 1)
	t.Logf("no group configured; group_id=%v", rows[0]["group_id"])
	require.Nil(t, rows[0]["group_id"])
}

func TestFirstEventTimeoutBeforeDispatch(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		output := captureSchedObsLogs(t)
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(t.Context(), st)
		now := time.Now()
		st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, now))
		st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 1, Model: "model-a", AttemptStart: now})
		plan := st.PlanAttemptTimeout(time.Second, false, now)
		sent, g := newFirstEventGuard(ctx, plan.Effective(), plan)
		defer g.Release()
		<-sent.Done()
		requestID := st.GatewayRequestID()
		st.BeginGatewayTurn(time.Now())
		RecordOpsUpstreamSendBoundary(sent, 1)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
		g.failoverError(c, &Account{ID: 1}, "response_headers")
		for _, row := range schedObsLogRecords(t, output) {
			if row["msg"] == "resilience.budget_timeout_plan" {
				require.Nil(t, row["attempt_seq"])
				require.Equal(t, requestID, row["gateway_request_id"])
				return
			}
		}
		t.Fatal("missing timeout plan")
	})
}

func TestOwnerGuardNonTextTakeover(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, avoided := range []bool{false, true} {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(2, 2), selectionFixtureAccount(1, 1))
		f.cache.sessionBindings["openai:session"] = 2
		st := resilience.NewRequestState()
		st.EnableGatewayRequest(time.Now(), resilience.GatewayRequestScopeImage)
		ctx := resilience.WithRequestState(t.Context(), st)
		want := int64(1)
		if avoided {
			require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 1, time.Minute))
			want = 2
		}
		sel := selectTrialRuntime(t, f, ctx, "session")
		require.Equal(t, want, sel.Account.ID)
		followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
		require.Equal(t, want, f.cache.sessionBindings["openai:session"])
		sel.ReleaseFunc()
	}
}

func TestOpenAIHeaderTimeoutRequestIdentity(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				output := captureSchedObsLogs(t)
				account := rawChatCompletionsTestAccount()
				ids := make([]string, 2)
				var wg sync.WaitGroup
				type expired struct {
					ctx  context.Context
					c    *gin.Context
					g    *openAIFirstOutputHeaderGuard
					svc  *OpenAIGatewayService
					plan resilience.TimeoutPlan
				}
				events := make([]expired, 2)
				now := time.Now()
				for i := range ids {
					c, _ := compatStreamContext("/v1/responses", "{}")
					st, ctx := ownerFactsState(c.Request.Context(), account)
					st.EnableGatewayRequest(now, resilience.GatewayRequestScopeText)
					c.Request = c.Request.WithContext(ctx)
					ids[i] = st.GatewayRequestID()
					if i == 1 {
						RecordOpsUpstreamSendBoundary(ctx, account.ID)
					}
					plan := st.PlanAttemptTimeout(time.Second, false, now)
					send, g := newOpenAIFirstOutputHeaderGuard(ctx, func() {}, now.Add(plan.Effective()), plan)
					req := httptest.NewRequest("POST", "http://fixture.invalid/v1/responses", nil).WithContext(send)
					svc := compatStreamService(factsUpstream{send: func(r *http.Request, id int64) (*http.Response, error) {
						RecordOpsUpstreamSendBoundary(r.Context(), id)
						<-r.Context().Done()
						return nil, r.Context().Err()
					}}, 1, 0, 0)
					events[i] = expired{ctx, c, g, svc, plan}
					wg.Go(func() {
						var capture *schedAttemptCapture
						_, _ = svc.sendSchedHTTP(ctx, account, req, "", nil, stream, &capture)
					})
				}
				wg.Wait()
				for _, e := range events {
					require.True(t, e.g.stopHeaderWait())
					require.Equal(t, now.Add(e.plan.Effective()), e.g.firedAt)
					e.svc.newOpenAIFirstOutputTimeoutError(context.WithValue(e.ctx, firedOpenAITimeoutKey{}, e.g.outcome), e.c, account, nil, "", now, "wire-model", "", e.plan.Effective(), "response_headers", nil, e.plan)
					e.g.close()
				}
				got := map[string]any{}
				for _, row := range schedObsLogRecords(t, output) {
					if row["msg"] == "resilience.budget_timeout_plan" {
						got[row["gateway_request_id"].(string)] = row["attempt_seq"]
					}
				}
				require.NotEqual(t, ids[0], ids[1])
				require.Equal(t, map[string]any{ids[0]: float64(1), ids[1]: float64(2)}, got)
			})
		})
	}
}

func TestOwnerGuardProfitFloorModels(t *testing.T) {
	owner, target := selectionFixtureAccount(2, 2), selectionFixtureAccount(1, 1)
	owner.Credentials = map[string]any{"model_mapping": map[string]any{"m": "model-before"}}
	target.Credentials = map[string]any{"model_mapping": map[string]any{"m": "model-after"}}
	owner.RateMultiplier, target.RateMultiplier = floatPtr(0.1), floatPtr(1)
	f := newSelectionRuntimeFixture(t, owner, target)
	f.cache.sessionBindings["openai:session"] = owner.ID
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(t.Context(), st)
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 7, platform: PlatformOpenAI, floor: 0.5, pricingAt: time.Now()})
	logs := captureSchedObsLogs(t)
	sel := selectTrialRuntime(t, f, ctx, "session")
	defer sel.ReleaseFunc()
	require.Equal(t, target.ID, sel.Account.ID)
	CompleteOpenAIStickyChange(ctx, sel.Account.ID, true, false)
	for _, row := range schedObsLogRecords(t, logs) {
		if row["msg"] == "sched_quality.session_switch" {
			require.Equal(t, "owner_released", row["kind"])
			require.Equal(t, "profit_floor", row["reason"])
			require.Equal(t, "m", row["model"])
			require.Equal(t, "model-before", row["from_model"])
			require.Equal(t, "model-after", row["to_model"])
			return
		}
	}
	t.Fatal("missing release event")
}
