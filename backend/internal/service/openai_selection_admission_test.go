//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSchedTestAdmissionRequestModel(t *testing.T) {
	tests := []struct {
		name           string
		mapping        map[string]any
		evidence, want string
		ok             bool
	}{
		{"evidence_key", nil, "m", "m", true},
		{"mapped_only", map[string]any{"a": "b"}, "b", "a", true},
		{"lexical_mapping", map[string]any{"z": "b", "a": "b", "*": "b"}, "b", "b", true},
		{"lexical_without_wildcard", map[string]any{"z": "b", "a": "b"}, "b", "a", true},
		{"wildcard_is_not_reverse_key", map[string]any{"a*": "b"}, "b", "", false},
		{"no_mapping", map[string]any{"a": "c"}, "b", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &OpenAIGatewayService{}
			a := selectionTestAccount(1, 1)
			if tt.mapping != nil {
				a.Credentials = map[string]any{"model_mapping": tt.mapping}
			}
			model, ok, reason := s.TestRequestModel(context.Background(), a, tt.evidence)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, model)
			if !ok {
				require.Equal(t, "no_model", reason)
			} else {
				require.Empty(t, reason)
			}
		})
	}
}

func TestSchedTestAdmissionHardGatesAndNoMutation(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	tests := []struct {
		name   string
		setup  func(*OpenAIGatewayService, *Account)
		reason string
	}{
		{"rate_limited", func(s *OpenAIGatewayService, a *Account) { a.RateLimitResetAt = &future }, "not_schedulable"},
		{"disabled", func(s *OpenAIGatewayService, a *Account) { a.Schedulable = false }, "not_schedulable"},
		{"iq_gate", func(s *OpenAIGatewayService, a *Account) {
			a.Extra = map[string]any{"model_rate_limits": map[string]any{"iq:m": map[string]any{}}}
		}, "model_rate_limited"},
		{"model_limit", func(s *OpenAIGatewayService, a *Account) {
			setAccountModelRateLimitSnapshot(a, "m", future, "limited", now)
		}, "model_rate_limited"},
		{"transient", func(s *OpenAIGatewayService, a *Account) {
			s.getOpenAIAccountModelTransientState().recordFailure(a.ID, "m", now)
			s.getOpenAIAccountModelTransientState().recordFailure(a.ID, "m", now)
		}, "runtime_blocked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []string{}
			binding := &selectionBindingFake{owner: 10, events: &events}
			s := &OpenAIGatewayService{cache: &selectionBoardBindings{selectionBindingFake: binding}, concurrencyService: &ConcurrencyService{cache: &selectionBoardLoads{}}}
			a := selectionTestAccount(1, 1)
			tt.setup(s, a)
			state := s.getOpenAIAccountModelTransientState()
			before := make(map[openAIAccountModelKey]openAIAccountModelTransientEntry)
			for k, v := range state.entries {
				before[k] = v
			}
			_, ok, reason := s.TestRequestModel(context.Background(), a, "m")
			require.False(t, ok)
			require.Equal(t, tt.reason, reason)
			for k, v := range state.entries {
				old := before[k]
				v.lastTouched = old.lastTouched
				require.Equal(t, old, v)
			}
			require.Len(t, state.entries, len(before))
			require.Equal(t, int64(10), binding.owner)
			require.Zero(t, binding.reads)
			require.Empty(t, events)
		})
	}
	t.Run("ignores_group_profit_gate", func(t *testing.T) {
		s := &OpenAIGatewayService{}
		a := selectionTestAccount(1, 1)
		ctx := s.withOpenAIProfitControlGate(profitControlTestCtx(profitControlTestGroup(7, .9, 0)), ptrInt64Selection(7))
		veto, _ := openAIProfitControlVetoReason(ctx, a)
		require.True(t, veto)
		model, ok, reason := s.TestRequestModel(ctx, a, "m")
		require.True(t, ok)
		require.Equal(t, "m", model)
		require.Empty(t, reason)
	})
}
func selectionThresholdFixture(t *testing.T) (*OpenAIGatewayService, *Account, *rateLimitAccountRepoStub) {
	t.Helper()
	reset := func() {
		accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
		accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
	}
	reset()
	t.Cleanup(reset)
	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":80}`
	settings := NewSettingService(settingsRepo, &config.Config{})
	repo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(settings)
	a := selectionTestAccount(1, 1)
	a.Extra = map[string]any{
		"codex_7d_used_percent": 85.0,
		"codex_7d_reset_at":     time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	return &OpenAIGatewayService{rateLimitService: rl, settingService: settings}, a, repo
}

func TestSchedTestAdmissionSchedulingThreshold(t *testing.T) {
	s, account, repo := selectionThresholdFixture(t)
	decision := EvaluateAccountSchedulingThreshold(account, s.settingService.GetAccountSchedulingThresholds(context.Background()), time.Now())
	require.True(t, decision.ShouldPause)
	model, ok, reason := s.TestRequestModel(context.Background(), account, "m")
	require.False(t, ok)
	require.Empty(t, model)
	require.Equal(t, "account_scheduling_threshold", reason)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, account.ID, repo.lastTempID)
	require.Equal(t, decision.Until, account.TempUnschedulableUntil)
	require.True(t, IsAccountSchedulingThresholdReason(repo.lastTempReason))
	require.False(t, account.IsSchedulable())
	require.True(t, s.isOpenAIAccountBlockedBySchedulingThreshold(context.Background(), account))
	require.Equal(t, 1, repo.tempCalls, "real selection sees the same pause without rewriting it")
}

func ptrInt64Selection(v int64) *int64 { return &v }
