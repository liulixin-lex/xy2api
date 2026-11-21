//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestConversionReplayBodyBoundary(t *testing.T) {
	for _, tc := range []struct{ name, body, reason string }{
		{"text", `{"model":"fixture","input":"hello"}`, ""},
		{"functions", `{"model":"fixture","input":"hello","tools":[{"type":"function","name":"fixture","parameters":{"type":"object"}}]}`, ""},
		{"function_history", `{"model":"fixture","input":[{"type":"function_call","call_id":"fixture_call","name":"fixture","arguments":"{}"},{"type":"function_call_output","call_id":"fixture_call","output":"ok"}]}`, ""},
		{"previous", `{"model":"fixture","input":"hello","previous_response_id":"fixture"}`, "state_continuation"},
		{"conversation", `{"model":"fixture","input":"hello","conversation":{"id":"fixture"}}`, "state_continuation"},
		{"handoff", `{"model":"fixture","input":[{"type":"handoff"}]}`, "state_continuation"},
		{"encrypted", `{"model":"fixture","input":[{"type":"reasoning","summary":[],"encrypted_content":"fixture"}]}`, "state_continuation"},
		{"reference", `{"model":"fixture","input":[{"type":"item_reference","id":"fixture"}]}`, "state_continuation"},
		{"hosted", `{"model":"fixture","input":"hello","tools":[{"type":"function","name":"fixture"},{"type":"web_search"}]}`, "provider_tools"},
		{"custom", `{"model":"fixture","input":"hello","tools":[{"type":"custom","name":"fixture"}]}`, "custom_tools"},
		{"unknown_tool", `{"model":"fixture","input":"hello","tools":[{"type":"extension","name":"fixture"}]}`, "unrecognized_tools"},
		{"missing_name", `{"model":"fixture","input":"hello","tools":[{"type":"function"}]}`, "unrecognized_tools"},
		{"bad_parameters", `{"model":"fixture","input":"hello","tools":[{"type":"function","name":"fixture","parameters":[]}]}`, "unrecognized_tools"},
		{"tool_shape", `{"model":"fixture","input":"hello","tools":{}}`, "unrecognized_tools"},
		{"input_shape", `{"model":"fixture","input":[{"type":"future_input"}]}`, "unrecognized_input"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.reason, conversionReplayBodyReason([]byte(tc.body))) })
	}
	// The old transport-read policy still accepts custom tools and a missing
	// marker. The stricter conversion gate never replaces either predicate.
	require.True(t, requestBodyReplaySafe([]byte(`{"tools":[{"type":"custom"}]}`), false))
	require.False(t, requestReplayUnsafe(context.Background()))
}

func conversionRetryTestContext(t *testing.T, body string) (*gin.Context, *resilience.RequestState) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(c.Request.Context(), state)
	settings := resilience.Defaults()
	state.SetBudget(resilience.NewBudget(ctx, &settings, resilience.KindText, time.Now()))
	ctx = WithRequestReplaySafety(ctx, []byte(body), false)
	c.Request = c.Request.WithContext(WithOpenAIConversionRetry(ctx))
	InstallDeliveryTracking(c, time.Now())
	openAIConversionRetryFrom(c.Request.Context()).wire = []byte(`{"model":"fixture","input":"hello","stream":true}`)
	return c, state
}

func TestConversionRetryQualification(t *testing.T) {
	for _, name := range []string{"eligible", "heartbeat", "output", "tool_fragment", "usage", "missing_marker", "marker_false", "canceled", "budget", "deadline", "missing_budget", "protected", "protected_child", "no_migration", "responses_shaped_state", "already_used", "other_reason"} {
		t.Run(name, func(t *testing.T) {
			body := `{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`
			if name == "responses_shaped_state" {
				body = `{"model":"fixture","input":"hello","previous_response_id":"fixture"}`
			}
			c, state := conversionRetryTestContext(t, body)
			scope := openAIConversionRetryFrom(c.Request.Context())
			if name == "responses_shaped_state" {
				scope.wire = []byte(body)
			}
			usage := false
			err := &apicompat.UpstreamConversionError{SourceProtocol: "responses", Field: "output", Reason: "conflicting output identity"}
			switch name {
			case "heartbeat":
				c.Header("Content-Type", "text/event-stream")
				n, writeErr := c.Writer.WriteString(": ping\n\n")
				require.NoError(t, writeErr)
				NoteDeliveryHeartbeat(c, n)
			case "output", "tool_fragment":
				c.Header("Content-Type", "text/event-stream")
				fragment := "data: {\"choices\":[{\"delta\":{\"content\":\"fragment\"}}]}\n\n"
				if name == "tool_fragment" {
					fragment = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\"}}]}}]}\n\n"
				}
				_, writeErr := c.Writer.WriteString(fragment)
				require.NoError(t, writeErr)
			case "usage":
				usage = true
			case "missing_marker":
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestReplaySafetyContextKey{}, nil))
			case "marker_false":
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestReplaySafetyContextKey{}, false))
			case "canceled":
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			case "budget":
				for i := 0; i < state.Budget().MaxAttempts()-1; i++ {
					state.Budget().RecordAttemptAt(time.Now())
				}
			case "deadline":
				settings := resilience.Defaults()
				state.SetBudget(resilience.NewBudget(c.Request.Context(), &settings, resilience.KindText, time.Now().Add(-time.Hour)))
			case "missing_budget":
				state.SetBudget(nil)
			case "protected":
				c.Request = c.Request.WithContext(WithOpenAITransferProtected(c.Request.Context()))
			case "protected_child":
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{preserveBinding: true}))
			case "no_migration":
				c.Request = c.Request.WithContext(withOpenAINoStickyMigration(c.Request.Context()))
			case "already_used":
				scope.used = true
			case "other_reason":
				err.Reason = "ambiguous output identity"
			}
			retry := openAIConversionRetryError(c, &http.Response{StatusCode: 200}, &Account{ID: 1}, "fixture", usage, "", "conversion failed", err)
			if name == "eligible" || name == "heartbeat" {
				require.NotNil(t, retry)
				require.Equal(t, 200, retry.StatusCode)
				require.False(t, retry.ShouldReportAccountScheduleFailure())
				_, _, health := classifyOpenAIAPIKeyHealthFailure(retry)
				require.False(t, health)
			} else {
				require.Nil(t, retry)
			}
		})
	}
}

func captureConversionTestWire(t *testing.T, c *gin.Context, account *Account, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "https://fixture.invalid/v1/responses", strings.NewReader(body))
	require.NoError(t, err)
	_, err = (&OpenAIGatewayService{}).prepareOpenAIConversionWire(c.Request.Context(), c, r, account, APIProtocolChatCompletions)
	require.NoError(t, err)
	return r
}

func TestConversionRetryWireUsesFinalRequest(t *testing.T) {
	c, _ := conversionRetryTestContext(t, `{"model":"fixture"}`)
	scope := openAIConversionRetryFrom(c.Request.Context())
	original := `{"model":"fixture","input":"hello","top_p":0.8}`
	repaired := `{"model":"fixture","input":"hello"}`
	a := &Account{ID: 1}
	setCodexToolNameReverse(c, map[string]string{"alias": "before repair"})
	captureConversionTestWire(t, c, a, original)
	setCodexToolNameReverse(c, map[string]string{"alias": "name at send"})
	captureConversionTestWire(t, c, a, repaired)
	codexToolNameReverseFromContext(c)["alias"] = "changed after send"
	scope.replay = scope.wire
	setCodexToolNameReverse(c, map[string]string{"second": "new builder"})
	second := captureConversionTestWire(t, c, &Account{ID: 2}, original)
	actual, err := io.ReadAll(second.Body)
	require.NoError(t, err)
	require.Equal(t, repaired, string(actual))
	require.EqualValues(t, len(repaired), second.ContentLength)
	require.Equal(t, map[string]string{"alias": "name at send"}, codexToolNameReverseFromContext(c))
	require.Nil(t, scope.replay)
}

func TestConversionRetryCandidateWireScope(t *testing.T) {
	c, _ := conversionRetryTestContext(t, `{"model":"fixture"}`)
	svc := &OpenAIGatewayService{}
	first := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "source"}}
	next := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "destination"}}
	for _, tc := range []struct {
		wire       string
		compatible bool
	}{
		{`{"model":"fixture","input":"hello"}`, true},
		{`{"model":"fixture","input":"hello","prompt_cache_key":"scoped"}`, false},
	} {
		captureConversionTestWire(t, c, first, tc.wire)
		state := openAIConversionRetryFrom(c.Request.Context())
		require.Equal(t, tc.compatible, svc.matchesConversionWire(c.Request.Context(), next, state))
	}
	// A later edit to the selected row must not change the captured namespace.
	first.Credentials["chatgpt_account_id"] = "destination"
	require.False(t, svc.matchesConversionWire(c.Request.Context(), next, openAIConversionRetryFrom(c.Request.Context())))
}

func TestConversionRetryChecksRepairedWireShape(t *testing.T) {
	entry := `{"model":"fixture","input":[{"type":"reasoning","summary":[],"encrypted_content":"fixture_cipher"}]}`
	for _, repaired := range []bool{false, true} {
		c, _ := conversionRetryTestContext(t, entry)
		wire := entry
		if repaired {
			wire = `{"model":"fixture","input":"complete local history"}`
		}
		captureConversionTestWire(t, c, &Account{ID: 1}, wire)
		failure := openAIConversionRetryError(c, &http.Response{StatusCode: 200}, &Account{ID: 1}, "fixture", false, "", "conversion failed", &apicompat.UpstreamConversionError{SourceProtocol: "responses", Field: "output", Reason: "conflicting output identity"})
		require.Equal(t, repaired, failure != nil, "only the actual wire's continuation state controls the structural gate")
	}
}

func TestConversionRetryCandidatesRecheckEveryConstraint(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, changed := range []string{"protocol", "wire", "image_rewrite", "priority", "model", "profit"} {
		t.Run(changed, func(t *testing.T) {
			accounts := []Account{selectionFixtureAccount(10, 1), selectionFixtureAccount(2, 1), selectionFixtureAccount(3, 1), selectionFixtureAccount(4, 1)}
			now := time.Now()
			for i := range accounts {
				cost := upstreamCostTestAccount(accounts[i].ID, UpstreamBillingProbeStatusOK, 0.1, now.Add(-time.Minute), 30*time.Minute)
				profitControlTestAccountWithRate(cost, 0.1)
				accounts[i].Extra, accounts[i].RateMultiplier = cost.Extra, cost.RateMultiplier
			}
			invalidate := func(account *Account) {
				switch changed {
				case "protocol":
					account.Extra["openai_responses_supported"] = false
				case "image_rewrite":
					account.Credentials = map[string]any{"base_url": DefaultDeepseekBaseURL}
				case "wire":
					account.Extra["openai_passthrough"] = true
				case "priority":
					account.Priority = 2
				case "model":
					account.Credentials = map[string]any{"model_mapping": map[string]any{"m": "different"}}
				case "profit":
					cost := upstreamCostTestAccount(account.ID, UpstreamBillingProbeStatusOK, 0.9, now.Add(-time.Minute), 30*time.Minute)
					profitControlTestAccountWithRate(cost, 0.9)
					account.Extra, account.RateMultiplier = cost.Extra, cost.RateMultiplier
				}
			}
			invalidate(&accounts[2])
			f := newSelectionRuntimeFixture(t, accounts...)
			f.loads.after = func(id int64) {
				if id == 2 {
					invalidate(&f.repo.accounts[1])
				}
			}
			c, _ := conversionRetryTestContext(t, `{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
			captureConversionTestWire(t, c, &accounts[0], `{"model":"m","input":"hello"}`)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 7, platform: PlatformOpenAI, ceilingOn: true, threshold: 0.5, pricingAt: now}))
			failure := openAIConversionRetryError(c, &http.Response{StatusCode: 200}, &accounts[0], "m", false, "", "conversion failed", &apicompat.UpstreamConversionError{SourceProtocol: "responses", Field: "output", Reason: "conflicting output identity"})
			require.NotNil(t, failure)
			require.True(t, f.svc.StartOpenAIConversionRetry(c, &accounts[0], failure))
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(c.Request.Context(), ptrInt64Selection(7), "", "", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			require.Equal(t, int64(4), selected.Account.ID)
			require.Equal(t, []int64{2, 4}, f.attempts)
			require.Equal(t, []int64{2}, f.releases)
		})
	}
}

func TestConversionRetryCannotBypassBusySlot(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(2, 1))
	f.loads.acquireResults = map[int64]bool{2: false}
	c, _ := conversionRetryTestContext(t, `{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	captureConversionTestWire(t, c, &f.repo.accounts[0], `{"model":"m","input":"hello"}`)
	failure := openAIConversionRetryError(c, &http.Response{StatusCode: 200}, &f.repo.accounts[0], "m", false, "", "conversion failed", &apicompat.UpstreamConversionError{SourceProtocol: "responses", Field: "output", Reason: "conflicting output identity"})
	require.NotNil(t, failure)
	require.True(t, f.svc.StartOpenAIConversionRetry(c, &f.repo.accounts[0], failure))
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(c.Request.Context(), ptrInt64Selection(7), "", "", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformOpenAI)
	require.Error(t, err)
	require.Nil(t, selected)
	require.Contains(t, f.attempts, int64(2))
	require.NotContains(t, f.attempts, int64(10))
	require.Empty(t, f.releases, "a slot never acquired cannot be released or used")
}

func TestConversionRetryStickyBoundary(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, scenario := range []string{"ineligible", "no_candidate", "startup_budget"} {
		t.Run(scenario, func(t *testing.T) {
			f := selectionNewSessionFixture()
			c, state := conversionRetryTestContext(t, `{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`)
			target := f.candidates[0]
			state.SetStickyChange(&openAIStickyChange{groupID: 7, session: "session", key: f.binding.key, model: "fixture", kind: "owner_released", expected: StickySessionBinding{AccountID: f.owner.ID, Generation: "g1"}, target: target.ID, ownerReleased: true, ttl: time.Hour, cache: f.binding})
			conversion := &apicompat.UpstreamConversionError{SourceProtocol: "responses", Field: "output", Reason: "conflicting output identity"}
			if scenario == "ineligible" {
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestReplaySafetyContextKey{}, false))
			}
			failure := openAIConversionRetryError(c, &http.Response{StatusCode: 200}, target, "fixture", false, "", "conversion failed", conversion)
			if scenario == "ineligible" {
				require.Nil(t, failure)
				FinishOpenAIStickyForward(c.Request.Context(), target.ID, nil, conversion)
			} else {
				require.NotNil(t, failure)
				AbandonOpenAIStickyForwardForIdentityConflict(c.Request.Context(), target.ID)
				if scenario == "startup_budget" {
					for state.Budget().Attempts() < state.Budget().MaxAttempts()-1 {
						state.Budget().RecordAttemptAt(time.Now())
					}
				}
				started := (&OpenAIGatewayService{}).StartOpenAIConversionRetry(c, target, failure)
				require.Equal(t, scenario != "startup_budget", started)
				if started {
					WriteOpenAIConversionRetryFailure(c, failure, "no_candidate")
				}
				require.Nil(t, pendingOpenAIStickyChange(c.Request.Context()))
			}
			avoided, err := f.session.store.SessionAvoid(c.Request.Context(), 7, stickyChangeAvoidSession("session"))
			require.NoError(t, err)
			if scenario == "ineligible" {
				require.Contains(t, avoided, target.ID)
			} else {
				require.Empty(t, avoided)
				require.False(t, state.Avoided(target.ID))
			}
			require.Equal(t, f.owner.ID, f.binding.owner)
		})
	}
}
