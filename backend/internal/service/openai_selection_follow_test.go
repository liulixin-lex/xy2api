//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func followState(t *testing.T, f *selectionSessionFixture, confirm bool) context.Context {
	t.Helper()
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetStickyChange(&openAIStickyChange{groupID: 7, session: "session", key: f.binding.key, expected: StickySessionBinding{AccountID: f.owner.ID, Generation: "g1"}, cache: f.binding, ttl: time.Hour})
	fact := resilience.OwnerFirstOutputCutoff{GroupID: 7, Session: "session", AccountID: f.owner.ID, BindingKey: f.binding.key, Generation: "g1", AttemptSeq: st.MarkOpsUpstreamSendBoundary(f.owner.ID), Effective: time.Second, FiredAt: time.Now(), CanTransfer: true}
	require.True(t, st.RecordOwnerCutoff(fact))
	if confirm {
		recordFirstOutputAvoid(ctx, fact)
	}
	return ctx
}

func followSelect(svc *OpenAIGatewayService, ctx context.Context, acc *Account) {
	svc.planOpenAIFollow(ctx, ptrInt64Selection(7), "session", "m", &AccountSelectionResult{Account: acc}, true)
}

func followDelivery(svc *OpenAIGatewayService, ctx context.Context, acc *Account, delay time.Duration, result *OpenAIForwardResult) {
	a := svc.beginSchedAttempt(ctx, acc, nil, 0, true)
	if a != nil {
		a.expectTerminal = true
		if delay >= 0 {
			a.observeOutput([]byte(`{"type":"response.output_text.delta","delta":"ok"}`), a.started.Add(delay))
		}
		a.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`))
		a.finish(result, nil)
	}
	FinishOpenAIStickyForward(ctx, acc.ID, result, nil)
}

func TestFirstOutputFollowDelivery(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name           string
		delay          time.Duration
		result         OpenAIForwardResult
		follow, failed bool
	}{
		{"strictly_faster", time.Second - time.Nanosecond, OpenAIForwardResult{}, true, false},
		{"zero", 0, OpenAIForwardResult{}, true, false},
		{"equal", time.Second, OpenAIForwardResult{}, false, false},
		{"slower", time.Second + time.Nanosecond, OpenAIForwardResult{}, false, false},
		{"missing", -1, OpenAIForwardResult{}, false, false},
		{"partial", time.Nanosecond, OpenAIForwardResult{PartialFailure: true}, false, true},
		{"canceled", time.Nanosecond, OpenAIForwardResult{ClientDisconnect: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, svc := selectionNewSessionFixture(), &OpenAIGatewayService{}
			ctx := followState(t, f, true)
			acc := f.candidates[0]
			followSelect(svc, ctx, acc)
			require.Equal(t, stickyFollow, pendingOpenAIStickyChange(ctx).kind)
			followDelivery(svc, ctx, acc, tc.delay, &tc.result)
			FinishOpenAIStickyForward(ctx, acc.ID, &tc.result, nil)
			require.Equal(t, tc.follow, f.binding.owner == acc.ID)
			if tc.follow {
				require.Len(t, f.binding.casKeys, 1)
			} else {
				require.Empty(t, f.binding.casKeys)
			}
			failed, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
			require.NoError(t, err)
			_, found := failed[acc.ID]
			require.Equal(t, tc.failed, found)
		})
	}
}

func TestFirstOutputFollowRelayAndConcurrentGeneration(t *testing.T) {
	withResilienceSettings(t, nil)
	f, svc := selectionNewSessionFixture(), &OpenAIGatewayService{}
	ctx := followState(t, f, true)
	other := followState(t, f, true)
	y, z, late := f.candidates[0], f.candidates[1], f.candidates[2]
	followSelect(svc, ctx, y)
	followDelivery(svc, ctx, y, 0, &OpenAIForwardResult{PartialFailure: true})
	require.Equal(t, f.owner.ID, f.binding.owner)
	followSelect(svc, ctx, z)
	followSelect(svc, other, late)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for _, turn := range []struct {
		ctx context.Context
		acc *Account
	}{{ctx, z}, {other, late}} {
		wg.Go(func() { <-ready; followDelivery(svc, turn.ctx, turn.acc, 0, &OpenAIForwardResult{}) })
	}
	close(ready)
	wg.Wait()
	require.Len(t, f.binding.casKeys, 2)
	require.Contains(t, []int64{z.ID, late.ID}, f.binding.owner)
	require.Equal(t, "g1-next", f.binding.generation)
	avoid, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, err)
	require.Equal(t, map[int64]struct{}{y.ID: {}}, avoid)
}

func TestFirstOutputFollowRequiresReceiptAndFront(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, scenario := range []string{"rejected", "error", "expired", "spillover", "trial", "rear", "last_resort", "protocol", "kept_owner"} {
		t.Run(scenario, func(t *testing.T) {
			f := selectionNewSessionFixture()
			svc := &OpenAIGatewayService{}
			NewOpenAISelectionBoard(svc, func() schedquality.View { return f.view }).now = func() time.Time { return f.now }
			ctx := followState(t, f, false)
			st := resilience.StateFrom(ctx)
			fact, _ := st.OwnerCutoff()
			acc := f.candidates[0]
			sel := &AccountSelectionResult{Account: acc}
			switch scenario {
			case "rejected":
				f.binding.avoidRejected = true
			case "error":
				f.binding.avoidErr = errors.New("unavailable")
			case "expired":
				fact.FiredAt = time.Now().Add(-11 * time.Minute)
			case "spillover":
				sel.StickyCapacitySpillover = true
			case "trial":
				f.state(acc.ID, schedquality.StateView{Cache: schedquality.LineView{Trial: true}})
			case "rear":
				f.state(acc.ID, schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}})
			case "last_resort":
				st.SetLastResort(acc.ID)
			case "protocol":
				ctx = withOpenAINoStickyMigration(ctx)
			case "kept_owner":
				st.KeepSticky(f.owner.ID)
			}
			recordFirstOutputAvoid(ctx, fact)
			_, confirmed := st.ConfirmedOwnerCutoff()
			require.Equal(t, scenario != "rejected" && scenario != "error" && scenario != "expired", confirmed)
			logs := captureOpenAIObservationLogs(t)
			svc.planOpenAIFollow(ctx, ptrInt64Selection(7), "session", "m", sel, true)
			events := schedObsLogRecords(t, logs)
			require.Len(t, events, 1)
			require.Equal(t, "sched_quality.session_switch", events[0]["msg"])
			require.Equal(t, st.GatewayRequestID(), events[0]["gateway_request_id"])
			require.Equal(t, false, events[0]["committed"])
			switch scenario {
			case "trial", "rear":
				require.Equal(t, "not_front", events[0]["reason"])
			case "rejected", "error", "expired":
				require.Equal(t, "avoid_unconfirmed", events[0]["reason"])
			case "protocol":
				require.Equal(t, "protocol_ineligible", events[0]["reason"])
			default:
				require.Equal(t, "borrowed", events[0]["reason"])
			}
			require.Zero(t, pendingOpenAIStickyChange(ctx).target)
			followDelivery(svc, ctx, acc, 0, &OpenAIForwardResult{})
			require.Empty(t, f.binding.casKeys)
		})
	}
}

type followScopeReader struct {
	ResilienceStore
	reads int
	err   error
}

func (r *followScopeReader) SessionAvoidWrittenAt(ctx context.Context, group int64, scope string) (map[int64]time.Time, error) {
	if scope == firstOutputAvoidSession("session") {
		r.reads++
		if r.err != nil {
			return nil, r.err
		}
	}
	return r.ResilienceStore.SessionAvoidWrittenAt(ctx, group, scope)
}

func TestFirstOutputFollowTakeoverScope(t *testing.T) {
	for _, scenario := range []string{"ordinary_empty", "ordinary_avoided", "quality_owned", "not_injected", "read_error", "migrate_avoided", "session_disabled"} {
		t.Run(scenario, func(t *testing.T) {
			f := selectionNewSessionFixture()
			store := &followScopeReader{ResilienceStore: f.session.store}
			f.session.store = store
			if scenario != "not_injected" {
				f.session.qualityOwnsSticky = func(context.Context, *int64, string) bool { return scenario == "quality_owned" }
			}
			if scenario != "ordinary_empty" {
				require.NoError(t, store.AddSessionAvoid(context.Background(), 7, firstOutputAvoidSession("session"), 1, time.Minute))
			}
			if scenario == "session_disabled" {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Session.Enabled = false })
			}
			if scenario == "read_error" {
				store.err = errors.New("unavailable")
			}
			if scenario == "migrate_avoided" {
				f.state(f.owner.ID, schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}})
			}
			sel := f.selectNow(context.Background())
			if scenario == "read_error" {
				require.Nil(t, sel)
				require.Empty(t, f.binding.casKeys)
				return
			}
			require.NotNil(t, sel)
			sel.ReleaseFunc()
			if scenario == "ordinary_avoided" || scenario == "migrate_avoided" {
				require.EqualValues(t, 2, f.binding.owner)
			} else {
				require.EqualValues(t, 1, f.binding.owner)
			}
			if scenario == "quality_owned" || scenario == "not_injected" || scenario == "session_disabled" {
				require.Zero(t, store.reads)
			} else {
				require.Equal(t, 1, store.reads)
			}
		})
	}
}

func TestFirstOutputFollowGuardAndConcurrentSelection(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.ModelOverrides = map[string]int{"m": 1, "wire-model": 1} })
	synctest.Test(t, func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(11, 2))
		f.cache.sessionBindings["openai:session"] = 10
		selectFor := func(ctx context.Context, excluded map[int64]struct{}) *AccountSelectionResult {
			sel, _, err := f.svc.SelectAccountWithScheduler(ctx, ptrInt64Selection(7), "", "session", "m", excluded, OpenAIUpstreamTransportHTTPSSE, false)
			require.NoError(t, err)
			require.NotNil(t, sel)
			if sel.ReleaseFunc != nil {
				sel.ReleaseFunc()
			}
			return sel
		}
		fresh := func() context.Context {
			return resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		}
		alreadyCtx := fresh()
		already := selectFor(alreadyCtx, nil)
		require.EqualValues(t, 10, already.Account.ID)
		alreadyOnWire := make(chan int64, 1)
		releaseAlready := make(chan struct{})
		finishAlready := sync.OnceFunc(func() { close(releaseAlready) })
		defer finishAlready()
		alreadyDone := make(chan error, 1)
		oldSender := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
			RecordOpsUpstreamSendBoundary(req.Context(), id)
			alreadyOnWire <- id
			<-releaseAlready
			return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
		}}, 0, 0, 0)
		req, err := http.NewRequestWithContext(alreadyCtx, http.MethodPost, "https://upstream.invalid", nil)
		require.NoError(t, err)
		go func() {
			var a *schedAttemptCapture
			resp, err := oldSender.sendSchedHTTP(alreadyCtx, already.Account, req, "", nil, true, &a)
			if err == nil {
				err = resp.Body.Close()
			}
			alreadyDone <- err
		}()
		select {
		case id := <-alreadyOnWire:
			require.EqualValues(t, 10, id)
		case err := <-alreadyDone:
			t.Fatalf("already selected request returned before dispatch: %v", err)
		}
		const body = `{"model":"m","input":"hello","stream":true}`
		c, _ := compatStreamContext("/v1/responses", body)
		account := rawChatCompletionsTestAccount()
		account.ID = 10
		st, ctx := ownerFactsState(c.Request.Context(), account)
		st.SetStickyChange(nil)
		require.EqualValues(t, 10, selectFor(ctx, nil).Account.ID)
		c.Request = c.Request.WithContext(ctx)
		blocked := make(chan struct{})
		up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
			RecordOpsUpstreamSendBoundary(req.Context(), id)
			<-req.Context().Done()
			<-blocked
			return nil, req.Context().Err()
		}}
		sender := compatStreamService(up, 1, 0, 0)
		done := make(chan error, 1)
		go func() {
			_, err := sender.Forward(ctx, c, account, []byte(body))
			done <- err
		}()
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		fact, ok := st.ConfirmedOwnerCutoff()
		close(blocked)
		requireFirstOutputFailover(t, <-done)
		require.True(t, ok)
		store := f.cache.store.(*memResilienceStore)
		expiry := func() time.Time { return store.avoid[memSessionKey(7, firstOutputAvoidSession("session"))][10] }
		require.Equal(t, fact.FiredAt.Add(600*time.Second), expiry())
		y := selectFor(ctx, map[int64]struct{}{10: {}})
		require.EqualValues(t, 11, y.Account.ID)
		require.Equal(t, stickyFollow, pendingOpenAIStickyChange(ctx).kind)
		// Hold Y at its physical send while fresh requests read the shared store.
		rescueOnWire := make(chan int64, 1)
		releaseRescue := make(chan struct{})
		finishRescue := sync.OnceFunc(func() { close(releaseRescue) })
		defer finishRescue()
		rescuer := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
			RecordOpsUpstreamSendBoundary(req.Context(), id)
			rescueOnWire <- id
			<-releaseRescue
			stream := "data: " + `{"type":"response.output_text.delta","delta":"ok"}` + "\n\n" + "data: " + `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream)), Request: req}, nil
		}}, 0, 0, 0)
		y.Account.Credentials = account.Credentials
		st.SetSchedSelection(resilience.SchedSelection{AccountID: y.Account.ID, Model: "m", HTTPStreamText: true})
		st.SetAttemptProfile(resilience.AttemptProfile{AccountID: y.Account.ID, Model: "m", AttemptStart: time.Now(), Stream: true})
		go func() {
			result, err := rescuer.Forward(ctx, c, y.Account, []byte(body))
			FinishOpenAIStickyForward(ctx, y.Account.ID, result, err)
			done <- err
		}()
		select {
		case id := <-rescueOnWire:
			require.EqualValues(t, 11, id)
		case err := <-done:
			t.Fatalf("rescue returned before dispatch: %v", err)
		}
		other := fresh()
		borrow := selectFor(other, nil)
		require.EqualValues(t, 11, borrow.Account.ID)
		require.Zero(t, pendingOpenAIStickyChange(other).target)
		require.EqualValues(t, 10, selectFor(withOpenAINoStickyMigration(fresh()), nil).Account.ID)
		last := fresh()
		require.EqualValues(t, 10, selectFor(last, map[int64]struct{}{11: {}}).Account.ID)
		require.EqualValues(t, 10, resilience.StateFrom(last).LastResort())
		finishAlready()
		require.NoError(t, <-alreadyDone)
		finishRescue()
		require.NoError(t, <-done)
		require.EqualValues(t, 11, f.cache.sessionBindings["openai:session"])
		require.Equal(t, fact.FiredAt.Add(600*time.Second), expiry())
		require.EqualValues(t, 11, selectFor(fresh(), nil).Account.ID)
	})
}

func TestFirstOutputFollowGuardWritesOnlyForFullOwner(t *testing.T) {
	for _, scenario := range []string{"full", "budget_cut", "client_cancel", "late_ack"} {
		t.Run(scenario, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Session.AvoidSeconds = 45 })
			synctest.Test(t, func(t *testing.T) {
				f := selectionNewSessionFixture()
				client, cancel := context.WithCancel(context.Background())
				defer cancel()
				account := f.owner
				st, ctx := ownerFactsState(client, account)
				change := pendingOpenAIStickyChange(ctx)
				change.expected.Generation = f.binding.generation
				change.cache = f.binding
				if scenario == "late_ack" {
					f.binding.avoidDelay = 10 * time.Second
				}
				svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
					RecordOpsUpstreamSendBoundary(req.Context(), id)
					return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
				}}, 0, 0, 0)
				svc.schedAttemptRecorder = &captureServiceRecorder{}
				send := func(acc *Account) *schedAttemptCapture {
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.invalid", nil)
					require.NoError(t, err)
					var a *schedAttemptCapture
					resp, err := svc.sendSchedHTTP(ctx, acc, req, "", nil, true, &a)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					return a
				}
				a := send(account)
				tier := time.Second
				if scenario == "budget_cut" {
					tier = time.Hour
				}
				plan := st.PlanAttemptTimeout(tier, false, time.Now())
				time.Sleep(plan.Effective())
				fired := time.Now()
				if scenario == "client_cancel" {
					cancel()
				}
				// Delay publication to check the deadline is anchored to the guard.
				time.Sleep(3 * time.Second)
				_, _ = claimOpenAIFirstOutputCutoff(a, plan, fired, nil)
				_, _ = claimOpenAIFirstOutputCutoff(a, plan, time.Now(), nil)
				_, confirmed := st.ConfirmedOwnerCutoff()
				require.Equal(t, scenario == "full", confirmed)
				store := f.session.store.(*memResilienceStore)
				entries := store.avoid[memSessionKey(7, firstOutputAvoidSession("session"))]
				if scenario != "full" && scenario != "late_ack" {
					require.Empty(t, entries)
					return
				}
				if scenario == "late_ack" {
					return
				}
				require.Equal(t, map[int64]time.Time{account.ID: fired.Add(45 * time.Second)}, entries)
				y := f.candidates[0]
				st.SetSchedSelection(resilience.SchedSelection{AccountID: y.ID, Model: "m", HTTPStreamText: true})
				st.SetAttemptProfile(resilience.AttemptProfile{AccountID: y.ID, Model: "m", Stream: true, AttemptStart: time.Now()})
				b := send(y)
				yp := st.PlanAttemptTimeout(time.Second, false, time.Now())
				time.Sleep(yp.Effective())
				_, _ = claimOpenAIFirstOutputCutoff(b, yp, time.Now(), nil)
				require.Equal(t, map[int64]time.Time{account.ID: fired.Add(45 * time.Second)}, entries)
			})
		})
	}
}

func TestFirstOutputFollowProtectedSelectionsDoNotReadScope(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"ordinary", "previous_response", "websocket", "encrypted", "guardian", "child", "manual", "quality", "seedance", "image_intent", "protected_chat", "session_disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(11, 2))
			f.cache.sessionBindings["openai:session"] = 10
			store := &followScopeReader{ResilienceStore: f.svc.resilience.store}
			f.svc.resilience.store = store
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			require.NoError(t, store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 10, time.Minute))
			previous, transport := "", OpenAIUpstreamTransportHTTPSSE
			capability := OpenAIEndpointCapabilityChatCompletions
			switch mode {
			case "session_disabled":
				withResilienceSettings(t, func(s *resilience.Settings) { s.Session.Enabled = false })
			case "protected_chat":
				ctx = WithOpenAITransferProtected(ctx)
			case "seedance":
				capability = OpenAIEndpointCapabilitySeedance
			case "image_intent":
				resilience.StateFrom(ctx).EnableGatewayRequest(time.Now(), resilience.GatewayRequestScopeImage)
			case "previous_response":
				previous = "resp_follow"
			case "websocket":
				transport = OpenAIUpstreamTransportResponsesWebsocketV2
			case "encrypted":
				ctx = withOpenAINoStickyMigration(ctx)
			case "guardian":
				ctx = context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{currentSessionHash: "session"})
			case "child":
				ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: "child", preserveBinding: true})
			case "manual":
				ctx = withOpenAISelectionManualOnly(ctx)
			case "quality":
				f.svc.cfg.Gateway.OpenAIQualityRouting.Mode = "enforce"
				ctx = context.WithValue(withOpenAIQualityRouting(ctx), openAIQualityContextKey{}, &openAIQualityRequest{
					session: "session", group: 7, scope: "synthetic", loaded: true, pinned: true,
					state: OpenAIQualityState{Generation: 1, Binding: 10, Owner: 10},
				})
			}
			sel, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), previous, "session", "m", nil, transport, capability, false, false, true)
			if sel != nil && sel.ReleaseFunc != nil {
				defer sel.ReleaseFunc()
			}
			if mode == "ordinary" {
				require.NoError(t, err)
				require.EqualValues(t, 11, sel.Account.ID)
				require.Positive(t, store.reads)
			} else {
				require.Zero(t, store.reads)
				if mode == "session_disabled" {
					require.NoError(t, err)
					require.EqualValues(t, 10, sel.Account.ID)
				}
			}
		})
	}
}

func TestFirstOutputFollowLegacySelectionKeepsOwner(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"no_request_state", "unversioned_cache", "snapshot_error"} {
		for _, avoid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/avoid=%t", mode, avoid), func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(11, 2))
				const key = "openai:session"
				f.cache.sessionBindings[key] = 10
				store := &followScopeReader{ResilienceStore: f.svc.resilience.store}
				f.svc.resilience.store = store
				ctx := context.Background()
				if mode != "no_request_state" {
					ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
					if mode == "snapshot_error" {
						f.svc.cache = &bindingReadErrorCache{f.cache}
					} else {
						f.svc.cache = struct{ GatewayCache }{f.cache}
					}
				}
				if avoid {
					require.NoError(t, store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 10, time.Minute))
				}
				sel, _, err := f.svc.SelectAccountWithScheduler(ctx, ptrInt64Selection(7), "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, false)
				require.NoError(t, err)
				require.NotNil(t, sel)
				if sel.ReleaseFunc != nil {
					sel.ReleaseFunc()
				}
				require.EqualValues(t, 10, sel.Account.ID)
				require.EqualValues(t, 10, f.cache.sessionBindings[key])
				if mode == "snapshot_error" {
					require.True(t, pendingOpenAIStickyChange(ctx).skipCommit)
				} else {
					require.Nil(t, pendingOpenAIStickyChange(ctx))
				}
				require.Zero(t, store.reads)
			})
		}
	}
}

func TestFirstOutputFollowReceiptBeforeBlockingClose(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"responses", "passthrough"} {
		t.Run(route, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := []byte(`{"model":"wire-model","input":"hello","stream":true}`)
				c, _ := compatStreamContext("/v1/responses", string(body))
				account := rawChatCompletionsTestAccount()
				st, ctx := ownerFactsState(c.Request.Context(), account)
				c.Request = c.Request.WithContext(ctx)
				f := selectionNewSessionFixture()
				f.binding.owner, f.binding.generation = account.ID, "v1"
				gate := &followWriteBarrier{f.binding, make(chan string), make(chan struct{})}
				pendingOpenAIStickyChange(ctx).cache = gate
				r, w := io.Pipe()
				defer w.Close()
				b := &factsBlockingCloser{ReadCloser: r, enter: make(chan struct{}), leave: make(chan struct{})}
				var transport context.Context
				svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
					transport = req.Context()
					RecordOpsUpstreamSendBoundary(transport, id)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: b, Request: req}, nil
				}}, 1, 0, 0)
				done := make(chan error, 1)
				go func() {
					var err error
					if route == "passthrough" {
						_, err = svc.forwardOpenAIPassthrough(ctx, c, account, body, body, "wire-model", false, nil, true, time.Now())
					} else {
						_, err = svc.Forward(ctx, c, account, body)
					}
					done <- err
				}()
				scope := <-gate.enter
				canceledAtWrite := transport.Err()
				_, earlyReceipt := st.ConfirmedOwnerCutoff()
				closeStarted := false
				select {
				case <-b.enter:
					closeStarted = true
				default:
				}
				close(gate.resume)
				<-b.enter
				fact, confirmed := st.ConfirmedOwnerCutoff()
				avoid, err := f.session.store.SessionAvoid(ctx, 7, firstOutputAvoidSession("session"))
				close(b.leave)
				requireFirstOutputFailover(t, <-done)
				require.NoError(t, err)
				require.True(t, confirmed)
				require.Equal(t, firstOutputAvoidSession("session"), scope)
				require.ErrorIs(t, canceledAtWrite, context.Canceled)
				require.False(t, earlyReceipt)
				require.False(t, closeStarted, "close waits until the write and receipt finish")
				require.Equal(t, map[int64]struct{}{fact.AccountID: {}}, avoid)
			})
		})
	}
}

func TestFirstOutputFollowReceiptWithoutTransferAuthority(t *testing.T) {
	f := selectionNewSessionFixture()
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	state.SetStickyChange(&openAIStickyChange{groupID: 7, session: "session", key: f.binding.key, cache: f.binding,
		expected: StickySessionBinding{AccountID: f.owner.ID, Generation: f.binding.generation}, ttl: time.Hour})
	fact := resilience.OwnerFirstOutputCutoff{GroupID: 7, Session: "session", BindingKey: f.binding.key,
		AccountID: f.owner.ID, Generation: f.binding.generation, AttemptSeq: state.MarkOpsUpstreamSendBoundary(f.owner.ID),
		FiredAt: time.Now(), Effective: time.Second, CanTransfer: false}
	require.True(t, state.RecordOwnerCutoff(fact))
	recordFirstOutputAvoid(ctx, fact)
	avoid, err := f.session.store.SessionAvoid(ctx, 7, firstOutputAvoidSession("session"))
	require.NoError(t, err)
	require.Empty(t, avoid, "an ineligible cutoff must not write even though the binding matches")
	_, confirmed := state.ConfirmedOwnerCutoff()
	require.False(t, confirmed)
	require.True(t, state.ConfirmOwnerAvoid(fact.AttemptSeq, fact.Generation))
	frozen, confirmed := state.ConfirmedOwnerCutoff()
	require.True(t, confirmed)
	require.False(t, frozen.CanTransfer)
	svc := &OpenAIGatewayService{}
	target := f.candidates[0]
	followSelect(svc, ctx, target)
	require.Zero(t, pendingOpenAIStickyChange(ctx).target)
	followDelivery(svc, ctx, target, 0, &OpenAIForwardResult{})
	require.Equal(t, f.owner.ID, f.binding.owner)
	require.Empty(t, f.binding.casKeys)
}

func TestFirstOutputFollowBorrowKeepsTrialLast(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, extraScope, bypass string
		retry                    bool
		wantSelected             int64
	}{
		{"same_request", "", "owner_failed_in_request", true, 2},
		{"next_request", "", "first_output_avoid", false, 2},
		{"strict_overlap", stickyChangeAvoidSession("session"), "owner_excluded", false, 2},
		{"ordinary_overlap", "session", "owner_excluded", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const ownerID, trialID = int64(10), int64(1)
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(ownerID, 5), selectionFixtureAccount(trialID, 1), selectionFixtureAccount(2, 9))
			const key = "openai:session"
			f.cache.sessionBindings[key] = ownerID
			selectionSetTrial(f, trialID, newSelectionTrialLeaseFake())
			selectFor := func(ctx context.Context, excluded map[int64]struct{}) *AccountSelectionResult {
				t.Helper()
				sel, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", excluded, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
				require.NoError(t, err)
				require.NotNil(t, sel)
				return sel
			}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			owner := selectFor(ctx, nil)
			require.Equal(t, ownerID, owner.Account.ID)
			owner.ReleaseFunc()
			change := pendingOpenAIStickyChange(ctx)
			fact := resilience.OwnerFirstOutputCutoff{GroupID: 7, Session: "session", BindingKey: key, AccountID: ownerID, Generation: change.expected.Generation, AttemptSeq: state.MarkOpsUpstreamSendBoundary(ownerID), Effective: time.Second, FiredAt: time.Now(), CanTransfer: change.canTransfer}
			require.True(t, state.RecordOwnerCutoff(fact))
			recordFirstOutputAvoid(ctx, fact)
			_, ok := state.ConfirmedOwnerCutoff()
			require.True(t, ok)
			var excluded map[int64]struct{}
			if tc.retry {
				state.MarkAccountFailed(ownerID)
				excluded = map[int64]struct{}{ownerID: {}}
			} else {
				state = resilience.NewRequestState()
				ctx = resilience.WithRequestState(context.Background(), state)
			}
			if tc.extraScope != "" {
				require.NoError(t, f.svc.resilience.store.AddSessionAvoid(ctx, 7, tc.extraScope, ownerID, time.Minute))
			}
			logs := captureOpenAIObservationLogs(t)
			result := selectFor(ctx, excluded)
			defer result.ReleaseFunc()
			require.Equal(t, tc.wantSelected, result.Account.ID)
			require.Equal(t, tc.extraScope == "session", state.Avoided(ownerID))
			var reasons []string
			for _, row := range schedObsLogRecords(t, logs) {
				if row["msg"] == "openai.sticky_bypass" {
					reasons = append(reasons, fmt.Sprint(row["reason"]))
				}
			}
			require.Equal(t, []string{tc.bypass}, reasons)
			followDelivery(f.svc, ctx, result.Account, time.Millisecond, &OpenAIForwardResult{})
			if tc.retry {
				require.Equal(t, tc.wantSelected, f.cache.sessionBindings[key])
			} else {
				require.Equal(t, ownerID, f.cache.sessionBindings[key])
			}
		})
	}
}

func TestFirstOutputFollowAvoidWriteLog(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, reason string
		hasError     bool
	}{
		{"written", "confirmed", false}, {"disabled", "session_disabled", false},
		{"snapshot", "binding_changed", false}, {"rejected", "rejected", false},
		{"expired", "expired", false}, {"store_error", "error", true}, {"timeout", "timeout", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			ctx := followState(t, f, false)
			state := resilience.StateFrom(ctx)
			fact, _ := state.OwnerCutoff()
			const privateText = "synthetic private store detail"
			switch tc.name {
			case "disabled":
				withResilienceSettings(t, func(s *resilience.Settings) { s.Session.Enabled = false })
			case "snapshot":
				fact.Generation = "another-version"
			case "rejected":
				f.binding.avoidRejected = true
			case "expired":
				fact.FiredAt = time.Now().Add(-time.Hour)
			case "store_error":
				f.binding.avoidErr = errors.New(privateText)
			case "timeout":
				f.binding.avoidErr = fmt.Errorf("%s: %w", privateText, context.DeadlineExceeded)
			}
			logs := captureOpenAIObservationLogs(t)
			recordFirstOutputAvoid(ctx, fact)
			rows := schedObsLogRecords(t, logs)
			require.Len(t, rows, 1)
			require.Equal(t, "sched_quality.first_output_avoid", rows[0]["msg"])
			require.Equal(t, tc.reason, rows[0]["reason"])
			wantWritten := tc.name == "written"
			require.Equal(t, wantWritten, rows[0]["confirmed"])
			class, hasClass := rows[0]["error_class"]
			require.Equal(t, tc.hasError, hasClass)
			if hasClass {
				require.NotEmpty(t, class)
			}
			require.NotContains(t, logs.String(), privateText)
			_, confirmed := state.ConfirmedOwnerCutoff()
			require.Equal(t, wantWritten, confirmed)
			stored, err := f.session.store.SessionAvoid(ctx, 7, firstOutputAvoidSession("session"))
			require.NoError(t, err)
			_, written := stored[f.owner.ID]
			require.Equal(t, wantWritten, written)
		})
	}
}

func TestFirstOutputFollowSwitchDisabledAfterReceipt(t *testing.T) {
	for _, planned := range []bool{false, true} {
		t.Run(fmt.Sprintf("planned=%t", planned), func(t *testing.T) {
			withResilienceSettings(t, nil)
			f, svc := selectionNewSessionFixture(), &OpenAIGatewayService{}
			ctx := followState(t, f, true)
			target := f.candidates[0]
			if planned {
				followSelect(svc, ctx, target)
				require.Equal(t, stickyFollow, pendingOpenAIStickyChange(ctx).kind)
			}
			withResilienceSettings(t, func(s *resilience.Settings) { s.Session.Enabled = false })
			if !planned {
				followSelect(svc, ctx, target)
				require.Zero(t, pendingOpenAIStickyChange(ctx).target)
			}
			followDelivery(svc, ctx, target, 0, &OpenAIForwardResult{})
			require.Equal(t, f.owner.ID, f.binding.owner)
			require.Empty(t, f.binding.casKeys)
		})
	}
}

func TestFirstOutputFollowPreDispatchFailureKeepsTargetAvailable(t *testing.T) {
	withResilienceSettings(t, nil)
	f, svc := selectionNewSessionFixture(), &OpenAIGatewayService{}
	ctx := followState(t, f, true)
	state := resilience.StateFrom(ctx)
	state.SetLastUpstreamFailure(&UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout})
	target := f.candidates[0]
	followSelect(svc, ctx, target)
	late, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(late, http.MethodPost, "https://upstream.invalid", nil)
	require.NoError(t, err)
	var capture *schedAttemptCapture
	_, err = svc.sendSchedHTTP(late, target, req, "", nil, true, &capture)
	var fe *UpstreamFailoverError
	require.ErrorAs(t, err, &fe)
	require.True(t, fe.PreDispatchSelectionInvalidated)
	require.Nil(t, capture)
	logs := captureOpenAIObservationLogs(t)
	FinishOpenAIStickyForward(late, target.ID, nil, err)
	require.Contains(t, logs.String(), `"reason":"pre_dispatch_invalidated"`)
	require.False(t, state.Avoided(target.ID))
	avoid, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, err)
	require.Empty(t, avoid)
	require.Equal(t, f.owner.ID, f.binding.owner)
}

type followWriteBarrier struct {
	OpenAIStickyVersionedCache
	enter  chan string
	resume chan struct{}
}

func (b *followWriteBarrier) CommitSessionBinding(ctx context.Context, g int64, key string, expected StickySessionBinding, id int64, ttl time.Duration, scopes ...string) (bool, error) {
	b.enter <- "commit"
	<-b.resume
	return b.OpenAIStickyVersionedCache.CommitSessionBinding(ctx, g, key, expected, id, ttl, scopes...)
}

func (b *followWriteBarrier) RecordSessionBindingFailure(ctx context.Context, g int64, key, scope string, expected StickySessionBinding, id int64, ttl time.Duration, reset ...time.Time) (bool, error) {
	b.enter <- scope
	<-b.resume
	return b.OpenAIStickyVersionedCache.RecordSessionBindingFailure(ctx, g, key, scope, expected, id, ttl, reset...)
}

func TestFirstOutputFollowTerminalUnlocksBeforeWrite(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, svc := selectionNewSessionFixture(), &OpenAIGatewayService{}
				ctx := followState(t, f, true)
				y := f.candidates[0]
				followSelect(svc, ctx, y)
				change := pendingOpenAIStickyChange(ctx)
				gate := &followWriteBarrier{change.cache, make(chan string), make(chan struct{})}
				change.cache = gate
				result := &OpenAIForwardResult{PartialFailure: partial}
				done := make(chan struct{})
				go func() { followDelivery(svc, ctx, y, time.Millisecond, result); close(done) }()
				op := <-gate.enter
				unlocked := change.mu.TryLock()
				if unlocked {
					change.mu.Unlock()
				}
				repeated := make(chan struct{})
				go func() { FinishOpenAIStickyForward(ctx, y.ID, result, nil); close(repeated) }()
				synctest.Wait()
				var returned bool
				select {
				case <-repeated:
					returned = true
				default:
				}
				close(gate.resume)
				<-done
				<-repeated
				require.True(t, unlocked)
				require.True(t, returned, "duplicate finish returns while the claimed write is blocked")
				if partial {
					require.Equal(t, stickyChangeAvoidSession("session"), op)
					require.Empty(t, f.binding.casKeys)
				} else {
					require.Equal(t, "commit", op)
					require.Len(t, f.binding.casKeys, 1)
				}
			})
		})
	}
}

type followQualityStore struct {
	GatewayCache
	OpenAIQualityRoutingStore
	state *OpenAIQualityState
	err   error
}

func (s *followQualityStore) ReadOpenAIQualityRouting(context.Context, string, int64, []string) (*OpenAIQualityState, []int64, error) {
	return s.state, nil, s.err
}

func TestFirstOutputFollowRechecksTargetBeforeCAS(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct{ name, reason string }{
		{"front", "faster_delivery"}, {"rear", "not_front"}, {"trial", "not_front"}, {"disabled", "target_ineligible"}, {"moved_group", "target_ineligible"},
		{"quality", "quality_owned"}, {"other_instance", "quality_owned"}, {"quality_read_failed", "quality_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			rt := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(1, 2))
			rt.svc.cfg.Gateway.OpenAIQualityRouting.Mode = "enforce"
			store := &followQualityStore{GatewayCache: rt.svc.cache}
			rt.svc.cache = store
			ctx := context.WithValue(withOpenAIQualityRouting(followState(t, f, true)), openAIQualityContextKey{},
				&openAIQualityRequest{group: 7, session: "session", scope: "synthetic", loaded: true})
			target := rt.repo.accounts[1]
			followSelect(rt.svc, ctx, &target)
			require.Equal(t, stickyFollow, pendingOpenAIStickyChange(ctx).kind)
			switch tc.name {
			case "rear":
				rt.view.States[fakeSchedViewKey{7, target.ID, "m"}] = schedquality.StateView{Cache: schedquality.LineView{Identity: schedquality.IdentityRear}}
			case "trial":
				rt.view.States[fakeSchedViewKey{7, target.ID, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Trial: true}}
			case "disabled":
				rt.repo.accounts[1].Schedulable = false
			case "moved_group":
				rt.repo.accounts[1].GroupIDs = []int64{8}
			case "quality":
				q := qualityRequest(ctx)
				q.mu.Lock()
				q.state.Generation = 1
				q.mu.Unlock()
			case "other_instance":
				store.state = &OpenAIQualityState{Generation: 1, Expires: time.Now().Add(time.Hour).UnixMilli()}
			case "quality_read_failed":
				store.err = errors.New("store unavailable")
			}
			logs := captureOpenAIObservationLogs(t)
			followDelivery(rt.svc, ctx, &target, time.Millisecond, &OpenAIForwardResult{})
			var events []map[string]any
			for _, row := range schedObsLogRecords(t, logs) {
				if row["msg"] == "sched_quality.session_switch" {
					events = append(events, row)
				}
			}
			require.Len(t, events, 1)
			require.Equal(t, tc.reason, events[0]["reason"])
			require.Equal(t, true, events[0]["faster_complete"])
			require.Equal(t, tc.name == "front", events[0]["commit_attempted"])
			if tc.name == "front" {
				require.Equal(t, target.ID, f.binding.owner)
			} else {
				require.Equal(t, f.owner.ID, f.binding.owner)
				require.Empty(t, f.binding.casKeys)
			}
			strict, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
			require.NoError(t, err)
			require.Empty(t, strict)
		})
	}
}

func TestFirstOutputFollowProductionTakeoverReader(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"ordinary", "avoided", "read_error", "quality_owned"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 8), selectionFixtureAccount(1, 1))
			f.cache.sessionBindings["openai:session"] = 10
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			if mode == "quality_owned" {
				f.svc.cfg.Gateway.OpenAIQualityRouting.Mode = "enforce"
				ctx = context.WithValue(withOpenAIQualityRouting(ctx), openAIQualityContextKey{}, &openAIQualityRequest{loaded: true, pinned: true, group: 7, session: "session", scope: "synthetic", state: OpenAIQualityState{Generation: 1, Binding: 10, Owner: 10}})
			}
			store := &followScopeReader{ResilienceStore: f.svc.resilience.store}
			f.svc.resilience.store = store
			if mode == "read_error" {
				store.err = errors.New("unavailable")
			}
			if mode == "avoided" || mode == "quality_owned" {
				require.NoError(t, store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 1, time.Minute))
			}
			gid := int64(7)
			f.svc.prepareOpenAIStickyChange(ctx, &gid, "session")
			req := OpenAIAccountScheduleRequest{GroupID: &gid, Platform: PlatformOpenAI, SessionHash: "session", RequestedModel: "m", RequiredCapability: OpenAIEndpointCapabilityResponses, RequiredTransport: OpenAIUpstreamTransportHTTPSSE}
			sel := f.svc.selectSessionOwner(ctx, req, &f.repo.accounts[0], func() ([]Account, error) { return f.repo.accounts, nil })
			if sel != nil {
				defer sel.ReleaseFunc()
			}
			switch mode {
			case "quality_owned":
				require.Zero(t, store.reads)
			case "ordinary":
				require.Equal(t, 1, store.reads)
				require.NotNil(t, sel)
				require.EqualValues(t, 1, sel.Account.ID)
			default:
				require.Equal(t, 1, store.reads)
				require.Nil(t, sel)
			}
			require.EqualValues(t, 10, f.cache.sessionBindings["openai:session"])
		})
	}
}
