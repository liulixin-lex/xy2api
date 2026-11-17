//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCaptureReviewWSPassthroughCachePresence(t *testing.T) {
	for _, tc := range []struct {
		name, details string
		want          *int
	}{
		{"zero", `,"input_tokens_details":{"cached_tokens":0}`, captureInt(0)},
		{"positive", `,"input_tokens_details":{"cached_tokens":800}`, captureInt(800)},
		{"missing", "", nil},
		{"missing_response_shell", "", nil},
		{"missing_done_shell", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newStagedPassthroughConn()
			if tc.name == "missing_response_shell" {
				upstream.Send(`{"type":"response.completed","response_id":"resp_fixture"}`)
			} else if tc.name == "missing_done_shell" {
				upstream.Send(`{"type":"response.done","response_id":"resp_fixture"}`)
			} else {
				upstream.Send(`{"type":"response.completed","response":{"id":"resp_fixture","model":"gpt-5.1","usage":{"input_tokens":1000,"output_tokens":2` + tc.details + `}}}`)
			}
			svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
			account := passthroughLifecycleAccount()
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "gpt-5.1"})
			ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), st))
			defer cancel()
			store := &fakeSchedAttemptStore{}
			rec := NewSchedAttemptRecorderService(store)
			rec.Start(ctx)
			defer rec.Stop()
			rows := make(chan q.AttemptSample, 1)
			rec.SetObserver(func(row q.AttemptSample) {
				if row.FinishedAt.IsZero() {
					return
				}
				select {
				case rows <- row:
				default:
					t.Error("duplicate finished attempt")
				}
			})
			svc.SetSchedAttemptRecorder(rec)
			completed := make(chan *OpenAIForwardResult, 1)
			server, _ := startPassthroughHookRecordingServer(t, ctx, svc, account, &OpenAIWSIngressHooks{
				AfterTurn: func(_ int, result *OpenAIForwardResult, _ error) { completed <- result },
			})
			defer server.Close()
			client := dialPassthroughLifecycleClient(t, server)
			defer func() { _ = client.CloseNow() }()
			_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
			require.NoError(t, err)
			var result *OpenAIForwardResult
			select {
			case result = <-completed:
			case <-time.After(3 * time.Second):
				t.Fatal("OnTurnComplete did not finish the attempt")
			}
			var row q.AttemptSample
			select {
			case row = <-rows:
			default:
				t.Fatal("OnTurnComplete returned without a finished sample")
			}
			require.Equal(t, q.OutcomeOK, row.Outcome)
			require.Equal(t, tc.want != nil, result.Usage.CacheReadInputTokensPresent)
			require.Equal(t, tc.want, row.CacheReadTokens)
			require.NoError(t, rec.Flush(ctx))
			require.Len(t, store.Rows, 1)
			require.Equal(t, tc.want, store.Rows[0].CacheReadTokens)
			st.CompleteGatewayForward(result.CompletedForDelivery(), false)
			meta, _ := st.ClaimGatewayRequest(false)
			require.Equal(t, tc.want, meta.CacheReadTokens)
		})
	}
}

func TestCaptureReviewRawChatResetAfterFinish(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, finished := range []bool{false, true} {
		t.Run(fmt.Sprint(finished), func(t *testing.T) {
			wire := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
			if finished {
				wire += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":2}}\n\n"
			}
			c, _ := compatStreamContext("/v1/chat/completions", "")
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.NoteGatewaySend(11)
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 11, Model: "fixture"})
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
			rec := &captureServiceRecorder{}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), schedAttemptRecorder: rec}
			account := &Account{ID: 11, Platform: PlatformOpenAI}
			a := svc.beginSchedAttempt(c.Request.Context(), account, nil, 0, true)
			a.expectTerminal = true
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &openAIChatStreamReadErrorCloser{payload: []byte(wire), err: syscall.ECONNRESET}}
			result, err := svc.streamRawChatCompletions(c, resp, account, "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
			a.finish(result, err)
			require.Len(t, rec.samples, 1)
			if finished {
				require.NoError(t, err)
				require.Equal(t, q.OutcomeOK, rec.samples[0].Outcome)
				st.CompleteGatewayForward(true, false)
				meta, _ := st.ClaimGatewayRequest(false)
				require.Equal(t, resilience.RequestSuccess, meta.Result)
			} else {
				require.Error(t, err)
				require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
			}
		})
	}
}

func TestCaptureReviewFinishedOwnerCannotRewriteResult(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "messages"} {
		t.Run(target, func(t *testing.T) {
			req := newWebSearchReplayRequest(t)
			account := webSearchReplayAccount(false)
			resilience.StateFrom(req.ctx).SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "fixture"})
			body := []byte(`{"model":"fixture","stream":false,"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad-A","summary":[]},{"role":"user","content":"hi"}]}`)
			param := "input[0].encrypted_content"
			req.c.Request.URL.Path = "/v1/chat/completions"
			if target == "messages" {
				req.c.Request.URL.Path = "/v1/messages"
				body = []byte(`{"model":"fixture","stream":false,"max_tokens":32,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"one","signature":"bad-A"},{"type":"thinking","thinking":"two","signature":"good-B"}]},{"role":"user","content":"hi"}]}`)
				param = "input[1].encrypted_content"
			}
			wire := m7ReviewWire(`{"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"invalid_encrypted_content","param":"` + param + `","message":"Encrypted content could not be verified"}}}`)
			var old *schedAttemptCapture
			first := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &captureCloseBody{
				ReadCloser: io.NopCloser(strings.NewReader(wire)),
				close: func() {
					if a := currentSchedAttempt(req.ctx); old == nil && a != nil {
						old = a
					}
				},
			}}
			second := webSearchSuccessStream("resp_repaired")
			probed := false
			second.Body = &captureCloseBody{ReadCloser: second.Body, close: func() {
				// Mark the retired handle after final delivery. Only a stale
				// deferred finish could copy this probe into the final result.
				if old != nil {
					old.mu.Lock()
					old.benignIncomplete = true
					old.mu.Unlock()
					probed = true
				}
			}}
			up := &httpUpstreamRecorder{responses: []*http.Response{first, second}}
			svc := newOpenAIImageGenerationControlTestService(up)
			svc.cfg.JWT.Secret = "fixture-secret"
			svc.SetSchedAttemptRecorder(&captureServiceRecorder{})
			result, err := captureForward(svc, req.c, account, body, target)
			require.NoError(t, err)
			require.Len(t, up.requests, 2)
			require.True(t, probed)
			require.False(t, result.BenignIncomplete, "retired send's defer must be blocked by a == schedCapture")
			require.True(t, result.CompletedForDelivery())
		})
	}
}

func TestCaptureReviewDrainedTerminalEnrichesFinishedAttempt(t *testing.T) {
	for _, tc := range []struct {
		payload, event  string
		benign, success bool
	}{
		{`{"type":"response.failed","response":{"status":"failed"}}`, "response.failed", false, false},
		{`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"interrupted"}}}`, "response.incomplete", false, false},
		{`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "response.incomplete", true, true},
	} {
		t.Run(tc.event+fmt.Sprint(tc.benign), func(t *testing.T) {
			a, rec := newClassificationCapture()
			a.cancel()                    // Downstream write failed, so the sample is already finished.
			a.observe([]byte(tc.payload)) // The gateway keeps draining the upstream.
			result := &OpenAIForwardResult{}
			a.finish(result, nil)
			require.Equal(t, tc.event, result.UpstreamTerminalEvent)
			require.Equal(t, tc.benign, result.BenignIncomplete)
			require.Equal(t, tc.success, result.SucceededForScheduling())
			require.Len(t, rec.samples, 1)
			require.Equal(t, q.Outcome(5), rec.samples[0].Outcome)
		})
	}
}

func TestCaptureReviewCompletedWithErrorIsQualityFailure(t *testing.T) {
	for _, payload := range []string{
		`{"status":"completed","error":{"message":"fixture failure"}}`,
		`{"type":"response.completed","response":{"status":"completed"},"error":{"message":"fixture failure"}}`,
	} {
		a, rec := newClassificationCapture()
		a.observe([]byte(payload))
		a.finish(&OpenAIForwardResult{}, nil)
		require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
	}
}

func TestCaptureReviewAuthoritativeFailureStaysSticky(t *testing.T) {
	a, rec := newClassificationCapture()
	a.observe([]byte(`{"type":"response.failed","response":{"status":"failed","error":{"message":"failed"}}}`))
	a.observe([]byte(`{"type":"error","error":{"message":"retrying"}}`))
	a.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`))
	a.finish(&OpenAIForwardResult{}, nil)
	require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
}

func TestCaptureReviewCacheAliases(t *testing.T) {
	for _, field := range []string{`"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}`, `"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":200`} {
		u := parseClaudeUsageFromResponseBody([]byte(`{"usage":{"input_tokens":200,"output_tokens":2,` + field + `}}`))
		normalized := claudeUsageToOpenAIUsage(u)
		require.Equal(t, 800, normalized.CacheReadInputTokens)
		require.True(t, schedUsage(&normalized).CacheReadTokensPresent)
	}
}

func TestCaptureReviewNativeBufferedDisconnect(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "responses", "messages"} {
		t.Run(target, func(t *testing.T) {
			c, _ := compatStreamContext("/v1/"+target, "")
			c.Writer = &openAIChatFailingWriter{ResponseWriter: c.Writer}
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			ctx := resilience.WithRequestState(c.Request.Context(), st)
			fixture := selectionNewSessionFixture()
			selection := fixture.session.selectOwner(ctx, 7, "session", "fixture", fixture.owner, fixture.candidates, selectionRequest{})
			require.NotNil(t, selection)
			defer selection.ReleaseFunc()
			st.NoteGatewaySend(selection.Account.ID)
			st.SetSchedSelection(resilience.SchedSelection{AccountID: selection.Account.ID, Model: "fixture"})
			c.Request = c.Request.WithContext(ctx)
			recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
			var row q.AttemptSample
			recorder.SetObserver(func(sample q.AttemptSample) {
				if !sample.FinishedAt.IsZero() {
					row = sample
				}
			})
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), schedAttemptRecorder: recorder}
			account := selection.Account
			a := svc.beginSchedAttempt(c.Request.Context(), account, nil, 0, false)
			var result *OpenAIForwardResult
			var err error
			switch target {
			case "chat":
				result, err = svc.handleCCBufferedFromNativeAnthropic(nativeAnthropicStreamResponse(), c, account, "fixture", "fixture", "fixture", nil, time.Now())
			case "responses":
				result, err = svc.handleResponsesBufferedFromNativeAnthropic(nativeAnthropicStreamResponse(), c, account, "fixture", "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			default:
				result, err = svc.handleNativeAnthropicBufferedResponse(c.Request.Context(), nativeAnthropicBufferedResponse(), c, account, "fixture", "fixture", "fixture", nil, time.Now())
			}
			a.finish(result, err)
			FinishOpenAIStickyForward(ctx, account.ID, result, err)
			require.Equal(t, int64(10), fixture.binding.owner)
			require.Empty(t, fixture.binding.casKeys)
			avoided, avoidErr := fixture.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
			require.NoError(t, avoidErr)
			require.Empty(t, avoided, "a client disconnect must not penalize the candidate")
			require.Error(t, err)
			require.NotNil(t, result)
			require.True(t, result.ClientDisconnect)
			require.Equal(t, 93, result.Usage.InputTokens, "metered usage remains available to the handler")
			require.Equal(t, q.Outcome(5), row.Outcome)
			require.Equal(t, 93, *row.PromptTokens)
			st.CompleteGatewayForward(false, true)
			meta, _ := st.ClaimGatewayRequest(false)
			require.Equal(t, resilience.RequestCanceled, meta.Result)
		})
	}
}

func TestCaptureReviewResponsesOutcomeAgreement(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, fields string
			failure      bool
		}{
			{"empty_error", `"status":"completed","error":{}`, false},
			{"completed_error", `"status":"completed","error":{"message":"fixture failure"}`, false},
			{"json_with_sse_header", `"status":"completed"`, false},
			{"json_without_status", `"type":"response"`, false},
			{"incomplete", `"status":"incomplete","incomplete_details":{"reason":"fixture_failure"}`, true},
			{"missing_reason", `"status":"incomplete"`, true},
			{"interrupted", `"status":"incomplete","incomplete_details":{"reason":"interrupted"}`, true},
			{"benign", `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, false},
		} {
			if stream && tc.name == "json_without_status" {
				continue // This case exercises the JSON reader, not an SSE envelope.
			}
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":"fixture","input":"hello","stream":%v}`, stream))
				c := newOpenAIRejectedFieldTestContext(body)
				account := newOpenAIRejectedFieldTestAccount()
				st := resilience.NewRequestState()
				st.EnableGatewayRequest(time.Now())
				st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "fixture"})
				ctx := resilience.WithRequestState(c.Request.Context(), st)
				c.Request = c.Request.WithContext(ctx)
				wire := `{"id":"resp_fixture","object":"response",` + tc.fields + `,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":4,"output_tokens":2}}`
				contentType := "application/json"
				if tc.name == "json_with_sse_header" || tc.name == "json_without_status" {
					contentType = "text/event-stream"
				}
				if stream {
					event := "response.completed"
					if tc.failure || tc.name == "benign" {
						event = "response.incomplete"
					}
					extra := ""
					if tc.name == "completed_error" {
						extra = `,"error":{"message":"fixture failure"}`
					}
					wire = m7ReviewWire(`{"type":"` + event + `","response":` + wire + extra + `}`)
					contentType = "text/event-stream"
				}
				svc := newOpenAIRejectedFieldTestService(&httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(wire))}})
				rec := &captureServiceRecorder{}
				svc.SetSchedAttemptRecorder(rec)
				result, err := svc.Forward(ctx, c, account, body)
				require.Len(t, rec.samples, 1)
				if tc.name == "completed_error" {
					require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
				} else if tc.failure {
					require.NoError(t, err, "delivery of non-benign incomplete must retain its existing behavior")
					require.NotNil(t, result)
					require.Equal(t, 4, result.Usage.InputTokens)
					require.Equal(t, 2, result.Usage.OutputTokens)
					require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
					require.Equal(t, SchedAttemptDetailOutputIncomplete, rec.samples[0].Detail)
				} else {
					require.NoError(t, err)
					require.True(t, result.CompletedForDelivery())
					require.Equal(t, q.OutcomeOK, rec.samples[0].Outcome)
					st.CompleteGatewayForward(true, false)
					meta, _ := st.ClaimGatewayRequest(false)
					require.Equal(t, resilience.RequestSuccess, meta.Result)
				}
			})
		}
	}
}

func TestCaptureReviewProvisionalErrorRecovery(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"error", "response.failed"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := compatStreamContext("/v1/responses", "")
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.NoteGatewaySend(11)
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 11, Model: "fixture"})
			ctx := resilience.WithRequestState(c.Request.Context(), st)
			c.Request = c.Request.WithContext(ctx)
			rec := &captureServiceRecorder{}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), schedAttemptRecorder: rec}
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			a := svc.beginSchedAttempt(ctx, account, nil, 0, true)
			a.expectTerminal = true
			failure := `{"type":"error","error":{"code":"transient","message":"retrying"}}`
			if kind == "response.failed" {
				failure = `{"type":"response.failed","response":{"status":"failed","error":{"code":"transient","message":"failed"}}}`
			}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(m7ReviewWire(failure) + captureResponsesWire()))}
			result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "fixture", "fixture")
			forward := &OpenAIForwardResult{PartialFailure: err != nil}
			if result != nil && result.usage != nil {
				forward.Usage = *result.usage
			}
			a.finish(forward, err)
			require.Len(t, rec.samples, 1)
			if kind == "error" {
				require.NoError(t, err)
				require.Equal(t, q.OutcomeOK, rec.samples[0].Outcome)
				st.CompleteGatewayForward(true, false)
				meta, _ := st.ClaimGatewayRequest(false)
				require.Equal(t, resilience.RequestSuccess, meta.Result)
			} else {
				require.Error(t, err)
				require.Equal(t, q.OutcomeAccountFault, rec.samples[0].Outcome)
			}
		})
	}
}
