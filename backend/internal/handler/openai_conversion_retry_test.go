//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newConversionRetryHandler(t *testing.T, upstream service.HTTPUpstream, mutate func([]service.Account), stores ...*conversionRetryRequestStore) *OpenAIGatewayHandler {
	t.Helper()
	accounts := []service.Account{
		{
			ID:          1,
			Name:        "responses-account-1",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    0,
			Credentials: map[string]any{"access_token": "token-1"},
		},
		{
			ID:          2,
			Name:        "responses-account-2",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    0,
			Credentials: map[string]any{"access_token": "token-2"},
		},
	}
	if mutate != nil {
		mutate(accounts)
	}
	accountRepo := openAIImagesFailoverAccountRepo{accounts: accounts}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	store := &conversionRetryRequestStore{}
	if len(stores) > 0 {
		store = stores[0]
	}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		store,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		nil,
		upstream,
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	concurrencyService := service.NewConcurrencyService(nil)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	handler.maxAccountSwitches = 10
	return handler
}

type conversionRetryRequestStore struct {
	gwUsageLogRepo
	service.GatewayRequestRepository
	unbilled []*service.GatewayUnbilledRequest
}

func (r *conversionRetryRequestStore) CreateUnbilledRequest(_ context.Context, row *service.GatewayUnbilledRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unbilled = append(r.unbilled, row)
	return nil
}

type conversionRetryUpstream struct {
	service.HTTPUpstream
	responses []string
	statuses  []int
	accounts  []int64
	bodies    [][]byte
	onSend    func(int)
	onClose   func(int)
}

func (u *conversionRetryUpstream) Do(req *http.Request, _ string, account int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.accounts = append(u.accounts, account)
	u.bodies = append(u.bodies, body)
	index := len(u.accounts) - 1
	service.RecordOpsUpstreamSendBoundary(req.Context(), account)
	if u.onSend != nil {
		u.onSend(index)
	}
	if index >= len(u.responses) {
		return nil, fmt.Errorf("unexpected third dispatch")
	}
	status := http.StatusOK
	if index < len(u.statuses) {
		status = u.statuses[index]
	}
	bodyReader := io.ReadCloser(io.NopCloser(strings.NewReader(u.responses[index])))
	if u.onClose != nil {
		bodyReader = &conversionCloseBody{ReadCloser: bodyReader, close: func() { u.onClose(index) }}
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: bodyReader}, nil
}

type conversionCloseBody struct {
	io.ReadCloser
	close func()
	once  sync.Once
}

func (b *conversionCloseBody) Close() error { b.once.Do(b.close); return b.ReadCloser.Close() }

func conversionConflictStream(output bool) string {
	stream := "data: " + `{"type":"response.output_item.added","output_index":0,"item":{"id":"fixture_item_a","type":"message","role":"assistant","content":[]}}` + "\n\n"
	if output {
		stream += "data: " + `{"type":"response.output_text.delta","item_id":"fixture_item_a","output_index":0,"content_index":0,"delta":"visible-before-conflict"}` + "\n\n"
	}
	stream += "data: " + `{"type":"response.output_item.added","output_index":0,"item":{"id":"fixture_item_b","type":"message","role":"assistant","content":[]}}` + "\n\n"
	stream += "data: " + `{"type":"response.completed","response":{"id":"fixture_response","status":"completed","output":[]}}` + "\n\n"
	return stream
}

const conversionSuccessStream = "data: " + `{"type":"response.completed","response":{"id":"fixture_success","status":"completed","output":[{"id":"fixture_final","type":"message","role":"assistant","content":[{"type":"output_text","text":"retry-success"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"

type conversionRetryLogs struct {
	mu      sync.Mutex
	entries []*logger.LogEvent
}

func (s *conversionRetryLogs) WriteLogEvent(e *logger.LogEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Message == "openai_conversion_retry" || e.Message == "upstream_conversion_failed" || e.Message == "billing.usage_task_panic" {
		s.entries = append(s.entries, e)
	}
}
func (s *conversionRetryLogs) events(name string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []map[string]any
	for _, entry := range s.entries {
		if entry.Message == name {
			result = append(result, entry.Fields)
		}
	}
	return result
}

type conversionAttemptRecorder struct{ rows []service.SchedAttemptFinish }
type conversionAttempt struct{ owner *conversionAttemptRecorder }

func (r *conversionAttemptRecorder) Begin(service.SchedAttemptStart) service.SchedAttempt {
	return &conversionAttempt{owner: r}
}
func (*conversionAttemptRecorder) SetObserver(func(schedquality.AttemptSample)) {}
func (*conversionAttemptRecorder) Flush(context.Context) error                  { return nil }
func (*conversionAttempt) MarkOutput(service.SchedOutputKind, time.Time)        {}
func (a *conversionAttempt) Finish(in service.SchedAttemptFinish) {
	a.owner.rows = append(a.owner.rows, in)
}

func TestChatConversionRetryEndToEnd(t *testing.T) {
	withHandlerResilience(t, nil)
	previous := slog.Default()
	logger.InitBootstrap()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"success", "functions", "tool_prefix", "second_conflict", "second_http_error", "second_protocol_fallback", "after_output", "heartbeat", "no_candidate", "candidate_protocol", "budget_limit", "same_account_repair", "cancel_before_retry", "cancel_second"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, scenario), func(t *testing.T) {
				sink := &conversionRetryLogs{}
				logger.SetSink(sink)
				t.Cleanup(func() { logger.SetSink(nil) })
				upstream := &conversionRetryUpstream{responses: []string{conversionConflictStream(false), conversionSuccessStream}}
				if scenario == "second_conflict" {
					upstream.responses[1] = conversionConflictStream(false)
				}
				if scenario == "second_http_error" {
					upstream.responses[1] = `{"error":{"message":"fixture failure"}}`
					upstream.statuses = []int{200, 503}
				}
				if scenario == "second_protocol_fallback" {
					upstream.responses[1] = `{"error":{"message":"responses unsupported"}}`
					upstream.statuses = []int{200, 404}
				}
				if scenario == "after_output" {
					upstream.responses[1] = conversionConflictStream(true)
				}
				mutate := func(accounts []service.Account) {
					if scenario == "second_protocol_fallback" {
						for i := range accounts {
							accounts[i].Type = service.AccountTypeAPIKey
							accounts[i].Credentials = map[string]any{"api_key": "fixture"}
						}
					}
					if scenario == "no_candidate" {
						accounts[1].Priority = 1
					}
					if scenario == "candidate_protocol" {
						accounts[1].Type = service.AccountTypeAPIKey
						accounts[1].Credentials = map[string]any{"api_key": "fixture", "api_protocol": "chat_completions"}
						accounts[1].Extra = map[string]any{"openai_responses_supported": false}
					}
				}
				requestStore := &conversionRetryRequestStore{}
				h := newConversionRetryHandler(t, upstream, mutate, requestStore)
				attempts := &conversionAttemptRecorder{}
				h.gatewayService.SetSchedAttemptRecorder(attempts)
				ctx, cancel := context.WithCancel(budgetCtx(t, resilience.KindText, 3131))
				defer cancel()
				c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
				body := fmt.Sprintf(`{"model":"gpt-5.4","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream)
				if scenario == "functions" || scenario == "tool_prefix" {
					body = strings.TrimSuffix(body, "}") + `,"tools":[{"type":"function","function":{"name":"fixture","parameters":{"type":"object"}}}]}`
				}
				if scenario == "tool_prefix" {
					upstream.responses[0] = "data: " + `{"type":"response.output_item.added","output_index":1,"item":{"id":"fixture_stale_tool","type":"function_call","call_id":"fixture_stale_call","name":"fixture_stale_name","arguments":""}}` + "\n\n" + upstream.responses[0]
				}
				if scenario == "same_account_repair" {
					body = strings.TrimSuffix(body, "}") + `,"top_p":0.8}`
					upstream.responses = []string{`{"error":{"message":"Unsupported parameter: top_p","param":"top_p"}}`, conversionConflictStream(false), conversionSuccessStream}
					upstream.statuses = []int{400, 200, 200}
				}
				c.Request.URL.Path = "/v1/chat/completions"
				c.Request.Body = io.NopCloser(strings.NewReader(body))
				c.Request.ContentLength = int64(len(body))
				service.InstallDeliveryTracking(c, time.Now())
				upstream.onSend = func(index int) {
					if scenario == "heartbeat" && stream && index == 0 {
						c.Header("Content-Type", "text/event-stream")
						n, err := c.Writer.WriteString(": heartbeat\n\n")
						require.NoError(t, err)
						service.NoteDeliveryHeartbeat(c, n)
					}
					if scenario == "cancel_second" && index == 1 {
						cancel()
					}
				}
				if scenario == "cancel_before_retry" {
					upstream.onClose = func(index int) {
						if index == 0 {
							cancel()
						}
					}
				}
				if scenario == "budget_limit" {
					upstream.onClose = func(index int) {
						if index == 0 {
							b := resilience.StateFrom(c.Request.Context()).Budget()
							for b.Attempts() < b.MaxAttempts()-1 {
								b.RecordAttemptAt(time.Now())
							}
						}
					}
				}
				h.ChatCompletions(c)
				wantCalls := 2
				if scenario == "no_candidate" || scenario == "candidate_protocol" || scenario == "budget_limit" || scenario == "cancel_before_retry" || scenario == "tool_prefix" && stream {
					wantCalls = 1
				}
				if scenario == "same_account_repair" {
					wantCalls = 3
				}
				require.Len(t, upstream.accounts, wantCalls, rec.Body.String())
				require.Equal(t, 1, len(requestStore.recorded())+len(requestStore.unbilled), "one request terminal across billed and unbilled records")
				if wantCalls > 1 && scenario != "same_account_repair" {
					require.NotEqual(t, upstream.accounts[0], upstream.accounts[1])
					require.Equal(t, upstream.bodies[0], upstream.bodies[1], "replay must use exact final wire body")
				}
				if scenario == "same_account_repair" {
					require.Equal(t, upstream.accounts[0], upstream.accounts[1])
					require.NotEqual(t, upstream.accounts[1], upstream.accounts[2])
					require.Equal(t, upstream.bodies[1], upstream.bodies[2])
					require.NotContains(t, string(upstream.bodies[2]), "top_p")
				}
				if scenario == "success" || scenario == "functions" || scenario == "tool_prefix" && !stream || scenario == "heartbeat" || scenario == "same_account_repair" {
					require.Contains(t, rec.Body.String(), "retry-success")
					require.NotContains(t, rec.Body.String(), `"error"`)
					require.NotContains(t, rec.Body.String(), "fixture_stale_")
				}
				if scenario == "after_output" && stream {
					require.Contains(t, rec.Body.String(), "visible-before-conflict")
				}
				if scenario != "cancel_before_retry" && scenario != "cancel_second" {
					require.True(t, service.ProtocolTerminalClaimed(c))
				}
				require.LessOrEqual(t, strings.Count(rec.Body.String(), `"error":`), 1, "the first attempt cannot leave an error terminal before replay")
				require.Empty(t, sink.events("billing.usage_task_panic"))
				events := sink.events("openai_conversion_retry")
				require.Len(t, events, 1)
				if scenario == "tool_prefix" && stream {
					require.Equal(t, "output_started", events[0]["skip_reason"])
					require.Contains(t, rec.Body.String(), "fixture_stale_name")
				}
				if scenario == "budget_limit" {
					require.Equal(t, "budget_exhausted", events[0]["skip_reason"])
				}
				if scenario == "candidate_protocol" {
					require.Equal(t, "candidate_protocol", events[0]["skip_reason"])
				}
				if scenario == "no_candidate" {
					require.Equal(t, "no_candidate", events[0]["skip_reason"])
				}
				if scenario == "second_conflict" {
					require.Equal(t, "conversion_error", events[0]["result"])
				}
				if scenario == "second_http_error" || scenario == "second_protocol_fallback" {
					require.Equal(t, "upstream_error", events[0]["result"])
				}
				if scenario == "after_output" && stream {
					require.Equal(t, "interrupted", events[0]["result"])
				}
				if scenario == "success" || scenario == "functions" || scenario == "tool_prefix" && !stream || scenario == "heartbeat" {
					require.Len(t, attempts.rows, 2)
					require.Equal(t, schedquality.ClassUnknown, attempts.rows[0].Class)
					require.Equal(t, 200, attempts.rows[0].HTTPStatus)
					require.Equal(t, schedquality.OutcomeOK, attempts.rows[1].Outcome)
				}
				require.LessOrEqual(t, resilience.StateFrom(c.Request.Context()).Budget().Attempts(), resilience.StateFrom(c.Request.Context()).Budget().MaxAttempts())
				t.Logf("calls=%v client=%d retry=%v", upstream.accounts, rec.Code, events[0])
			})
		}
	}
}

func TestConversionRetryConsumesBudgetWithoutPenalties(t *testing.T) {
	withHandlerResilience(t, nil)
	ctx := budgetCtx(t, resilience.KindText, 7)
	recorder := &countingRecorder{failures: map[int64]int{}}
	state := resilience.StateFrom(ctx)
	state.SetSession(7, "fixture", recorder)
	failure := &service.UpstreamFailoverError{StatusCode: 200, Reason: service.OpenAIConversionIdentityReason, Scope: service.GatewayFailureScopeRequest}
	require.False(t, resilienceAttemptFailed(ctx, 1, failure))
	require.Equal(t, 1, state.Budget().Attempts())
	resilienceReportAttemptFailure(ctx, 1, failure)
	require.Empty(t, recorder.failures)
	require.False(t, failure.ShouldReportAccountScheduleFailure())
	excluded := map[int64]struct{}{1: {}}
	require.False(t, resilienceBeforeSwitch(ctx, &hostSharingUnscheduler{sameHost: map[int64][]int64{1: {1, 2}}}, 1, failure, excluded))
	require.Equal(t, map[int64]struct{}{1: {}}, excluded)
}

func conversionAPIKey(a *service.Account) {
	a.Type = service.AccountTypeAPIKey
	a.Credentials = map[string]any{"api_key": "fixture", "api_protocol": "responses"}
	a.Extra = map[string]any{"openai_responses_supported": true}
}

func runConversionChat(t *testing.T, h *OpenAIGatewayHandler, body string) (string, map[string]any) {
	t.Helper()
	sink := &conversionRetryLogs{}
	previous := slog.Default()
	logger.InitBootstrap()
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil); slog.SetDefault(previous) })
	c, rec := newOpenAIResponsesFailoverTestContext(t, budgetCtx(t, resilience.KindText, 3131))
	c.Request.URL.Path = "/v1/chat/completions"
	c.Request.Body = io.NopCloser(strings.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	service.InstallDeliveryTracking(c, time.Now())
	h.ChatCompletions(c)
	events := sink.events("openai_conversion_retry")
	require.Len(t, events, 1)
	return rec.Body.String(), events[0]
}

func TestConversionReplayDuplicateKeysAtDispatch(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name, fields, preserved string
		duplicate               bool
	}{
		{"previous_response", `"input":"hello","previous_response_id":"","previous_response_id":""`, `"previous_response_id":"","previous_response_id":""`, true},
		{"conversation", `"input":"hello","conversation":null,"conversation":null`, `"conversation":null,"conversation":null`, true},
		{"tools", `"input":"hello","tools":[],"tools":[]`, `"tools":[],"tools":[]`, true},
		{"nested_tool", `"input":"hello","tools":[{"type":"function","name":"fixture","name":"fixture"}]`, `"name":"fixture","name":"fixture"`, true},
		{"nested_input_escaped_name", `"input":[{"role":"user","content":[{"type":"input_text","text":"a","\u0074ext":"a"}]}]`, `"text":"a","\u0074ext":"a"`, true},
		{"separate_objects", `"input":[{"role":"user","content":"a"},{"role":"user","content":"b"}]`, `{"role":"user","content":"b"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &conversionRetryUpstream{responses: []string{conversionConflictStream(false), conversionSuccessStream}}
			h := newConversionRetryHandler(t, up, func(as []service.Account) {
				for i := range as {
					conversionAPIKey(&as[i])
				}
			})
			output, event := runConversionChat(t, h, `{"model":"gpt-5.4",`+tc.fields+`}`)
			require.NotEmpty(t, up.bodies)
			require.Contains(t, string(up.bodies[0]), tc.preserved, "gate is tested on the actual bytes sent")
			if tc.duplicate {
				require.Len(t, up.bodies, 1)
				require.Equal(t, "duplicate_key", event["skip_reason"])
				require.Equal(t, false, event["retried"])
				require.NotContains(t, output, "retry-success")
			} else {
				require.Len(t, up.bodies, 2)
				require.Equal(t, "success", event["result"])
			}
		})
	}
}

func TestReviewConversionToolAliasAcrossAccountTypes(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, stream := range []bool{false, true} {
		for _, from := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
			for _, to := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
				t.Run(fmt.Sprintf("%s_to_%s/stream=%t", from, to, stream), func(t *testing.T) {
					up := &conversionRetryUpstream{responses: []string{conversionConflictStream(false), ""}}
					h := newConversionRetryHandler(t, up, func(as []service.Account) {
						for i, kind := range []string{from, to} {
							if kind == service.AccountTypeAPIKey {
								conversionAPIKey(&as[i])
							}
						}
					})
					up.onSend = func(i int) {
						if i == 1 {
							name := gjson.GetBytes(up.bodies[i], "tools.0.name").String()
							up.responses[i] = "data: " + fmt.Sprintf(`{"type":"response.completed","response":{"id":"result","status":"completed","output":[{"id":"item","type":"function_call","call_id":"call","name":%q,"arguments":"{}","status":"completed"}]}}`, name) + "\n\n"
						}
					}
					body := fmt.Sprintf(`{"model":"gpt-5.4","stream":%t,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"python","parameters":{"type":"object"}}}]}`, stream)
					out, event := runConversionChat(t, h, body)
					require.NotEmpty(t, up.bodies)
					wireName := gjson.GetBytes(up.bodies[0], "tools.0.name").String()
					require.NotEmpty(t, wireName)
					require.Equal(t, from == service.AccountTypeAPIKey, wireName == "python")
					if from != to {
						require.Len(t, up.accounts, 1)
						require.Equal(t, "candidate_wire", event["skip_reason"])
						require.Equal(t, false, event["retried"])
						return
					}
					require.Equal(t, []int64{1, 2}, up.accounts)
					require.Equal(t, up.bodies[0], up.bodies[1])
					require.Equal(t, "success", event["result"])
					require.Contains(t, out, `"name":"python"`)
					if wireName != "python" {
						require.NotContains(t, out, wireName)
					}
				})
			}
		}
	}
}
