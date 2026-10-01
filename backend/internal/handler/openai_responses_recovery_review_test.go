package handler

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestReviewNativeOuterTerminalDeliveredOnceAndUsagePreserved(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, managed := range []bool{false, true} {
			for _, status := range []string{"failed", "incomplete", "cancelled", "bare_then_failed"} {
				t.Run(fmt.Sprintf("ws=%v/managed=%v/%s", ws, managed, status), func(t *testing.T) {
					f := newNativeRecoveryFixtureOptions(t, "", nativeRecoveryOptions{webSocket: ws, terminalStatus: status})
					f.server.Client().Timeout = 4 * time.Second
					f.emitNext()
					f.finishNext()
					key := ""
					if managed {
						key = "terminal-once"
					}
					code, body, _ := postReviewNative(t, f, fmt.Sprintf(`{"model":"gpt-5.6-sol","input":"terminal fixture","stream":true,"background":%v}`, managed && !ws), key)
					require.Equal(t, 200, code, string(body))
					terminals := []string{}
					for _, line := range strings.Split(string(body), "\n") {
						if !strings.HasPrefix(line, "data:") {
							continue
						}
						typ := gjson.Get(strings.TrimSpace(strings.TrimPrefix(line, "data:")), "type").String()
						if typ == "response.failed" || typ == "response.incomplete" || typ == "response.cancelled" || typ == "response.completed" {
							terminals = append(terminals, typ)
						}
					}
					wanted := status
					if status == "bare_then_failed" {
						wanted = "failed"
					}
					require.Equal(t, []string{"response." + wanted}, terminals, string(body))
					require.NotContains(t, string(body), "Upstream request failed")
					if ws {
						require.Equal(t, int64(1), f.wsCreateCount.Load(), "fixture must use real WebSocket upstream")
					}
					select {
					case usage := <-f.usage:
						require.Equal(t, 4, usage.InputTokens)
						require.Equal(t, 1, usage.OutputTokens)
					case <-time.After(time.Second):
						t.Fatal("verified failed/partial/cancelled terminal usage missing")
					}
					require.Equal(t, int64(1), f.createCount.Load())
					require.Eventually(t, func() bool { return atomic.LoadInt32(&f.slots.releaseUserCalled) == 1 }, time.Second, time.Millisecond)
					select {
					case <-f.usage:
						t.Fatal("terminal billed twice")
					default:
					}
				})
			}
		}
	}
}

func TestReviewNativeSSELateProofCancelsOriginalWithoutRevival(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%v", shutdown), func(t *testing.T) {
			f := newNativeRecoveryFixture(t)
			requestBody := `{"model":"gpt-5.6-sol","input":"SSE acceptance race","background":true,"stream":true}`
			turn, _, err := f.handler.NativeResponseManager().Create(responseturn.CreateOptions{Scope: f.scope, Body: []byte(requestBody), ControlContext: context.Background()})
			require.NoError(t, err)
			upstream, err := f.upstream.Client().Post(f.upstream.URL+"/v1/responses", "application/json", strings.NewReader(requestBody))
			require.NoError(t, err)
			frame := readNativeFrame(t, bufio.NewReader(upstream.Body))
			_ = upstream.Body.Close()
			account := &service.Account{ID: 74, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-secret", "base_url": f.upstream.URL}, Extra: map[string]any{"openai_responses_supported": true}}
			execution := &nativeResponseExecution{turn: turn, accountID: 74, account: account, accountEligible: true, recoveryRequested: true}
			f.handler.nativeResponseExecutions.Store(turn.ID(), execution)
			defer f.handler.nativeResponseExecutions.Delete(turn.ID())
			wanted := responseturn.ReasonClientDetached
			if shutdown {
				wanted = responseturn.ReasonAdminCancel
				require.NoError(t, f.handler.NativeResponseManager().Close())
			} else {
				turn.Cancel(wanted)
			}
			writer := newNativeJournalWriter(execution, turn.Context(), f.handler)
			writer.Header().Set("Content-Type", "text/event-stream")
			_, err = writer.Write([]byte(frame))
			require.Error(t, err)
			f.handler.cancelAcceptedNativeResponse(turn)
			f.handler.cancelAcceptedNativeResponse(turn)
			snapshot := turn.Snapshot()
			require.Equal(t, responseturn.StateCancelled, snapshot.State)
			require.Equal(t, wanted, snapshot.Reason)
			require.False(t, execution.recoveryConfirmed)
			require.False(t, snapshot.Recoverable)
			require.Zero(t, snapshot.JournalEvents)
			require.Equal(t, "resp_fixture", snapshot.UpstreamResponseID)
			require.Equal(t, int64(74), snapshot.OwnerAccountID)
			require.Equal(t, int64(1), f.createCount.Load())
			require.Equal(t, int64(1), f.cancelCount.Load())
		})
	}
}

func TestReviewNativeJSONProtocolTerminalState(t *testing.T) {
	for _, status := range []responseturn.State{responseturn.StateCompleted, responseturn.StateFailed, responseturn.StatePartial, responseturn.StateCancelled} {
		t.Run(string(status), func(t *testing.T) {
			manager := responseturn.NewManager(responseturn.Config{DisableBackground: true})
			defer func() { require.NoError(t, manager.Close()) }()
			turn, _, err := manager.Create(responseturn.CreateOptions{Scope: responseturn.Scope{UserID: 1, APIKeyID: 2, GroupID: 3, Interface: "responses"}, Body: []byte(`{"model":"fixture","stream":false}`), ControlContext: context.Background()})
			require.NoError(t, err)
			writer := newNativeJournalWriter(&nativeResponseExecution{turn: turn, accountID: 4}, turn.Context(), nil)
			writer.Header().Set("Content-Type", "application/json")
			_, err = fmt.Fprintf(writer, `{"id":"resp_status","status":%q,"model":"fixture","output":[]}`, status)
			require.NoError(t, err)
			writer.finish()
			require.Equal(t, status, turn.Snapshot().State, "HTTP 200 is not a protocol terminal state")
		})
	}
}

func TestReviewNativeBackgroundAcceptedAfterCancellationStillCancelsOriginal(t *testing.T) {
	for _, reason := range []string{"client_detached", "admin_cancel", "shutdown"} {
		t.Run(reason, func(t *testing.T) {
			f := newNativeRecoveryFixture(t, "background")
			requestBody := []byte(`{"model":"gpt-5.6-sol","input":"accept race","background":true,"stream":false}`)
			turn, _, err := f.handler.NativeResponseManager().Create(responseturn.CreateOptions{Scope: f.scope, Body: requestBody, ControlContext: context.Background()})
			require.NoError(t, err)
			upstream, err := f.upstream.Client().Post(f.upstream.URL+"/v1/responses", "application/json", strings.NewReader(string(requestBody)))
			require.NoError(t, err)
			body, err := io.ReadAll(upstream.Body)
			require.NoError(t, err)
			_ = upstream.Body.Close()
			require.Equal(t, 200, upstream.StatusCode)
			account := &service.Account{ID: 74, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-secret", "base_url": f.upstream.URL}, Extra: map[string]any{"openai_responses_supported": true}}
			execution := &nativeResponseExecution{turn: turn, accountID: 74, account: account, backgroundRequested: true}
			f.handler.nativeResponseExecutions.Store(turn.ID(), execution)
			defer f.handler.nativeResponseExecutions.Delete(turn.ID())
			writer := newNativeJournalWriter(execution, turn.Context(), f.handler)
			writer.Header().Set("Content-Type", "application/json")
			_, err = writer.Write(body)
			require.NoError(t, err)
			wanted := responseturn.Reason(reason)
			if reason == "shutdown" {
				wanted = responseturn.ReasonAdminCancel
				require.NoError(t, f.handler.NativeResponseManager().Close())
			} else {
				turn.Cancel(wanted)
			}
			before := turn.Snapshot()
			worker, _ := gin.CreateTestContext(httptest.NewRecorder())
			worker.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(turn.Context())
			worker.Set(nativeResponseExecutionKey, execution)
			worker.Writer = writer
			result, err := f.handler.completeNativeBackgroundResponse(worker, account, &service.OpenAIForwardResult{RequestID: "accept-race-original"})
			require.Error(t, err)
			require.NotNil(t, result, "verified cancelled usage must survive the acceptance race")
			require.Equal(t, 4, result.Usage.InputTokens)
			require.Equal(t, 1, result.Usage.OutputTokens)
			require.Equal(t, "accept-race-original", result.RequestID)
			after := turn.Snapshot()
			require.Equal(t, responseturn.StateCancelled, after.State)
			require.Equal(t, wanted, after.Reason)
			require.Equal(t, before.FinishedAt, after.FinishedAt)
			require.Equal(t, "resp_fixture", after.UpstreamResponseID)
			require.Equal(t, int64(74), after.OwnerAccountID)
			require.Equal(t, int64(1), f.createCount.Load())
			require.Equal(t, int64(1), f.cancelCount.Load())
		})
	}
}

func TestReviewNativeBackgroundNoKeyStoreFalseUsesOriginalDeadline(t *testing.T) {
	f := newNativeRecoveryFixtureOptions(t, "background", nativeRecoveryOptions{executionTimeout: 250 * time.Millisecond})
	f.server.Client().Timeout = time.Second
	status, body, _ := postReviewNative(t, f, `{"model":"gpt-5.6-sol","input":"no key deadline","background":true,"stream":false,"store":false}`, "")
	require.Equal(t, 200, status, string(body))
	turn := f.turn(t)
	deadline, ok := turn.Context().Deadline()
	require.True(t, ok)
	require.Less(t, time.Until(deadline), 300*time.Millisecond)
	require.Eventually(t, func() bool {
		return turn.Snapshot().State == responseturn.StateCancelled && turn.Snapshot().CancelConfirmed
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, responseturn.ReasonDeadline, turn.Snapshot().Reason)
	require.Empty(t, turn.Snapshot().Result)
	require.Equal(t, int64(1), f.createCount.Load())
	require.Equal(t, int64(1), f.cancelCount.Load())
}

func postReviewNative(t *testing.T, f *nativeRecoveryFixture, body, key string) (int, []byte, bool) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	var reused atomic.Bool
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}))
	response, err := f.server.Client().Do(request)
	require.NoError(t, err)
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	_ = response.Body.Close()
	return response.StatusCode, data, reused.Load()
}

func TestReviewNativeBackgroundKeepaliveReturnsWhileCapacityRetained(t *testing.T) {
	f := newNativeRecoveryFixture(t, "background")
	f.server.Client().Timeout = time.Second
	body := `{"model":"gpt-5.6-sol","input":"keepalive fixture","background":true,"stream":false}`
	start := time.Now()
	status, data, _ := postReviewNative(t, f, body, "keepalive")
	require.Equal(t, 200, status, string(data))
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, "queued", gjson.GetBytes(data, "status").String())
	status, data, reused := postReviewNative(t, f, body, "keepalive")
	require.True(t, reused, "the second request must use the same actual HTTP connection")
	require.Equal(t, 409, status, string(data))
	require.Eventually(t, func() bool { return f.pollCount.Load() > 0 }, 2*time.Second, time.Millisecond)
	require.Zero(t, atomic.LoadInt32(&f.slots.releaseUserCalled))
	require.Zero(t, atomic.LoadInt32(&f.slots.releaseAccountCalled))
	require.Equal(t, int64(1), f.createCount.Load())
	f.finishNext()
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&f.slots.releaseUserCalled) == 1 && atomic.LoadInt32(&f.slots.releaseAccountCalled) == 1
	}, 2*time.Second, time.Millisecond)
	t.Log("same HTTP/1.1 connection: queued returned; duplicate=409; original user/account slots retained through polling; upstream create=1")
}

func TestReviewNativeBackgroundCancelDoesNotReplayQueuedOrReleaseEarly(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	defer unblock()
	f := newNativeRecoveryFixtureOptions(t, "background", nativeRecoveryOptions{cancelGate: gate})
	f.server.Client().Timeout = time.Second
	body := `{"model":"gpt-5.6-sol","input":"cancel fixture","background":true,"stream":false}`
	status, _, _ := postReviewNative(t, f, body, "cancel-replay")
	require.Equal(t, 200, status)
	turn := f.turn(t)
	cancelled, err := f.server.Client().Post(f.server.URL+"/v1/responses/resp_fixture/cancel", "application/json", nil)
	require.NoError(t, err)
	data, err := io.ReadAll(cancelled.Body)
	require.NoError(t, err)
	_ = cancelled.Body.Close()
	require.Equal(t, 200, cancelled.StatusCode, string(data))
	select {
	case <-turn.Context().Done():
	default:
		t.Fatal("cancel did not synchronously end the execution context")
	}
	require.False(t, turn.Snapshot().CancelConfirmed)
	require.Eventually(t, func() bool { return f.cancelCount.Load() == 1 }, time.Second, time.Millisecond)
	status, data, _ = postReviewNative(t, f, body, "cancel-replay")
	require.Equal(t, 200, status, string(data))
	require.Equal(t, "cancelled", gjson.GetBytes(data, "status").String())
	require.Equal(t, "upstream_cancel_unconfirmed", gjson.GetBytes(data, "error.code").String())
	require.Zero(t, atomic.LoadInt32(&f.slots.releaseUserCalled), "bounded cancel cleanup still owns user capacity")
	require.Zero(t, atomic.LoadInt32(&f.slots.releaseAccountCalled), "bounded cancel cleanup still owns account capacity")
	unblock()
	require.Eventually(t, func() bool {
		return turn.Snapshot().CancelConfirmed && atomic.LoadInt32(&f.slots.releaseAccountCalled) == 1
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, int64(1), f.createCount.Load())
}

func TestReviewNativeSSEShutdownWaitsForConcurrentCancel(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	defer unblock()
	f := newNativeRecoveryFixtureOptions(t, "", nativeRecoveryOptions{cancelGate: gate})
	f.emitNext() // Reach the first-answer commit boundary before testing shutdown.
	f.server.Client().Timeout = 2 * time.Second
	response := f.create(t, `{"model":"gpt-5.6-sol","input":"SSE shutdown fixture","background":true,"stream":true}`)
	defer func() { _ = response.Body.Close() }()
	require.Contains(t, readNativeFrame(t, bufio.NewReader(response.Body)), "response.created")
	turn := f.turn(t)
	turn.Cancel(responseturn.ReasonAdminCancel)
	require.Eventually(t, func() bool { return f.cancelCount.Load() == 1 }, time.Second, time.Millisecond)
	secondCancelDone := make(chan struct{})
	go func() { f.handler.cancelAcceptedNativeResponse(turn); close(secondCancelDone) }()
	select {
	case <-secondCancelDone:
		t.Fatal("concurrent cleanup returned while the original upstream cancel POST was still blocked")
	case <-time.After(30 * time.Millisecond):
	}
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	require.ErrorIs(t, f.handler.ShutdownNativeResponses(ctx), context.DeadlineExceeded)
	_, exists := f.handler.nativeResponseExecutions.Load(turn.ID())
	require.True(t, exists, "shutdown must retain the execution until bounded upstream cancellation completes")
	require.False(t, turn.Snapshot().CancelConfirmed)
	unblock()
	select {
	case <-secondCancelDone:
	case <-time.After(time.Second):
		t.Fatal("cancel completion did not release the concurrent cleanup")
	}
	ctx2, stop2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop2()
	require.NoError(t, f.handler.ShutdownNativeResponses(ctx2))
	require.True(t, turn.Snapshot().CancelConfirmed)
	require.Equal(t, responseturn.StateCancelled, turn.Snapshot().State)
	require.Equal(t, responseturn.ReasonAdminCancel, turn.Snapshot().Reason)
	require.Equal(t, int64(1), f.createCount.Load())
	require.Equal(t, int64(1), f.cancelCount.Load())
	_, exists = f.handler.nativeResponseExecutions.Load(turn.ID())
	require.False(t, exists)
	t.Log("real SSE created delivered; one blocked upstream cancel POST; concurrent cleanup and shutdown waited; confirmation completed once; execution never revived")
}

func TestReviewNativeBackgroundShutdownIsBoundedAndRejectsNewAdmission(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	defer unblock()
	f := newNativeRecoveryFixtureOptions(t, "background", nativeRecoveryOptions{cancelGate: gate})
	f.server.Client().Timeout = 2 * time.Second
	body := `{"model":"gpt-5.6-sol","input":"shutdown fixture","background":true,"stream":false}`
	status, _, _ := postReviewNative(t, f, body, "shutdown-original")
	require.Equal(t, 200, status)
	turn := f.turn(t)
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	require.ErrorIs(t, f.handler.ShutdownNativeResponses(ctx), context.DeadlineExceeded)
	require.Equal(t, responseturn.StateCancelled, turn.Snapshot().State)
	require.Equal(t, responseturn.ReasonAdminCancel, turn.Snapshot().Reason)
	require.Zero(t, atomic.LoadInt32(&f.slots.releaseAccountCalled))
	status, _, _ = postReviewNative(t, f, body, "shutdown-new")
	require.Equal(t, 503, status)
	unblock()
	ctx2, stop2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop2()
	require.NoError(t, f.handler.ShutdownNativeResponses(ctx2))
	require.Equal(t, int64(1), f.createCount.Load())
	require.Equal(t, int64(1), f.cancelCount.Load())
	require.Equal(t, int32(1), atomic.LoadInt32(&f.slots.releaseUserCalled))
	require.Equal(t, int32(1), atomic.LoadInt32(&f.slots.releaseAccountCalled))
	select {
	case usage := <-f.usage:
		require.Equal(t, 4, usage.InputTokens)
	case <-time.After(time.Second):
		t.Fatal("shutdown dropped final cancellation usage")
	}
}

func TestReviewNativeBackgroundUnsupportedAccountRejectsBeforeCreate(t *testing.T) {
	f := newNativeRecoveryFixtureOptions(t, "background", nativeRecoveryOptions{unsupportedBackground: true})
	f.server.Client().Timeout = time.Second
	status, body, _ := postReviewNative(t, f, `{"model":"gpt-5.6-sol","input":"unsupported fixture","background":true,"stream":false}`, "unsupported")
	require.Equal(t, 400, status, string(body))
	require.Equal(t, "unsupported_background_response", gjson.GetBytes(body, "error.code").String())
	require.Zero(t, f.createCount.Load())
	require.Zero(t, f.cancelCount.Load())
}

func TestReviewNativeJSONErrorStatusPreservedByDuplicateCreate(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newNativeRecoveryFixture(t, "json_error")
			f.server.Client().Timeout = time.Second
			body := fmt.Sprintf(`{"model":"gpt-5.6-sol","input":"error fixture","stream":%v}`, stream)
			status, initial, _ := postReviewNative(t, f, body, "json-error")
			require.Equal(t, 400, status, string(initial))
			status, duplicate, _ := postReviewNative(t, f, body, "json-error")
			require.Equal(t, 400, status, string(duplicate))
			require.JSONEq(t, string(initial), string(duplicate))
			require.Equal(t, int64(1), f.createCount.Load())
		})
	}
}

func TestReviewNativeBackgroundPollingFailureCancelsOriginal(t *testing.T) {
	f := newNativeRecoveryFixtureOptions(t, "background", nativeRecoveryOptions{pollError: true})
	f.server.Client().Timeout = time.Second
	status, _, _ := postReviewNative(t, f, `{"model":"gpt-5.6-sol","input":"poll fixture","background":true,"stream":false}`, "poll-failure")
	require.Equal(t, 200, status)
	turn := f.turn(t)
	require.Eventually(t, func() bool {
		return turn.Snapshot().State == responseturn.StateCancelled && turn.Snapshot().CancelConfirmed
	}, 4*time.Second, time.Millisecond)
	require.Equal(t, responseturn.ReasonUpstreamFailure, turn.Snapshot().Reason)
	require.Equal(t, int64(1), f.createCount.Load())
	require.Equal(t, int64(1), f.cancelCount.Load())
}

func TestReviewNativeBackgroundIdempotencyPollsOriginalUntilTerminal(t *testing.T) {
	f := newNativeRecoveryFixture(t, "background")
	body := `{"model":"gpt-5.6-sol","input":"fixture","background":true,"stream":false}`
	request, err := http.NewRequest(http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "fixture-background")
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	require.NoError(t, err)
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode, string(data))
	require.Equal(t, "queued", gjson.GetBytes(data, "status").String())
	turn := f.turn(t)
	require.Equal(t, responseturn.StateRunning, turn.Snapshot().State)
	require.Equal(t, int32(0), atomic.LoadInt32(&f.slots.releaseAccountCalled), "queued background response still owns capacity")
	select {
	case <-f.usage:
		t.Fatal("queued response was billed as a final response")
	default:
	}
	require.Eventually(t, func() bool { return f.pollCount.Load() > 0 }, 2*time.Second, time.Millisecond)
	f.finishNext()
	require.Eventually(t, func() bool { return turn.Snapshot().State == responseturn.StateCompleted }, 3*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return atomic.LoadInt32(&f.slots.releaseAccountCalled) == 1 }, time.Second, time.Millisecond)
	select {
	case log := <-f.usage:
		require.Equal(t, 4, log.InputTokens)
		require.Equal(t, 1, log.OutputTokens)
	case <-time.After(time.Second):
		t.Fatal("terminal background usage missing")
	}
	for i := 0; i < 3; i++ {
		duplicateRequest, createErr := http.NewRequest(http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(body))
		require.NoError(t, createErr)
		duplicateRequest.Header = request.Header.Clone()
		duplicate, duplicateErr := client.Do(duplicateRequest)
		require.NoError(t, duplicateErr)
		replayed, readErr := io.ReadAll(duplicate.Body)
		require.NoError(t, readErr)
		_ = duplicate.Body.Close()
		require.Equal(t, http.StatusOK, duplicate.StatusCode)
		require.Equal(t, "completed", gjson.GetBytes(replayed, "status").String())
		query, queryErr := client.Get(f.server.URL + "/v1/responses/resp_fixture")
		require.NoError(t, queryErr)
		_, _ = io.Copy(io.Discard, query.Body)
		_ = query.Body.Close()
		require.Equal(t, http.StatusOK, query.StatusCode)
		cancelled, cancelErr := client.Post(f.server.URL+"/v1/responses/resp_fixture/cancel", "application/json", nil)
		require.NoError(t, cancelErr)
		_, _ = io.Copy(io.Discard, cancelled.Body)
		_ = cancelled.Body.Close()
	}
	require.Equal(t, int64(1), f.createCount.Load())
	select {
	case <-f.usage:
		t.Fatal("background response settled more than once")
	default:
	}
}

func TestReviewNativeBackgroundCancelAndRevocationSettleOnce(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "explicit_cancel"
		if revoke {
			name = "permission_revoked"
		}
		t.Run(name, func(t *testing.T) {
			f := newNativeRecoveryFixture(t, "background")
			f.server.Client().Timeout = 5 * time.Second
			response := f.create(t, `{"model":"gpt-5.6-sol","input":"fixture","background":true,"stream":false}`)
			_, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			_ = response.Body.Close()
			turn := f.turn(t)
			if revoke {
				f.accessRevoked.Store(true)
			} else {
				cancelled, e := f.server.Client().Post(f.server.URL+"/v1/responses/resp_fixture/cancel", "application/json", nil)
				require.NoError(t, e)
				_, _ = io.Copy(io.Discard, cancelled.Body)
				_ = cancelled.Body.Close()
				require.Equal(t, 200, cancelled.StatusCode)
			}
			require.Eventually(t, func() bool {
				return turn.Snapshot().State == responseturn.StateCancelled && turn.Snapshot().CancelConfirmed
			}, 4*time.Second, time.Millisecond)
			want := responseturn.ReasonUserStop
			if revoke {
				want = responseturn.Reason("permission_revoked")
			}
			require.Equal(t, want, turn.Snapshot().Reason)
			require.Eventually(t, func() bool { return atomic.LoadInt32(&f.slots.releaseAccountCalled) == 1 }, time.Second, time.Millisecond)
			select {
			case usage := <-f.usage:
				require.Equal(t, 4, usage.InputTokens)
				require.Equal(t, 1, usage.OutputTokens)
			case <-time.After(time.Second):
				t.Fatal("verified cancelled usage was not settled")
			}
			read, e := f.server.Client().Get(f.server.URL + "/v1/responses/resp_fixture")
			require.NoError(t, e)
			data, e := io.ReadAll(read.Body)
			require.NoError(t, e)
			_ = read.Body.Close()
			if revoke {
				require.Equal(t, 404, read.StatusCode)
			} else {
				require.Equal(t, "cancelled", gjson.GetBytes(data, "status").String())
			}
			require.Equal(t, int64(1), f.createCount.Load())
			require.Equal(t, int64(1), f.cancelCount.Load())
			select {
			case <-f.usage:
				t.Fatal("cancelled usage settled twice")
			default:
			}
		})
	}
}
