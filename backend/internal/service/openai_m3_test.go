//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const m3TwoItemRequest = `{"model":"gpt-5.5","stream":true,"input":[{"type":"agent_message","id":"amsg_one","content":[{"type":"encrypted_content","data":"same-cipher"},{"type":"input_text","text":"first"}]},{"type":"agent_message","id":"amsg_two","content":[{"type":"encrypted_content","data":"same-cipher"},{"type":"input_text","text":"second"}]}]}`

func TestOpenAIM3S4LearnsOnlyNamedItem(t *testing.T) {
	req := newWebSearchReplayRequest(t)
	ctx := req.ctx
	svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	account := webSearchReplayAccount(false)
	a := svc.armOpenAIM3(req.c, account, []byte(m3TwoItemRequest))
	payload := []byte(`{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}`)
	err := a.learn(ctx, []byte(m3TwoItemRequest), payload, "Encrypted content could not be verified for item amsg_one", http.StatusBadRequest, 0)
	var signal *openAIRequestRepairSignal
	require.ErrorAs(t, err, &signal)
	require.Len(t, a.state.replaced, 1)
	require.Len(t, svc.getOpenAIWSStateStore().GetSessionBadEncryptedSlots(a.request.groupID, a.request.session, account.ID), 1)
	derived, err := a.derive(ctx, []byte(m3TwoItemRequest))
	require.NoError(t, err)
	require.Equal(t, "input_text", gjson.GetBytes(derived, "input.0.content.0.type").String())
	require.Equal(t, "amsg_one", gjson.GetBytes(derived, "input.0.id").String())
	require.Equal(t, "amsg_two", gjson.GetBytes(derived, "input.1.id").String())
	require.Equal(t, "encrypted_content", gjson.GetBytes(derived, "input.1.content.0.type").String())
}

func TestOpenAIM3BatchBadWaitsForSuccessfulReplay(t *testing.T) {
	req := newWebSearchReplayRequest(t)
	svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	account := webSearchReplayAccount(false)
	a := svc.armOpenAIM3(req.c, account, []byte(m3TwoItemRequest))
	err := a.learn(req.ctx, []byte(m3TwoItemRequest), []byte(`{"error":{"message":"Encrypted content is invalid for this conversation"}}`), "Encrypted content is invalid for this conversation", http.StatusBadRequest, 0)
	var signal *openAIRequestRepairSignal
	require.ErrorAs(t, err, &signal)
	require.Empty(t, svc.getOpenAIWSStateStore().GetSessionBadEncryptedSlots(a.request.groupID, a.request.session, account.ID))
	a.succeeded(req.ctx)
	require.Len(t, svc.getOpenAIWSStateStore().GetSessionBadEncryptedSlots(a.request.groupID, a.request.session, account.ID), 2)
}

func TestOpenAIM3LearnsFunctionOutputSlots(t *testing.T) {
	req := newWebSearchReplayRequest(t)
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	account := webSearchReplayAccount(false)
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"function_call_output","call_id":"call_one","output":[{"type":"encrypted_content","payload":"cipher"}]}]}`)
	attempt := svc.armOpenAIM3(req.c, account, body)
	err := attempt.learn(req.ctx, body, []byte(`{"error":{"code":"server_error","message":"Encrypted function output content could not be decrypted"}}`), "Encrypted function output content could not be decrypted", http.StatusBadRequest, 0)
	var signal *openAIRequestRepairSignal
	require.ErrorAs(t, err, &signal)
	require.Equal(t, "input_text", gjson.GetBytes(signal.next, "input.0.output.0.type").String())
	require.Empty(t, store.GetSessionBadEncryptedSlots(attempt.request.groupID, attempt.request.session, account.ID))
	attempt.succeeded(req.ctx)
	require.Len(t, store.GetSessionBadEncryptedSlots(attempt.request.groupID, attempt.request.session, account.ID), 1)
}

func TestForwardOpenAIM3ReplaySendsOnlyPlaceholderInNamedSlot(t *testing.T) {
	withResilienceSettings(t, nil)
	req := newWebSearchReplayRequest(t)
	rejected := `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIUpstreamErrorResponse(http.StatusBadRequest, rejected),
		webSearchSuccessStream("resp_m3_replayed"),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(m3TwoItemRequest))
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)
	second := upstream.bodies[1]
	require.Equal(t, "input_text", gjson.GetBytes(second, "input.0.content.0.type").String())
	require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(second, "input.0.content.0.text").String())
	require.Equal(t, "amsg_one", gjson.GetBytes(second, "input.0.id").String())
	require.Equal(t, "encrypted_content", gjson.GetBytes(second, "input.1.content.0.type").String())
	require.Equal(t, "same-cipher", gjson.GetBytes(second, "input.1.content.0.data").String())
}

func TestForwardOpenAIM3NativeStreamReplaySendsPlaceholder(t *testing.T) {
	withResilienceSettings(t, nil)
	req := newWebSearchReplayRequest(t)
	payload := `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(payload), webSearchSuccessStream("resp_m3_native_stream")}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(m3TwoItemRequest))
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(upstream.bodies[1], "input.0.content.0.text").String())
}

func TestForwardOpenAIM3PassthroughHTTPReplaySendsPlaceholder(t *testing.T) {
	withResilienceSettings(t, nil)
	req := newWebSearchReplayRequest(t)
	rejected := `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIUpstreamErrorResponse(http.StatusBadRequest, rejected),
		webSearchSuccessStream("resp_m3_passthrough_http"),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(true), []byte(m3TwoItemRequest))
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(upstream.bodies[1], "input.0.content.0.text").String())
}

func TestForwardOpenAIM3AggregatedReplaySendsPlaceholder(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			body, err := sjson.SetBytes([]byte(m3TwoItemRequest), "stream", false)
			require.NoError(t, err)
			payload := `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}}`
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				m3AggregateFailure(payload),
				webSearchSuccessStream("resp_m3_aggregate"),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			_, err = svc.Forward(req.ctx, req.c, webSearchReplayAccount(passthrough), body)
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 2)
			require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(upstream.bodies[1], "input.0.content.0.text").String())
		})
	}
}

func m3AggregateFailure(payload string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("event: response.failed\ndata: " + payload + "\n\ndata: [DONE]\n\n")),
	}
}

func TestForwardOpenAIM3PassthroughReplaySendsPlaceholder(t *testing.T) {
	withResilienceSettings(t, nil)
	req := newWebSearchReplayRequest(t)
	payload := `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(payload), webSearchSuccessStream("resp_m3_passthrough")}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(true), []byte(m3TwoItemRequest))
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)
	second := upstream.bodies[1]
	require.Equal(t, "input_text", gjson.GetBytes(second, "input.0.content.0.type").String())
	require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(second, "input.0.content.0.text").String())
	require.Equal(t, "encrypted_content", gjson.GetBytes(second, "input.1.content.0.type").String())
}

func TestOpenAIM3StateSwitchBudgetIsIndependent(t *testing.T) {
	settings := &resilience.Settings{Enabled: true, Budget: resilience.BudgetSettings{Enabled: true, MaxAttempts: 5, SameAccountRetryMax: 1, PreOutputBudgetSeconds: 240}}
	budget := resilience.NewBudget(nil, settings, resilience.KindText, time.Now())
	require.True(t, budget.AllowStateSwitch())
	require.False(t, budget.AllowStateSwitch())
	require.False(t, budget.CapacitySwitchUsed())
}

func TestOpenAIM3AnonymousSlotsAreNeverPersisted(t *testing.T) {
	req := newWebSearchReplayRequest(t)
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	account := webSearchReplayAccount(false)
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"agent_message","content":[{"type":"encrypted_content","data":"cipher"}]}]}`)
	a := svc.armOpenAIM3(req.c, account, body)
	err := a.learn(req.ctx, body, []byte(`{"error":{"message":"Encrypted content is invalid for this conversation"}}`), "Encrypted content is invalid for this conversation", http.StatusBadRequest, 0)
	var signal *openAIRequestRepairSignal
	require.ErrorAs(t, err, &signal)
	a.succeeded(req.ctx)
	require.Empty(t, store.GetSessionBadEncryptedSlots(a.request.groupID, a.request.session, account.ID))
}

func TestResetOpenAIRepairAttemptRefreshesAttemptProfile(t *testing.T) {
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	state := resilience.StateFrom(ctx)
	firstAccount := webSearchReplayAccount(false)
	start := time.Now().Add(-time.Second)
	state.SetAttemptProfile(resilience.AttemptProfile{AccountID: firstAccount.ID, Model: "old", AttemptStart: start, Warm: true})
	newStart := resetOpenAIRepairAttempt(ctx, firstAccount, "gpt-5.5", true, []byte("body"), true)
	profile := state.AttemptProfile()
	require.True(t, profile.AttemptStart.Equal(newStart))
	require.True(t, profile.AttemptStart.After(start))
	require.True(t, profile.Warm)
	require.Equal(t, len("body"), profile.BodyBytes)

	otherAccount := *firstAccount
	otherAccount.ID++
	resetOpenAIRepairAttempt(ctx, &otherAccount, "gpt-5.5", true, []byte("body"), false)
	require.False(t, state.AttemptProfile().Warm, "warm state does not cross account switches")
}

func TestOpenAIM3SequentialS4AndForwardRebuild(t *testing.T) {
	req := newWebSearchReplayRequest(t)
	svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	account := webSearchReplayAccount(false)
	attempt := svc.armOpenAIM3(req.c, account, []byte(m3TwoItemRequest))
	body := []byte(m3TwoItemRequest)
	for _, item := range []string{"amsg_one", "amsg_two"} {
		message := "Encrypted content could not be verified for item " + item
		err := attempt.learn(req.ctx, body, nil, message, http.StatusBadRequest, 0)
		var signal *openAIRequestRepairSignal
		require.ErrorAs(t, err, &signal)
		body = signal.next
	}
	require.Len(t, attempt.state.replaced, 2)
	rebuilt := svc.armOpenAIM3(req.c, account, []byte(m3TwoItemRequest))
	require.Same(t, attempt.state, rebuilt.state)
	err := rebuilt.learn(req.ctx, []byte(m3TwoItemRequest), nil, "Encrypted content could not be verified for item amsg_one", 400, 0)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "no_growth", failure.M3Cause)
	other := *account
	other.ID++
	require.Empty(t, svc.armOpenAIM3(req.c, &other, []byte(m3TwoItemRequest)).state.replaced)
}

func TestOpenAIM3SharedBadDoesNotSuppressRequestGrowth(t *testing.T) {
	req := newWebSearchReplayRequest(t)
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	account := webSearchReplayAccount(false)
	attempt := svc.armOpenAIM3(req.c, account, []byte(m3TwoItemRequest))
	slots, _, err := openAIM3Slots([]byte(m3TwoItemRequest))
	require.NoError(t, err)
	// Another request learned the same evidence after this request was sent.
	store.MarkSessionBadEncryptedSlots(attempt.request.groupID, attempt.request.session, account.ID, []openAIBadEncryptedSlot{slots[0].id.openAIBadEncryptedSlot}, time.Hour)
	err = attempt.learn(req.ctx, []byte(m3TwoItemRequest), nil, "Encrypted content could not be verified for item amsg_one", 400, 0)
	var signal *openAIRequestRepairSignal
	require.ErrorAs(t, err, &signal)
	require.Len(t, attempt.state.replaced, 1)
}

func TestOpenAIM3CanonicalDigestAndOpaqueContainers(t *testing.T) {
	first := []byte(`{"input":[{"type":"agent_message","id":"amsg_one","content":[{"type":"encrypted_content","encrypted_content":"<cipher>","n":9007199254740993}]}]}`)
	second := []byte(`{"input":[{"id":"amsg_one","type":"agent_message","content":[{"n":9007199254740993,"encrypted_content":"\u003ccipher\u003e","type":"encrypted_content"}]}]}`)
	left, _, err := openAIM3Slots(first)
	require.NoError(t, err)
	right, _, err := openAIM3Slots(second)
	require.NoError(t, err)
	require.Equal(t, left[0].id, right[0].id)
	changed := strings.Replace(string(second), "9007199254740993", "9007199254740992", 1)
	different, _, err := openAIM3Slots([]byte(changed))
	require.NoError(t, err)
	require.NotEqual(t, left[0].id.digest, different[0].id.digest)
	opaque := []byte(`{"input":[{"type":"function_call","arguments":"{\"type\":\"encrypted_content\"}"},{"type":"function_call_output","call_id":"call_1","output":"[{\"type\":\"encrypted_content\"}]"}]}`)
	slots, _, err := openAIM3Slots(opaque)
	require.NoError(t, err)
	require.Empty(t, slots)
}

func TestOpenAIM3AmbiguousShapesFailClosed(t *testing.T) {
	for _, body := range []string{
		`{"input":[],"input":[]}`,
		`{"input":[{"type":"agent_message","id":"amsg_one","content":{}}]}`,
		`{"input":[{"type":"agent_message","id":"amsg_one","content":[{"type":"encrypted_content","data":"a","data":"b"}]}]}`,
		`{"input":[{"type":"agent_message","id":"amsg_one","content":[{"type":"encrypted_content"}]},{"type":"agent_message","id":"amsg_one","content":[{"type":"encrypted_content"}]}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			req := newWebSearchReplayRequest(t)
			svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
			attempt := svc.armOpenAIM3(req.c, webSearchReplayAccount(false), []byte(body))
			err := attempt.learn(req.ctx, []byte(body), nil, "Encrypted content could not be verified for item amsg_one", 400, 0)
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, "shape", failure.M3Cause)
			require.Empty(t, attempt.state.replaced)
			require.Empty(t, svc.getOpenAIWSStateStore().GetSessionBadEncryptedSlots(attempt.request.groupID, attempt.request.session, attempt.account.ID))
		})
	}
}

func TestOpenAIM3BadScopeAndExpiry(t *testing.T) {
	store := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)
	slot := openAIBadEncryptedSlot{kind: "agent_message", ref: "amsg_one", digest: "digest"}
	require.Equal(t, 1, store.MarkSessionBadEncryptedSlots(7, "session", 11, []openAIBadEncryptedSlot{slot}, time.Hour))
	require.Empty(t, store.GetSessionBadEncryptedSlots(8, "session", 11))
	require.Empty(t, store.GetSessionBadEncryptedSlots(7, "other", 11))
	require.Empty(t, store.GetSessionBadEncryptedSlots(7, "session", 12))
	snapshot := store.GetSessionBadEncryptedSlots(7, "session", 11)
	delete(snapshot, slot)
	require.Len(t, store.GetSessionBadEncryptedSlots(7, "session", 11), 1, "callers cannot mutate the shared Bad set")
	store.sessionBadEncryptedMu.Lock()
	key := openAIBadEncryptedScope{7, "session", 11}
	binding := store.sessionBadEncrypted[key]
	binding.expiresAt = time.Now().Add(-time.Second)
	store.sessionBadEncrypted[key] = binding
	store.sessionBadEncryptedMu.Unlock()
	require.Empty(t, store.GetSessionBadEncryptedSlots(7, "session", 11))
}

func TestOpenAIM3WrappedStatusRoutesOnly400(t *testing.T) {
	for _, status := range []int{400, 401, 403, 413, 429, 500} {
		req := newWebSearchReplayRequest(t)
		account := webSearchReplayAccount(false)
		svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
		repair := armOpenAIRequestRepair(req.c, account, nil)
		repair.body = []byte(m3TwoItemRequest)
		repair.m3 = svc.armOpenAIM3(req.c, account, repair.body)
		next, err, retry := svc.interceptOpenAIM3Failover(req.ctx, req.c, account, nil, nil, &UpstreamFailoverError{StatusCode: status, WrappedStatus: status, ClientMessage: "upstream rejection"})
		if status == 400 {
			require.NoError(t, err)
			require.True(t, retry)
			require.Equal(t, "input_text", gjson.GetBytes(next, "input.0.content.0.type").String())
			require.Equal(t, "input_text", gjson.GetBytes(next, "input.1.content.0.type").String())
			continue
		}
		require.Error(t, err)
		require.False(t, retry)
		require.Empty(t, repair.m3.state.replaced)
	}
}

func TestForwardOpenAIM3Rewritten4xxRepairsFieldsBeforeSwitch(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		withResilienceSettings(t, nil)
		req := newWebSearchReplayRequest(t)
		body, err := sjson.SetBytes([]byte(m3TwoItemRequest), "temperature", 0.5)
		require.NoError(t, err)
		upstream := &httpUpstreamRecorder{responses: []*http.Response{
			newOpenAIUpstreamErrorResponse(400, `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}`),
			newOpenAIUpstreamErrorResponse(400, `{"error":{"message":"Unsupported parameter: temperature","param":"temperature","code":"unsupported_parameter"}}`),
			newOpenAIUpstreamErrorResponse(422, `{"error":{"message":"rewritten shape rejected"}}`),
		}}
		svc := newOpenAIImageGenerationControlTestService(upstream)
		_, err = svc.Forward(req.ctx, req.c, webSearchReplayAccount(passthrough), body)
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, OpenAIStateRepairReason, failure.Reason)
		require.Equal(t, "rewritten_4xx", failure.M3Cause)
		require.Len(t, upstream.bodies, 3)
		require.False(t, gjson.GetBytes(upstream.bodies[2], "temperature").Exists())
		require.Equal(t, "input_text", gjson.GetBytes(upstream.bodies[2], "input.0.content.0.type").String())
	}
}

func TestOpenAIM3ReplayFirstOutputWindowUsesRemainingBudget(t *testing.T) {
	withResilienceSettings(t, nil)
	settings := resilience.Current()
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	budgetStart := time.Now().Add(-225 * time.Second)
	state.SetBudget(resilience.NewBudget(ctx, settings, resilience.KindText, budgetStart))
	account := webSearchReplayAccount(false)
	state.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: budgetStart, Warm: false})
	attemptStart := resetOpenAIRepairAttempt(ctx, account, "gpt-5.5", true, []byte("body"), true)
	window := (&OpenAIGatewayService{}).openAIFirstOutputTimeoutFor(ctx, account.ID, "gpt-5.5", "", attemptStart)
	require.Positive(t, window)
	require.LessOrEqual(t, window, 15*time.Second)
	require.Greater(t, window, 14*time.Second)
	require.True(t, attemptStart.Add(window).After(time.Now()), "replay does not inherit the previous attempt's expired deadline")
}

func TestOpenAIM3Wrapped400LocalRepairsRetainEvidence(t *testing.T) {
	for _, compact := range []bool{false, true} {
		req := newWebSearchReplayRequest(t)
		account := webSearchReplayAccount(false)
		svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
		repair := armOpenAIRequestRepair(req.c, account, nil)
		repair.body = []byte(m3TwoItemRequest)
		repair.m3 = svc.armOpenAIM3(req.c, account, repair.body)
		payload := []byte(`{"error":{"message":"response protection is unavailable"}}`)
		message := "response protection is unavailable"
		if compact {
			req.c.Request.URL.Path = "/v1/responses/compact"
			payload = []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`)
			message = "model not found"
		}
		next, err, retry := svc.interceptOpenAIM3Failover(req.ctx, req.c, account, nil, nil, &UpstreamFailoverError{StatusCode: 400, WrappedStatus: 400, ClientMessage: message, ResponseBody: payload})
		if compact {
			var signal *openAICompactFallbackSignal
			require.ErrorAs(t, err, &signal)
			require.Equal(t, 400, signal.wrappedStatus)
			// The account has no compact fallback; the same evidence reaches M3.
			err = openAIM3AfterLocalRepairExhausted(req.ctx, req.c, signal.payload, signal.message, http.StatusBadRequest, signal.wrappedStatus)
			var repairSignal *openAIRequestRepairSignal
			require.ErrorAs(t, err, &repairSignal)
			next, retry = repairSignal.next, true
		} else {
			require.NoError(t, err)
		}
		require.True(t, retry)
		require.Equal(t, "input_text", gjson.GetBytes(next, "input.0.content.0.type").String())
		require.Empty(t, svc.getOpenAIWSStateStore().GetSessionBadEncryptedSlots(repair.m3.request.groupID, repair.m3.request.session, account.ID), "batch evidence waits for complete replay success")
	}
}

func TestOpenAIWebSearchSignalCarriesWrapped400ToM3(t *testing.T) {
	req := newWebSearchReplayRequest(t)
	account := webSearchReplayAccount(false)
	svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	repair := armOpenAIRequestRepair(req.c, account, nil)
	repair.body = []byte(m3TwoItemRequest)
	repair.m3 = svc.armOpenAIM3(req.c, account, repair.body)
	webSearch := &openAIWebSearchReplay{accountID: account.ID, declared: true}
	req.c.Set(openAIWebSearchReplayContextKey, webSearch)
	message := "response protection is unavailable; Encrypted content could not be verified for item amsg_one"
	wrappedBody := []byte(`{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":400,"message":"` + message + `"}}}`)
	signalErr := newOpenAIWebSearchReplaySignal(req.c, account, wrappedBody, message, http.StatusBadRequest)
	var signal *openAIWebSearchReplaySignal
	require.ErrorAs(t, signalErr, &signal)
	next, err := svc.openAIWebSearchReplayAfterSignal(req.ctx, req.c, account, webSearch, repair.body, nil, signalErr)
	require.NoError(t, err)
	require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(next, "input.0.content.0.text").String())
	require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(next, "input.1.content.0.text").String())
}

func TestForwardOpenAIM3ContinuesAfterWebSearchRepairExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		passthrough bool
		streamError bool
	}{
		{name: "native stream", streamError: true},
		{name: "passthrough HTTP", passthrough: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.MaxAttempts = 10 })
			resetWebSearchDeclarations(t)
			req := newWebSearchReplayRequest(t)
			body, err := sjson.SetRawBytes([]byte(m3TwoItemRequest), "input.2", []byte(`{"type":"web_search_call","id":"ws_old","status":"completed","action":{"type":"search","query":"history"}}`))
			require.NoError(t, err)
			first := `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"}}`
			second := `{"error":{"code":"invalid_encrypted_content","message":"response protection is unavailable; Encrypted content could not be verified for item amsg_two"}}`
			reject := func(payload string) *http.Response {
				if tc.streamError {
					return requestRepairFailedStream(`{"type":"response.failed","response":` + payload + `}`)
				}
				return newOpenAIUpstreamErrorResponse(http.StatusBadRequest, payload)
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				reject(first), reject(second), reject(second), webSearchSuccessStream("resp_m3_web_search"),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			_, err = svc.Forward(req.ctx, req.c, webSearchReplayAccount(tc.passthrough), body)
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 4, "one M3 item, bounded declaration repair, then the second M3 item")
			require.Equal(t, "input_text", gjson.GetBytes(upstream.bodies[1], "input.0.content.0.type").String())
			require.Equal(t, "encrypted_content", gjson.GetBytes(upstream.bodies[2], "input.1.content.0.type").String())
			require.True(t, gjsonToolsContainWebSearch(gjson.GetBytes(upstream.bodies[2], "tools")))
			require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(upstream.bodies[3], "input.1.content.0.text").String())
			require.Equal(t, "amsg_two", gjson.GetBytes(upstream.bodies[3], "input.1.id").String())
			require.Contains(t, req.rec.Body.String(), "hello")
			require.Empty(t, req.session.failures)
		})
	}
}

func TestOpenAIM3WebSearchExhaustionDoesNotInvent4xx(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		req := newWebSearchReplayRequest(t)
		account := webSearchReplayAccount(false)
		svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
		repair := armOpenAIRequestRepair(req.c, account, nil)
		repair.body = []byte(m3TwoItemRequest)
		repair.m3 = svc.armOpenAIM3(req.c, account, repair.body)
		slots, _, err := openAIM3Slots(repair.body)
		require.NoError(t, err)
		repair.m3.state.replaced[slots[0].id] = struct{}{}
		_, err = svc.openAIWebSearchReplayDecide(req.ctx, req.c, account, &openAIWebSearchReplay{accountID: account.ID, declared: true}, repair.body, status, "", nil, "response protection is unavailable")
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, OpenAIWebSearchHistoryRejectedReason, failure.Reason)
	}
}

func TestOpenAIM3AvoidancePreservesHealthAndRejectsFailedLastResort(t *testing.T) {
	withResilienceSettings(t, nil)
	store := newMemResilienceStore()
	cache := &atomicStickyTestCache{bindings: make(map[string]int64)}
	until := time.Now().Add(45 * time.Second)
	account := Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &until}
	svc := &OpenAIGatewayService{
		accountRepo: openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{account}}},
		cache:       cache, cfg: &config.Config{},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, store),
	}
	groupID, session := int64(9), "v2:m3-session"
	key := svc.openAISessionCacheKey(session)
	cache.bindings[key] = account.ID
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	svc.AvoidOpenAIStateAccount(ctx, groupID, session, account.ID)
	avoided, err := store.SessionAvoid(ctx, groupID, session)
	require.NoError(t, err)
	require.Contains(t, avoided, account.ID)
	require.True(t, state.Avoided(account.ID))
	_, marked := svc.resilience.avoidingSessions.Load(sessionMarkKey(groupID, session))
	require.True(t, marked)
	require.NotContains(t, cache.bindings, key, "the failed owner is removed")
	failures, err := store.SessionFailures(ctx, groupID, session)
	require.NoError(t, err)
	require.Empty(t, failures, "state refusal must not advance the session failure streak")

	cache.bindings[key] = account.ID + 1
	svc.AvoidOpenAIStateAccount(ctx, groupID, session, account.ID)
	require.Equal(t, account.ID+1, cache.bindings[key], "a concurrent replacement owner survives")
	failedAccountIDs := map[int64]struct{}{account.ID: {}}
	selection := svc.resilienceOpenAILastResort(ctx, OpenAIAccountScheduleRequest{
		GroupID: &groupID, Platform: PlatformOpenAI, SessionHash: session, RequestedModel: "gpt-5.5", ExcludedIDs: failedAccountIDs,
	}, avoided)
	require.Nil(t, selection, "last resort cannot return an account already failed in this request")
}
