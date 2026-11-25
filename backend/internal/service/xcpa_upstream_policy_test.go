//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestXCPAUpstreamPolicyTrustAndAccountSwitch(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := compatStreamService(nil, 1, 1, 0)
	a := xcpaTestAccount()
	enabled := withXCPAUpstreamPolicy(context.Background(), a)
	timeout, _ := svc.openAIFirstOutputPlanFor(enabled, a.ID, "gpt-6-astra", "xhigh", time.Now())
	require.Zero(t, timeout)
	timeout, _ = svc.openAIFirstOutputPlanFor(enabled, a.ID+1, "gpt-6-astra", "xhigh", time.Now())
	require.Positive(t, timeout, "a policy is bound to the selected account")
	normal := rawChatCompletionsTestAccount()
	normal.ID++
	ctx := withXCPAUpstreamPolicy(enabled, normal)
	timeout, _ = svc.openAIFirstOutputPlanFor(ctx, normal.ID, "gpt-6-astra", "xhigh", time.Now())
	require.Positive(t, timeout, "switching accounts clears the previous policy")
	require.False(t, xcpaUntimedFromContext(ctx))
	require.Equal(t, HTTPUpstreamProfileXCPAUntimed, HTTPUpstreamProfileFromContext(WithHTTPUpstreamProfile(enabled, HTTPUpstreamProfileOpenAI)))
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAI)))
	for _, value := range []any{"true", 1, 1.0, nil, false} {
		normal.Extra = map[string]any{XCPAActiveUpstreamUntimedExtraKey: value}
		require.False(t, normal.xcpaActiveUpstreamUntimed())
	}
	for _, change := range []func(*Account){func(a *Account) { a.Platform = PlatformAnthropic }, func(a *Account) { a.Type = AccountTypeOAuth }} {
		wrong := xcpaTestAccount()
		change(wrong)
		require.False(t, wrong.xcpaActiveUpstreamUntimed())
	}
	c, _ := compatStreamContext("/v1/responses", `{"xcpa_active_upstream_untimed":true}`)
	c.Request.Header.Set("X-XCPA-Active-Upstream-Untimed", "true")
	normal.Name = "XCPA 5X-2"
	require.False(t, xcpaUntimedFromContext(withXCPAUpstreamPolicy(c.Request.Context(), normal)))
	require.NoError(t, validateXCPAUpstreamExtra(PlatformOpenAI, AccountTypeAPIKey, a.Extra))
	require.Error(t, validateXCPAUpstreamExtra(PlatformAnthropic, AccountTypeAPIKey, a.Extra))
	require.Error(t, validateXCPAUpstreamExtra(PlatformOpenAI, AccountTypeOAuth, a.Extra))
	require.Error(t, validateXCPAUpstreamExtra(PlatformOpenAI, AccountTypeAPIKey, map[string]any{XCPAActiveUpstreamUntimedExtraKey: "true"}))
}

func TestXCPAUpstreamPolicyRetainsCallerCancellationAndMetadata(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		type metadataKey struct{}
		caller, cancel := context.WithCancel(context.WithValue(context.Background(), metadataKey{}, "fixture"))
		defer cancel()
		ctx := withXCPAUpstreamPolicy(caller, xcpaTestAccount())
		detached, release := detachUpstreamContext(ctx)
		defer release()
		require.Equal(t, "fixture", detached.Value(metadataKey{}))
		time.Sleep(10 * time.Minute)
		require.NoError(t, detached.Err())
		cancel()
		synctest.Wait()
		require.ErrorIs(t, detached.Err(), context.Canceled, "caller cancellation propagates without a drain deadline")
	})
}

func TestXCPAUpstreamPolicyObservedProviderFailurePrecedesCancellation(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, failure := range []string{"401", "429", "503", "overload"} {
		t.Run(failure, func(t *testing.T) {
			account := xcpaTestAccount()
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := resilience.NewRequestState()
			st.InitDelivery(client, time.Now(), nil)
			st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "gpt-5.4"})
			ctx := resilience.WithRequestState(client, st)
			var rows []schedquality.AttemptSample
			recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
			recorder.SetObserver(func(row schedquality.AttemptSample) { rows = append(rows, row) })
			svc := &OpenAIGatewayService{schedAttemptRecorder: recorder}
			capture := svc.beginSchedAttempt(ctx, account, nil, 0, true)
			if failure == "overload" {
				capture.observe([]byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_is_overloaded","message":"busy"}}}`))
			} else {
				status := map[string]int{"401": 401, "429": 429, "503": 503}[failure]
				capture.finishHTTP(&http.Response{StatusCode: status}, []byte(`{"error":{"type":"server_error","message":"provider failure"}}`))
			}
			cancel()
			capture.cancel()
			capture.finish(nil, context.Canceled)
			require.Len(t, rows, 2)
			require.Equal(t, schedquality.OutcomeAccountFault, rows[1].Outcome)
			require.NotEqual(t, schedquality.ClassClientCancel, rows[1].Class)
			require.False(t, strings.Contains(rows[1].Detail, "CLIENT_CANCEL"))
		})
	}
}

func TestXCPAUpstreamPolicyFailedDownstreamWriteStopsBody(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		a := xcpaTestAccount()
		w := newGWWriter()
		w.fail = func(string) bool { return true }
		c := gwContext("/v1/responses", w)
		st := resilience.NewRequestState()
		st.SetSchedSelection(resilience.SchedSelection{AccountID: a.ID, Model: "gpt-5.4"})
		ctx := resilience.WithRequestState(c.Request.Context(), st)
		c.Request = c.Request.WithContext(ctx)
		var rows []schedquality.AttemptSample
		recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
		recorder.SetObserver(func(row schedquality.AttemptSample) { rows = append(rows, row) })
		svc := compatStreamService(nil, 0, 0, 0)
		svc.schedAttemptRecorder = recorder
		capture := svc.beginSchedAttempt(ctx, a, nil, 0, true)
		up := newGWUpstream(nil)
		defer up.Close()
		defer up.pw.Close()
		done := make(chan error, 1)
		go func() {
			_, err := svc.handleStreamingResponse(ctx, up.response(nil), c, a, time.Now(), "gpt-5.4", "gpt-5.4")
			capture.finish(nil, err)
			done <- err
		}()
		up.send(t, ptCreated, ptDelta)
		synctest.Wait()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		default:
			t.Fatal("failed downstream write left the upstream body active")
		}
		require.True(t, up.closed.Load())
		require.Len(t, rows, 2)
		require.Equal(t, schedquality.Outcome(5), rows[1].Outcome)
	})
}

type xcpaCancelOnRead struct {
	reader *strings.Reader
	cancel context.CancelFunc
}

func (b *xcpaCancelOnRead) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if n > 0 && b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	return n, err
}
func (b *xcpaCancelOnRead) Close() error { return nil }

func TestXCPAUpstreamPolicyDecodedFailureAndCancelInSameRead(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"responses", "passthrough", "chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			a := xcpaTestAccount()
			w := newGWWriter()
			c := gwContext("/v1/"+route, w)
			client, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			st := resilience.NewRequestState()
			st.InitDelivery(client, time.Now(), nil)
			st.SetSchedSelection(resilience.SchedSelection{AccountID: a.ID, Model: "gpt-5.4"})
			ctx := resilience.WithRequestState(client, st)
			c.Request = c.Request.WithContext(ctx)
			svc := compatStreamService(nil, 0, 0, 0)
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: &xcpaCancelOnRead{reader: strings.NewReader(ptOverload), cancel: cancel}}
			var err error
			switch route {
			case "passthrough":
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, a, time.Now(), "gpt-5.4", "gpt-5.4")
			case "chat":
				_, err = svc.handleChatStreamingResponse(resp, c, a, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now(), 0)
			case "messages":
				_, err = svc.handleAnthropicStreamingResponse(resp, c, a, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
			default:
				_, err = svc.handleStreamingResponse(ctx, resp, c, a, time.Now(), "gpt-5.4", "gpt-5.4")
			}
			require.Error(t, err)
			require.False(t, errors.Is(err, context.Canceled), "a decoded provider error must survive a concurrent cancellation")
			require.NotContains(t, err.Error(), "first_output_timeout")
		})
	}
}

func TestXCPAUpstreamPolicyAdminBulkWritesOnlyValidatedTargets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		accounts  []*Account
		value     any
		wantError bool
	}{
		{"enabled", fcoCompatBulkAccounts(2), true, false},
		{"disabled", fcoCompatBulkAccounts(2), false, false},
		{"string rejected", fcoCompatBulkAccounts(2), "true", true},
		{"mixed source type rejected", fcoCompatBulkAccounts(1, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fcoCompatBulkRepo{accounts: tc.accounts}
			svc := &adminServiceImpl{accountRepo: repo}
			result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, Extra: map[string]any{XCPAActiveUpstreamUntimedExtraKey: tc.value}})
			if tc.wantError {
				require.Error(t, err)
				require.Zero(t, repo.bulkWrites)
			} else {
				require.NoError(t, err)
				require.Equal(t, 2, result.Success)
				require.Equal(t, 1, repo.bulkWrites)
			}
		})
	}
}

func TestXCPAUpstreamPolicyNativeMessagesNoHiddenProgressCutoff(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stall.TextStallSeconds = 1 })
	synctest.Test(t, func(t *testing.T) {
		a := xcpaTestAccount()
		w := newGWWriter()
		c := gwContext("/v1/messages", w)
		up := newGWUpstream(nil)
		defer up.Close()
		defer up.pw.Close()
		svc := compatStreamService(nil, 0, 1, 1)
		done := make(chan error, 1)
		go func() {
			_, err := svc.handleNativeAnthropicStreamingResponse(c.Request.Context(), up.response(nil), c, a, "gpt-5.4", "gpt-5.4", "gpt-5.4", nil, time.Now())
			done <- err
		}()
		up.send(t, gwMsgStart)
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("native conversion revived a progress cutoff: %v", err)
		default:
		}
		up.send(t, gwTextStart, gwTextDelta, gwBlockStop, gwMsgDelta("end_turn"), gwMsgStop)
		_ = up.pw.Close()
		synctest.Wait()
		require.NoError(t, <-done)
	})
}
