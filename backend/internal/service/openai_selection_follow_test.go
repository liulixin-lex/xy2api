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
			svc.planOpenAIFollow(ctx, ptrInt64Selection(7), "session", "m", sel, true)
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
	for _, scenario := range []string{"ordinary_empty", "ordinary_avoided", "quality_owned", "not_injected", "read_error", "migrate_avoided"} {
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
			if scenario == "quality_owned" || scenario == "not_injected" {
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
	for _, mode := range []string{"ordinary", "previous_response", "websocket", "encrypted", "guardian", "child", "manual", "quality", "seedance", "image_intent", "protected_chat"} {
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
			}
		})
	}
}

func TestFirstOutputFollowLegacySelectionKeepsOwner(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"no_request_state", "unversioned_cache"} {
		for _, avoid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/avoid=%t", mode, avoid), func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(11, 2))
				const key = "openai:session"
				f.cache.sessionBindings[key] = 10
				store := &followScopeReader{ResilienceStore: f.svc.resilience.store}
				f.svc.resilience.store = store
				ctx := context.Background()
				if mode == "unversioned_cache" {
					ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
					f.svc.cache = struct{ GatewayCache }{f.cache}
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
				require.Nil(t, pendingOpenAIStickyChange(ctx))
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
				pendingOpenAIStickyChange(ctx).cache = f.binding
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
				<-b.enter
				fact, confirmed := st.ConfirmedOwnerCutoff()
				canceled := transport.Err()
				avoid, err := f.session.store.SessionAvoid(ctx, 7, firstOutputAvoidSession("session"))
				close(b.leave)
				requireFirstOutputFailover(t, <-done)
				require.NoError(t, err)
				require.True(t, confirmed)
				require.ErrorIs(t, canceled, context.Canceled)
				require.Equal(t, map[int64]struct{}{fact.AccountID: {}}, avoid)
			})
		})
	}
}
