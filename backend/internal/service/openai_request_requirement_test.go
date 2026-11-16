//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func chatRequirementAccount(id int64, platform, model, protocol string) Account {
	return Account{ID: id, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: int(id), GroupIDs: []int64{7},
		Credentials: map[string]any{"api_key": "test", "api_protocol": protocol, "model_mapping": map[string]any{"dispatch": model}},
		Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: protocol == APIProtocolResponses}}
}

func chatRequirementContext(model, effort, field string, mapping []ReasoningEffortMapping, responsesShape bool) context.Context {
	request := map[string]any{"model": model, "messages": []any{map[string]string{"role": "user", "content": "hello"}}}
	if responsesShape {
		delete(request, "messages")
		request["input"] = "hello"
	}
	if effort != "" {
		request["reasoning_effort"] = effort
	}
	if field == "functions" {
		request[field] = []any{map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}
	} else if field != "" {
		if responsesShape {
			request[field] = []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}
		} else {
			request[field] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}
		}
	}
	body, _ := json.Marshal(request)
	ctx := m1EffortContext(body)
	ctx = WithOpenAIReasoningEffortPolicy(ctx, "", mapping, "")
	return WithChatRequestRequirementFromBody(ctx, body, "dispatch")
}

func TestChatResponsesRequirementSelection(t *testing.T) {
	for _, tc := range []struct {
		name, model, effort, field string
		mappings                   []ReasoningEffortMapping
		wantResponses              bool
	}{
		{name: "sol61_tools", model: "gpt-6.1-sol", field: "tools", wantResponses: true},
		{name: "terra_tools", model: "gpt-6.1-terra", effort: "none", field: "tools", wantResponses: true},
		{name: "sol61_functions", model: "gpt-6.1-sol", effort: "none", field: "functions", wantResponses: true},
		{name: "sol6_default", model: "gpt-6-sol", field: "tools", wantResponses: true},
		{name: "luna_default", model: "gpt-6-luna", field: "functions", wantResponses: true},
		{name: "sol6_none", model: "gpt-6-sol", effort: "none", field: "tools"},
		{name: "sol6_mapped_none", model: "gpt-6-sol", effort: "high", field: "tools", mappings: []ReasoningEffortMapping{{From: "high", To: "none", Model: "gpt-6-sol", MatchType: "exact"}}},
		{name: "sol6_none_mapped_high", model: "gpt-6-sol", effort: "none", field: "tools", mappings: []ReasoningEffortMapping{{From: "none", To: "high"}}, wantResponses: true},
		{name: "ordinary_text", model: "gpt-6.1-sol"},
		{name: "unrelated_tools", model: "custom-model", field: "tools"},
	} {
		for _, path := range []string{"ordinary", "sticky", "legacy", "db_recheck", "last_resort", "fallback_group"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				withResilienceSettings(t, nil)
				a := chatRequirementAccount(1, PlatformOpenAI, tc.model, APIProtocolChatCompletions)
				b := chatRequirementAccount(2, PlatformOpenAI, tc.model, APIProtocolResponses)
				if path == "last_resort" {
					until := time.Now().Add(time.Minute)
					b.OverloadUntil = &until
				}
				group := int64(7)
				if path == "fallback_group" {
					group = 19
					a.GroupIDs = []int64{group}
					b.GroupIDs = []int64{group}
				}
				f := newSelectionRuntimeFixture(t, a, b)
				ctx := chatRequirementContext("public-alias", tc.effort, tc.field, tc.mappings, false)
				session := ""
				if path == "sticky" || path == "legacy" {
					session = "s"
					f.cache.sessionBindings[f.svc.openAISessionCacheKey(session)] = 1
				}
				if path == "db_recheck" {
					stale := a
					stale.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true}
					f.svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&stale, &b}, accountsByID: map[int64]*Account{1: &stale, 2: &b}}}
				}
				var result *AccountSelectionResult
				var err error
				if path == "legacy" {
					result, err = f.svc.SelectAccountWithLoadAwareness(ctx, &group, session, "dispatch", nil)
				} else {
					result, _, err = f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "dispatch", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				want := int64(1)
				if tc.wantResponses {
					want = 2
				}
				require.Equal(t, want, result.Account.ID)
				if tc.wantResponses {
					require.NotContains(t, f.attempts, int64(1))
				}
				if path == "last_resort" && tc.wantResponses {
					require.Equal(t, int64(2), resilience.StateFrom(ctx).LastResort())
				}
				require.Empty(t, resilience.SnapshotConversionNotices(ctx), "rejected candidate checks must not emit wire conversion notices")
				original := ctx.Value(finalReasoningRequestKey{}).(*finalReasoningRequest)
				require.False(t, original.appliedSet, "selection must not cache a rejected account's policy")
				result.ReleaseFunc()
			})
		}
	}
	t.Run("actual_protocol", func(t *testing.T) {
		for _, tc := range []struct {
			name, platform, protocol string
			shape, rule, probeFalse  bool
			want                     int64
		}{
			{"fixed_chat", PlatformKimi, APIProtocolChatCompletions, false, false, false, 2},
			{"adaptive_chat", PlatformKimi, APIProtocolAdaptive, false, false, false, 2},
			{"adaptive_responses_shape", PlatformKimi, APIProtocolAdaptive, true, false, false, 1},
			{"fixed_responses_ignores_old_probe", PlatformKimi, APIProtocolResponses, false, false, true, 1},
			{"model_route_chat", PlatformOpenCodeGo, APIProtocolAdaptive, false, true, false, 2},
			{"model_route_responses_ignores_old_probe", PlatformOpenCodeGo, APIProtocolAdaptive, false, false, true, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				withResilienceSettings(t, nil)
				a := chatRequirementAccount(1, tc.platform, "gpt-6.1-sol", tc.protocol)
				b := chatRequirementAccount(2, tc.platform, "gpt-6.1-sol", APIProtocolResponses)
				if tc.platform == PlatformOpenCodeGo {
					protocol := APIProtocolResponses
					if tc.rule {
						protocol = APIProtocolChatCompletions
					}
					a.Credentials["protocol_rules"] = []any{map[string]any{"pattern": "gpt-*", "protocol": protocol}}
				}
				a.Extra[openai_compat.ExtraKeyResponsesSupported] = !tc.probeFalse
				f := newSelectionRuntimeFixture(t, a, b)
				ctx := chatRequirementContext("public", "none", "tools", nil, tc.shape)
				result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "", "dispatch", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true, tc.platform)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, tc.want, result.Account.ID)
				result.ReleaseFunc()
			})
		}
	})
	t.Run("capable_but_temporarily_blocked", func(t *testing.T) {
		for _, full := range []bool{false, true} {
			name := "hard_rate_limit"
			if full {
				name = "concurrency"
			}
			t.Run(name, func(t *testing.T) {
				withResilienceSettings(t, nil)
				a := chatRequirementAccount(1, PlatformOpenAI, "gpt-6.1-sol", APIProtocolChatCompletions)
				b := chatRequirementAccount(2, PlatformOpenAI, "gpt-6.1-sol", APIProtocolResponses)
				if !full {
					until := time.Now().Add(time.Hour)
					b.RateLimitResetAt = &until
				}
				f := newSelectionRuntimeFixture(t, a, b)
				if full {
					f.loads.acquireResults = map[int64]bool{2: false}
					f.loads.loadMap = map[int64]*AccountLoadInfo{2: {AccountID: 2, LoadRate: 100}}
				}
				ctx := WithOpenAISelectionNoWait(chatRequirementContext("public", "", "tools", nil, false))
				result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "", "dispatch", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true)
				require.Nil(t, result)
				require.ErrorIs(t, err, ErrNoAvailableAccounts)
				e, ok := SelectionExhaustionFromError(err)
				require.True(t, ok)
				require.Equal(t, 1, e.RequestSupported)
				require.False(t, IsCapabilityOnlySelectionError(err))
				reason := SelectionReasonRateLimited
				if full {
					reason = SelectionReasonConcurrencyFull
				}
				require.Equal(t, 1, e.Reasons[reason])
			})
		}
	})
	t.Run("tool_failure_never_poison_plain_text", func(t *testing.T) {
		withResilienceSettings(t, nil)
		a := chatRequirementAccount(1, PlatformOpenAI, "gpt-6.1-sol", APIProtocolChatCompletions)
		until := time.Now().Add(time.Minute)
		a.OverloadUntil = &until
		f := newSelectionRuntimeFixture(t, a)
		group := int64(7)
		selectNext := func(ctx context.Context) (*AccountSelectionResult, error) {
			result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "", "dispatch", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true)
			return result, err
		}
		for i := 0; i < 2; i++ {
			toolsCtx := chatRequirementContext("public", "", "tools", nil, false)
			result, err := selectNext(toolsCtx)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			e, ok := SelectionExhaustionFromError(err)
			require.True(t, ok)
			require.True(t, e.Known)
			require.Equal(t, 1, e.ModelSupported)
			require.Zero(t, e.RequestSupported)
			require.Equal(t, 1, e.Reasons[SelectionReasonResponsesRequired])
			require.False(t, resilience.StateFrom(toolsCtx).GroupNoAccount())
			plainCtx := chatRequirementContext("public", "", "", nil, false)
			result, err = selectNext(plainCtx)
			require.NoError(t, err)
			require.Equal(t, int64(1), result.Account.ID)
			result.ReleaseFunc()
		}
		plainCtx := chatRequirementContext("public", "", "", nil, false)
		variant := "openai:" + string(OpenAIUpstreamTransportHTTPSSE) + ":" + string(OpenAIEndpointCapabilityChatCompletions) + ":" + PlatformOpenAI
		resilience.ProcessNegativeCache().Put(selectionNegativeKey(plainCtx, &group, "dispatch", variant), group, resilience.CachedResponse{}, time.Minute, time.Now())
		_, err := selectNext(chatRequirementContext("public", "", "functions", nil, false))
		require.False(t, errors.Is(err, errNegativeCached))
		e, ok := SelectionExhaustionFromError(err)
		require.True(t, ok)
		require.Equal(t, 1, e.Reasons[SelectionReasonResponsesRequired])
	})
}

func TestChatRequestRequirementContext(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		if _, ok := ChatRequestRequirementFromContext(ctx); ok {
			t.Fatal("unexpected requirement")
		}
	}
	req := ChatRequestRequirement{OriginalModel: "public", DispatchModel: "mapped", ReasoningEffort: "none", HasTools: true, ToolField: "functions"}
	ctx := WithChatRequestRequirement(context.Background(), req)
	req.OriginalModel = "changed"
	got, ok := ChatRequestRequirementFromContext(ctx)
	if !ok || got.OriginalModel != "public" || got.DispatchModel != "mapped" || got.ReasoningEffort != "none" || !got.HasTools || got.ToolField != "functions" {
		t.Fatal(got, ok)
	}
	if _, ok := ChatRequestRequirementFromContext(WithChatRequestRequirement(nil, ChatRequestRequirement{})); !ok {
		t.Fatal("explicit empty requirement lost")
	}
}
