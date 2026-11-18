//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type factsUpstream struct {
	HTTPUpstream
	send func(*http.Request, int64) (*http.Response, error)
}

func (u factsUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	return u.send(req, id)
}

func ownerFactsState(ctx context.Context, account *Account) (*resilience.RequestState, context.Context) {
	st := resilience.NewRequestState()
	ctx = resilience.WithRequestState(ctx, st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
	st.SetSession(7, "session", nil)
	st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "public-model", StickyWarm: true, HTTPStreamText: true})
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, Model: "wire-model", Stream: true, Warm: true, AttemptStart: time.Now()})
	st.SetStickyChange(&openAIStickyChange{groupID: 7, session: "session", key: "openai:session", expected: StickySessionBinding{AccountID: account.ID, Generation: "v1"}, canTransfer: true})
	return st, ctx
}

func TestOwnerCutoffAtWinningGuards(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"headers", "late_dispatch", "chat_headers", "messages_late_dispatch", "responses", "passthrough", "chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				late := strings.Contains(route, "late_dispatch")
				header := late || strings.Contains(route, "headers")
				conversion := strings.SplitN(route, "_", 2)[0]
				body := `{"model":"wire-model","input":"hello","stream":true}`
				if conversion == "chat" || conversion == "messages" {
					body = `{"model":"wire-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`
				}
				c, _ := compatStreamContext("/v1/"+route, body)
				account := rawChatCompletionsTestAccount()
				st, ctx := ownerFactsState(c.Request.Context(), account)
				c.Request = c.Request.WithContext(ctx)
				var capture *schedAttemptCapture
				var dispatched time.Time
				wire := make(chan struct{})
				up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
					capture, _ = req.Context().Value(schedHTTPAttemptKey{}).(*schedAttemptCapture)
					if late {
						<-req.Context().Done()
					}
					dispatched = time.Now()
					RecordOpsUpstreamSendBoundary(req.Context(), id)
					if header {
						<-req.Context().Done()
						<-wire
						return nil, req.Context().Err()
					}
					r, w := io.Pipe()
					context.AfterFunc(req.Context(), func() { _ = w.CloseWithError(req.Context().Err()) })
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: r, Request: req}, nil
				}}
				svc := compatStreamService(up, 1, 0, 0)
				done := make(chan error, 1)
				go func() {
					var err error
					switch conversion {
					case "chat", "messages":
						_, err = captureForward(svc, c, account, []byte(body), conversion)
					case "passthrough":
						_, err = svc.forwardOpenAIPassthrough(ctx, c, account, []byte(body), []byte(body), "wire-model", false, nil, true, time.Now())
					default:
						_, err = svc.Forward(ctx, c, account, []byte(body))
					}
					done <- err
				}()
				synctest.Wait()
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				fact, ok := st.OwnerCutoff()
				close(wire)
				requireFirstOutputFailover(t, <-done)
				require.NotNil(t, capture)
				require.True(t, capture.ownerHit)
				require.Equal(t, !late, capture.fullTierFirstOutputTimeout)
				require.Equal(t, !late, ok, "headers must publish even while Do is blocked")
				if ok {
					require.Equal(t, dispatched, fact.DispatchAt)
					require.True(t, fact.CanTransfer)
					require.EqualValues(t, 1, fact.AttemptSeq)
					require.Equal(t, "wire-model", fact.Model)
					require.Equal(t, "openai:session", fact.BindingKey)
					require.Equal(t, "v1", fact.Generation)
					require.Equal(t, fact.Effective, fact.FiredAt.Sub(fact.DispatchAt))
					_, confirmed := st.ConfirmedOwnerCutoff()
					require.False(t, confirmed)
				}
			})
		})
	}
}

func TestOwnerCutoffRejectsUnrelatedAttempts(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, scenario := range []string{"same_account_replay", "late_callback", "capacity_borrow", "client_cancel", "valid_output", "constructor", "short_budget", "missing_snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			account := rawChatCompletionsTestAccount()
			st, ctx := ownerFactsState(client, account)
			sel, _ := st.SchedSelection()
			if scenario == "capacity_borrow" {
				sel.StickyWarm = OpenAIAttemptWarm(OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerSessionSticky, StickySessionHit: true}, &AccountSelectionResult{Account: account, StickyCapacitySpillover: true}, false)
			}
			st.SetSchedSelection(sel)
			if scenario == "missing_snapshot" {
				st.SetStickyChange(nil)
			}
			svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
				RecordOpsUpstreamSendBoundary(req.Context(), id)
				return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
			}}, 0, 0, 0)
			svc.schedAttemptRecorder = &captureServiceRecorder{}
			send := func() *schedAttemptCapture {
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.invalid/responses", strings.NewReader("{}"))
				require.NoError(t, err)
				var a *schedAttemptCapture
				resp, err := svc.sendSchedHTTP(ctx, account, req, "", nil, true, &a)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.NotNil(t, a)
				return a
			}
			a := send()
			plan := st.PlanAttemptTimeout(time.Second, false, time.Now())
			switch scenario {
			case "same_account_replay":
				a = send()
			case "late_callback":
				_ = send()
			case "client_cancel":
				cancel()
			case "valid_output":
				a.MarkOutput(OutputValid, time.Now())
			case "short_budget":
				plan = st.PlanAttemptTimeout(time.Hour, false, time.Now())
			case "constructor":
				c, _ := compatStreamContext("/v1/responses", "")
				c.Request = c.Request.WithContext(ctx)
				_ = svc.newOpenAIFirstOutputTimeoutError(ctx, c, account, nil, "", plan.Start(), sel.Model, "", plan.Effective(), "response_headers", nil, plan)
			}
			if scenario != "constructor" {
				_, _ = claimOpenAIFirstOutputCutoff(a, plan, plan.Start().Add(plan.Effective()), nil)
			}
			fact, ok := st.OwnerCutoff()
			require.Equal(t, scenario == "missing_snapshot", ok)
			require.False(t, fact.CanTransfer)
			require.Equal(t, scenario != "same_account_replay" && scenario != "capacity_borrow", a.ownerHit)
			require.Equal(t, scenario == "capacity_borrow" || scenario == "same_account_replay" || scenario == "missing_snapshot", a.fullTierFirstOutputTimeout)
		})
	}
}

func TestTargetFirstValidClockBelongsToFinalSend(t *testing.T) {
	account := rawChatCompletionsTestAccount()
	st, ctx := ownerFactsState(context.Background(), account)
	c := &openAIStickyChange{target: account.ID}
	st.SetStickyChange(c)
	svc := &OpenAIGatewayService{}
	a := svc.beginSchedAttempt(ctx, account, nil, 0, true)
	a.MarkOutput(OutputFirstEvent, a.started)
	a.MarkOutput(OutputValid, a.started.Add(123*time.Nanosecond))
	a.MarkOutput(OutputValid, a.started.Add(time.Second))
	a.finish(nil, nil)
	require.True(t, c.targetUpstreamFirstValidKnown)
	require.Equal(t, 123*time.Nanosecond, c.targetUpstreamFirstValidDelay)
	b := svc.beginSchedAttempt(ctx, account, nil, 0, true)
	require.False(t, c.targetUpstreamFirstValidKnown)
	noteOpenAIStickyAttemptOutcome(a, true)
	require.False(t, c.targetUpstreamFirstValidKnown)
	b.MarkOutput(OutputValid, b.started)
	b.finish(nil, nil)
	require.True(t, c.targetUpstreamFirstValidKnown)
	require.Zero(t, c.targetUpstreamFirstValidDelay)
}

func TestTransferEligibilityFollowsFinalSelection(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(11, 2))
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	f.cache.sessionBindings["openai:session"] = 10
	selectAccount := func(ctx context.Context, session string, excluded map[int64]struct{}) *AccountSelectionResult {
		selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, "m", excluded, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
		require.NoError(t, err)
		require.NotNil(t, selected)
		t.Cleanup(selected.ReleaseFunc)
		return selected
	}
	_ = selectAccount(ctx, "session", nil)
	first := pendingOpenAIStickyChange(ctx)
	require.NotNil(t, first)
	require.True(t, first.canTransfer)
	_ = selectAccount(withOpenAINoStickyMigration(ctx), "session", nil)
	require.False(t, pendingOpenAIStickyChange(ctx).canTransfer)
	_ = selectAccount(ctx, "session", nil)
	require.True(t, pendingOpenAIStickyChange(ctx).canTransfer)
	_ = selectAccount(ctx, "session", map[int64]struct{}{10: {}})
	require.False(t, pendingOpenAIStickyChange(ctx).canTransfer)
	target := selectAccount(ctx, "new-session", nil)
	final := pendingOpenAIStickyChange(ctx)
	require.NotSame(t, first, final)
	require.Equal(t, target.Account.ID, final.target, "the legacy replacement plan installed the final change")
	require.False(t, final.canTransfer)
}

func TestTransferEligibilityProtection(t *testing.T) {
	account := rawChatCompletionsTestAccount()
	for _, scenario := range []string{"ordinary", "missing_generation", "snapshot_error", "wrong_group", "wrong_session", "wrong_owner", "guardian", "child", "quality", "not_ordinary"} {
		t.Run(scenario, func(t *testing.T) {
			svc := compatStreamService(nil, 0, 0, 0)
			svc.cfg.Gateway.OpenAIQualityRouting.Mode = "enforce"
			st, ctx := ownerFactsState(context.Background(), account)
			change := pendingOpenAIStickyChange(ctx)
			switch scenario {
			case "missing_generation":
				change.expected.Generation = ""
			case "snapshot_error":
				change.skipCommit = true
			case "wrong_group":
				change.groupID++
			case "wrong_session":
				change.session = "other"
			case "wrong_owner":
				change.expected.AccountID++
			case "guardian":
				ctx = context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{currentSessionHash: "session"})
			case "child":
				ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: "child", preserveBinding: true})
			case "quality":
				ctx = context.WithValue(withOpenAIQualityRouting(ctx), openAIQualityContextKey{}, &openAIQualityRequest{session: "session", group: 7, loaded: true, state: OpenAIQualityState{Generation: 1}})
			}
			svc.freezeOpenAITransferEligibility(ctx, ptrInt64Selection(7), "session", &AccountSelectionResult{Account: account}, scenario != "not_ordinary")
			require.Equal(t, scenario == "ordinary", change.canTransfer)
			a := svc.newSchedAttemptCapture(ctx, account, nil, true, time.Time{}, &openAIFirstOutputHeaderGuard{})
			require.NotNil(t, a)
			require.True(t, a.factsOnly)
			st.MarkOpsUpstreamSendBoundary(account.ID)
			a.markHTTPDispatch(1)
			require.True(t, a.ownerHit)
			require.Equal(t, scenario == "ordinary", a.dispatch.CanTransfer)
		})
	}
}

func TestOwnerCutoffRepeatedSendBoundary(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, phase := range []string{"first", "explicit_resend", "next_connection"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account := rawChatCompletionsTestAccount()
				body := []byte(`{"model":"wire-model","input":"hello","stream":true}`)
				c, _ := compatStreamContext("/v1/responses", string(body))
				st, ctx := ownerFactsState(c.Request.Context(), account)
				c.Request = c.Request.WithContext(ctx)
				var a *schedAttemptCapture
				up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
					a = req.Context().Value(schedHTTPAttemptKey{}).(*schedAttemptCapture)
					RecordOpsUpstreamSendBoundary(req.Context(), id)
					a.markHTTPDispatch(a.dispatch.AttemptSeq) // repeat notification of the same send
					trace := httptrace.ContextClientTrace(req.Context())
					if trace.GetConn != nil {
						trace.GetConn("first.invalid:443")
					}
					if phase == "next_connection" && trace.GetConn != nil {
						trace.GetConn("next.invalid:443")
					}
					if phase == "explicit_resend" {
						RecordOpsUpstreamSendBoundary(req.Context(), id)
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				}}
				svc := compatStreamService(up, 1, 0, 0)
				_, err := svc.Forward(ctx, c, account, body)
				requireFirstOutputFailover(t, err)
				_, ok := st.OwnerCutoff()
				require.Equal(t, phase == "first", ok)
				require.Equal(t, phase == "first", a.ownerHit)
				require.Equal(t, phase == "first", a.fullTierFirstOutputTimeout)
			})
		})
	}
}

func TestOwnerCutoffBeforeCompatReaderReturns(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"chat", "messages"} {
		for _, mode := range []string{"delayed_read", "cancel_after_fire"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					account := rawChatCompletionsTestAccount()
					c, _ := compatStreamContext("/v1/"+route, compatMessagesStreamBody)
					client, cancel := context.WithCancel(c.Request.Context())
					defer cancel()
					st, ctx := ownerFactsState(client, account)
					c.Request = c.Request.WithContext(ctx)
					release := make(chan struct{})
					guardCanceled := make(chan struct{})
					up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
						RecordOpsUpstreamSendBoundary(req.Context(), id)
						reader, writer := io.Pipe()
						go func() {
							<-req.Context().Done()
							close(guardCanceled)
							<-release
							_ = writer.CloseWithError(req.Context().Err())
						}()
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader, Request: req}, nil
					}}
					svc := compatStreamService(up, 1, 0, 0)
					done := make(chan error, 1)
					go func() {
						_, err := captureForward(svc, c, account, []byte(compatMessagesStreamBody), route)
						done <- err
					}()
					<-guardCanceled
					synctest.Wait()
					_, atFire := st.OwnerCutoff()
					if mode == "cancel_after_fire" {
						cancel()
					}
					close(release)
					err := <-done
					_, atReturn := st.OwnerCutoff()
					require.True(t, atFire, "timer must publish before the reader releases")
					require.True(t, atReturn)
					if mode == "delayed_read" {
						requireFirstOutputFailover(t, err)
					} else {
						require.Error(t, err)
					}
				})
			})
		}
	}
}

type factsBlockingCloser struct {
	io.ReadCloser
	closed atomic.Bool
	once   sync.Once
	enter  chan struct{}
	leave  chan struct{}
}

func (b *factsBlockingCloser) Close() error {
	b.once.Do(func() {
		close(b.enter)
		<-b.leave
		_ = b.ReadCloser.Close()
		b.closed.Store(true)
	})
	return nil
}

func TestOwnerCutoffCancelsBeforeBlockingClose(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"responses", "passthrough"} {
		t.Run(route, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := []byte(`{"model":"wire-model","input":"hello","stream":true}`)
				c, _ := compatStreamContext("/v1/responses", string(body))
				account := rawChatCompletionsTestAccount()
				st, ctx := ownerFactsState(c.Request.Context(), account)
				r, w := io.Pipe()
				defer w.Close()
				b := &factsBlockingCloser{ReadCloser: r, enter: make(chan struct{}), leave: make(chan struct{})}
				var upstream context.Context
				type notification struct {
					fact          resilience.OwnerFirstOutputCutoff
					delay         time.Duration
					upstreamErr   error
					closeFinished bool
				}
				var received []notification
				ctx = context.WithValue(ctx, ownerCutoffObserverKey{}, func(fact resilience.OwnerFirstOutputCutoff) {
					received = append(received, notification{fact: fact, delay: time.Since(fact.FiredAt), upstreamErr: upstream.Err(), closeFinished: b.closed.Load()})
				})
				c.Request = c.Request.WithContext(ctx)
				up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
					upstream = req.Context()
					RecordOpsUpstreamSendBoundary(req.Context(), id)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: b, Request: req}, nil
				}}
				svc := compatStreamService(up, 1, 0, 0)
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
				fact, frozen := st.OwnerCutoff()
				// A synchronous Close in the cancellation callback would consume the
				// whole avoidance window before reaching the notification.
				time.Sleep(601 * time.Second)
				close(b.leave)
				requireFirstOutputFailover(t, <-done)
				require.True(t, frozen)
				require.Len(t, received, 1)
				note := received[0]
				require.Equal(t, fact, note.fact)
				require.ErrorIs(t, note.upstreamErr, context.Canceled)
				assert.False(t, note.closeFinished, "follow notification arrived after Close finished")
				assert.Zero(t, note.delay, "follow notification delay since the winning deadline")

			})
		})
	}
}

func TestOwnerCutoffPassthroughDisconnectDuringClose(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		body := []byte(`{"model":"wire-model","input":"hello","stream":true}`)
		account := rawChatCompletionsTestAccount()
		c, _ := compatStreamContext("/v1/responses", string(body))
		client, cancel := context.WithCancel(c.Request.Context())
		defer cancel()
		st, ctx := ownerFactsState(client, account)
		c.Request = c.Request.WithContext(ctx)
		r, w := io.Pipe()
		defer w.Close()
		b := &factsBlockingCloser{ReadCloser: r, enter: make(chan struct{}), leave: make(chan struct{})}
		up := factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
			RecordOpsUpstreamSendBoundary(req.Context(), id)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: b, Request: req}, nil
		}}
		done := make(chan error, 1)
		go func() {
			_, err := compatStreamService(up, 1, 0, 0).forwardOpenAIPassthrough(ctx, c, account, body, body, "wire-model", false, nil, true, time.Now())
			done <- err
		}()
		<-b.enter
		before, ok := st.OwnerCutoff()
		cancel()
		close(b.leave)
		require.EqualError(t, <-done, "stream usage incomplete: client disconnected before output")
		require.True(t, IsResponseCommitted(c))
		after, preserved := st.OwnerCutoff()
		require.True(t, ok && preserved)
		require.Equal(t, before, after)
	})
}

func TestOwnerCutoffLightweightCapturePreservesDiagnosticClock(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		account := rawChatCompletionsTestAccount()
		c, _ := compatStreamContext("/v1/responses", "")
		st, ctx := ownerFactsState(c.Request.Context(), account)
		c.Request = c.Request.WithContext(ctx)
		start := time.Now()
		plan := st.PlanAttemptTimeout(20*time.Second, false, start)
		upstream, g := newOpenAIFirstOutputHeaderGuard(ctx, func() {}, start.Add(plan.Effective()), plan)
		defer g.close()
		time.Sleep(3 * time.Second) // construction after the timer was armed
		req, err := http.NewRequestWithContext(upstream, http.MethodPost, "https://upstream.invalid/responses", nil)
		require.NoError(t, err)
		svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
			RecordOpsUpstreamSendBoundary(req.Context(), id)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}}, 1, 0, 0)
		var a *schedAttemptCapture
		_, err = svc.sendSchedHTTP(ctx, account, req, "", nil, true, &a)
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, g.stopHeaderWait())
		require.True(t, a.factsOnly)
		svc.newOpenAIFirstOutputTimeoutError(ctx, c, account, nil, "", start, "wire-model", "", plan.Effective(), "response_headers", nil, plan)
		raw, _ := c.Get(OpsUpstreamErrorsKey)
		require.EqualValues(t, 20000, raw.([]*OpsUpstreamErrorEvent)[0].AttemptElapsedMs)
		fact, ok := st.OwnerCutoff()
		require.True(t, ok)
		require.Equal(t, 3*time.Second, fact.DispatchAt.Sub(start))
		require.Equal(t, 17*time.Second, fact.FiredAt.Sub(fact.DispatchAt))
	})
}

func TestOwnerCutoffGuardlessRawChatBypass(t *testing.T) {
	withResilienceSettings(t, nil)
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
	c, _ := compatStreamContext("/v1/chat/completions", compatChatStreamBody)
	st, ctx := ownerFactsState(c.Request.Context(), account)
	c.Request = c.Request.WithContext(ctx)
	var a *schedAttemptCapture
	var guard bool
	var path string
	svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
		path = req.URL.Path
		a, _ = req.Context().Value(schedHTTPAttemptKey{}).(*schedAttemptCapture)
		guard = req.Context().Value(openAIHeaderGuardKey{}) != nil
		RecordOpsUpstreamSendBoundary(req.Context(), id)
		wire := "data: " + `{"id":"x","choices":[{"delta":{"content":"hi"},"index":0,"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire)), Request: req}, nil
	}}, 1, 0, 0)
	_, err := captureForward(svc, c, account, []byte(compatChatStreamBody), "chat")
	require.NoError(t, err)
	require.Equal(t, "/v1/chat/completions", path)
	require.False(t, guard)
	require.False(t, st.GatewayRequestEnabled())
	require.Nil(t, a, "guardless stream cannot produce a cutoff for follow")
}

func TestOwnerCutoffUnqualifiedGuardAllocations(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"cold", "wrong_account", "sent"} {
		t.Run(mode, func(t *testing.T) {
			account := rawChatCompletionsTestAccount()
			st, ctx := ownerFactsState(context.Background(), account)
			sel, _ := st.SchedSelection()
			switch mode {
			case "cold":
				sel.StickyWarm = false
			case "wrong_account":
				sel.AccountID++
			case "sent":
				st.MarkOpsUpstreamSendBoundary(account.ID)
			}
			st.SetSchedSelection(sel)
			guard := &openAIFirstOutputHeaderGuard{}
			svc := &OpenAIGatewayService{}
			var got *schedAttemptCapture
			allocs := testing.AllocsPerRun(100, func() { got = svc.newSchedAttemptCapture(ctx, account, nil, true, time.Time{}, guard) })
			require.Nil(t, got)
			require.Zero(t, allocs)
		})
	}
}

func TestOwnerCutoffFiredBeforeDispatch(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		account := rawChatCompletionsTestAccount()
		st, ctx := ownerFactsState(context.Background(), account)
		plan := st.PlanAttemptTimeout(time.Second, false, time.Now())
		svc := &OpenAIGatewayService{schedAttemptRecorder: &captureServiceRecorder{}}
		a := svc.beginSchedAttempt(ctx, account, nil, 0, true)
		time.Sleep(plan.Effective())
		firedAt := time.Now()
		time.Sleep(time.Nanosecond)
		a.markHTTPDispatch(st.MarkOpsUpstreamSendBoundary(account.ID))
		require.True(t, a.ownerHit)
		_, published := claimOpenAIFirstOutputCutoff(a, plan, firedAt, nil)
		require.False(t, published)
		require.False(t, a.fullTierFirstOutputTimeout)
	})
}

func TestOwnerCutoffCompatReaderExclusions(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"messages", "chat"} {
		for _, mode := range []string{"held_prelude", "disconnected"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					account := rawChatCompletionsTestAccount()
					c, _ := compatStreamContext("/v1/"+route, compatMessagesStreamBody)
					st, ctx := ownerFactsState(c.Request.Context(), account)
					c.Request = c.Request.WithContext(ctx)
					keepalive := 0
					before := keepaliveStallCreated
					var writer *openAICompatFailingWriter
					if mode == "disconnected" {
						keepalive = 1
						before = ""
						writer = &openAICompatFailingWriter{ResponseWriter: c.Writer}
						c.Writer = writer
					}
					up := &silentUpstream{headers: true, before: before}
					var a *schedAttemptCapture
					svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
						a = req.Context().Value(schedHTTPAttemptKey{}).(*schedAttemptCapture)
						RecordOpsUpstreamSendBoundary(req.Context(), id)
						return up.Do(req, "", id, 1)
					}}, 1, 30, keepalive)
					_, err := captureForward(svc, c, account, []byte(compatMessagesStreamBody), route)
					if writer != nil {
						require.Equal(t, 1, writer.failedWrites)
						require.Error(t, err)
						require.NoError(t, ctx.Err())
					} else {
						requireFirstOutputFailover(t, err)
						require.True(t, a.holdingCompatPrelude)
					}
					require.True(t, a.ownerHit)
					require.False(t, a.fullTierFirstOutputTimeout)
					_, ok := st.OwnerCutoff()
					require.False(t, ok)
				})
			})
		}
	}
}

func TestOwnerCutoffRequiresMatchingActiveTimer(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"current", "different_handle", "different_account", "no_text_budget"} {
		t.Run(mode, func(t *testing.T) {
			account := rawChatCompletionsTestAccount()
			st, ctx := ownerFactsState(context.Background(), account)
			a := (&OpenAIGatewayService{schedAttemptRecorder: &captureServiceRecorder{}}).beginSchedAttempt(ctx, account, nil, 0, true)
			a.markHTTPDispatch(st.MarkOpsUpstreamSendBoundary(account.ID))
			require.True(t, a.ownerHit)
			if mode == "different_account" {
				profile := st.AttemptProfile()
				profile.AccountID++
				st.SetAttemptProfile(profile)
			}
			plan := st.PlanAttemptTimeout(time.Second, false, time.Now())
			if mode == "different_handle" {
				st.SetCurrentAttempt(&schedAttemptCapture{accountID: account.ID})
			}
			if mode == "no_text_budget" {
				st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindImage, time.Now()))
			}
			_, ok := claimOpenAIFirstOutputCutoff(a, plan, plan.Start().Add(plan.Effective()), nil)
			require.Equal(t, mode == "current", ok)
			require.Equal(t, ok, a.fullTierFirstOutputTimeout)
		})
	}
}
