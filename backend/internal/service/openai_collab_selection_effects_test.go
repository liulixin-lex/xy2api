//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollabSelectionQuotaThresholdHasNoSharedEffects(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(fmt.Sprint(optional), func(t *testing.T) {
			accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
			accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
			t.Cleanup(func() {
				accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
				accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
			})
			settingsRepo := newMockSettingRepo()
			thresholds, err := json.Marshal(map[string]int{"openai": 80})
			require.NoError(t, err)
			settingsRepo.data[SettingKeyAccountSchedulingThresholds] = string(thresholds)
			first, second := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
			first.Extra = map[string]any{"codex_7d_used_percent": 91.5, "codex_7d_reset_at": time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339)}
			f := newSelectionRuntimeFixture(t, first, second)
			repo, blocker := &rateLimitAccountRepoStub{}, &runtimeBlockRecorder{}
			rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))
			rl.SetAccountRuntimeBlocker(blocker)
			f.svc.rateLimitService = rl
			ctx := resilience.WithCollabAttempt(resilience.WithRequestState(t.Context(), resilience.NewRequestState()), resilience.CollabAttempt{Optional: optional})
			selected, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
			require.NoError(t, err)
			require.NotNil(t, selected)
			CombineSelectionRelease(selected, nil)()
			require.EqualValues(t, 2, selected.Account.ID, "threshold still skips the account in this attempt")
			want := 1
			if optional {
				want = 0
			}
			require.Equal(t, want, repo.tempCalls)
			require.Len(t, blocker.accounts, want)
			// Direct admission must not mutate the caller's shared account row.
			snapshot := selectionFixtureAccount(3, 1)
			snapshot.Extra = first.Extra
			require.True(t, rl.ApplyAccountSchedulingThreshold(ctx, &snapshot))
			if optional {
				require.Nil(t, snapshot.TempUnschedulableUntil)
				require.Empty(t, snapshot.TempUnschedulableReason)
			} else {
				require.NotNil(t, snapshot.TempUnschedulableUntil)
			}
		})
	}
}

func TestCollabSelectionSlotRaceDoesNotAffectNextRequest(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(fmt.Sprint(optional), func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 5))
			f.loads.acquireResults = map[int64]bool{1: false, 2: true}
			child := resilience.NewCollabChildState(resilience.NewRequestState())
			ctx := resilience.WithCollabAttempt(resilience.WithRequestState(context.Background(), child), resilience.CollabAttempt{Optional: optional})
			selected, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
			require.NoError(t, err)
			require.NotNil(t, selected)
			CombineSelectionRelease(selected, nil)()
			require.EqualValues(t, 2, selected.Account.ID)
			require.Equal(t, []int64{1, 2}, f.attempts)
			require.Equal(t, !optional, f.svc.selectionFullMarks.Recently(1, f.svc.selectionNow()))
			f.loads.acquireResults = map[int64]bool{1: true, 2: true}
			f.cache.sessionBindings["openai:session"] = 2
			next := f.selectNow(t, context.Background(), "session", nil)
			want := int64(2)
			if optional {
				want = 1
			}
			require.Equal(t, want, next.Account.ID)
		})
	}
}

func TestCollabSelectionAutoResetDoesNotQueueMaintenance(t *testing.T) {
	for _, mode := range []string{"five_hour", "seven_day", "credit_check"} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional_%t", mode, optional), func(t *testing.T) {
				notifier := &OpenAIQuotaAutoResetService{ctx: context.Background(), queue: make(chan int64, 4)}
				setOpenAIAutoResetNotifier(notifier)
				t.Cleanup(func() { clearOpenAIAutoResetNotifier(notifier) })
				first, second := selectionFixtureAccount(41, 1), selectionFixtureAccount(42, 1)
				first.Type = AccountTypeOAuth
				first.Extra = map[string]any{
					OpenAIAutoResetCreditEnabledExtraKey: true,
					"codex_5h_used_percent":              100.0,
					"codex_5h_reset_at":                  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					"codex_7d_used_percent":              0.0,
					"codex_7d_reset_at":                  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				}
				if mode == "seven_day" {
					first.Extra["codex_5h_used_percent"] = 0.0
					first.Extra["codex_7d_used_percent"] = 100.0
				}
				if mode == "credit_check" {
					first.Extra["codex_5h_used_percent"] = 90.0
					first.Extra["auto_pause_5h_threshold"] = 0.8
				}
				openAIAutoResetSchedulerNotifiedAt.Delete(first.ID)
				t.Cleanup(func() { openAIAutoResetSchedulerNotifiedAt.Delete(first.ID) })
				f := newSelectionRuntimeFixture(t, first, second)
				ctx := resilience.WithCollabAttempt(resilience.WithRequestState(t.Context(), resilience.NewRequestState()), resilience.CollabAttempt{Optional: optional})
				selected, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
				require.NoError(t, err)
				require.NotNil(t, selected)
				CombineSelectionRelease(selected, nil)()
				require.EqualValues(t, 42, selected.Account.ID)
				_, notified := openAIAutoResetSchedulerNotifiedAt.Load(first.ID)
				require.Equal(t, !optional, notified)
				want := 1
				if optional {
					want = 0
				}
				require.Len(t, notifier.queue, want)
			})
		}
	}
}

func TestCollabSelectionRuntimeReadsPreserveSharedState(t *testing.T) {
	for _, expired := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("expired_%t/optional_%t", expired, optional), func(t *testing.T) {
				first, second := selectionFixtureAccount(51, 1), selectionFixtureAccount(52, 5)
				f := newSelectionRuntimeFixture(t, first, second)
				until := time.Now().Add(time.Hour)
				if expired {
					until = time.Now().Add(-time.Hour)
				}
				f.svc.BlockAccountScheduling(&first, until, "synthetic")
				// A fresh row has no persisted cooldown. Ordinary admission clears
				// the older local mark; optional admission must only read it.
				state := f.svc.getOpenAIAccountModelTransientState()
				observed := time.Now().Add(-time.Minute)
				state.recordFailure(first.ID, "m", observed)
				key, valid := openAIAccountModelTransientKey(first.ID, "m")
				require.True(t, valid)
				before := state.entries[key]
				generation, _ := f.svc.openaiAccountRuntimeBlockGeneration.Load(first.ID)
				ctx := resilience.WithCollabAttempt(resilience.WithRequestState(t.Context(), resilience.NewRequestState()), resilience.CollabAttempt{Optional: optional})
				selected, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
				require.NoError(t, err)
				require.NotNil(t, selected)
				CombineSelectionRelease(selected, nil)()
				require.EqualValues(t, first.ID, selected.Account.ID)
				_, blockRetained := f.svc.openaiAccountRuntimeBlockUntil.Load(first.ID)
				assert.Equal(t, optional, blockRetained)
				if optional {
					afterGeneration, _ := f.svc.openaiAccountRuntimeBlockGeneration.Load(first.ID)
					assert.Equal(t, generation, afterGeneration)
					require.Equal(t, before, state.entries[key])
				} else {
					require.True(t, state.entries[key].lastTouched.After(before.lastTouched))
				}
			})
		}
	}
}

func TestCollabOptionalAdaptiveConcurrencyDoesNotPersist(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(fmt.Sprint(optional), func(t *testing.T) {
			account := ticketTestAccount(41)
			repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
			upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}, nil
			}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{AdaptiveConcurrencyAccountIDs: []int64{account.ID}}, upstream)
			svc.accountRepo = repo
			ctx := resilience.WithCollabAttempt(t.Context(), resilience.CollabAttempt{Optional: optional})
			request := httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			response, err := svc.doOpenAIUpstream(request, "", account)
			require.NoError(t, err)
			require.Equal(t, http.StatusTooManyRequests, response.StatusCode)
			_, written := repo.updates[codexPressureKey]
			require.Equal(t, !optional, written)
			current, err := repo.GetByID(t.Context(), account.ID)
			require.NoError(t, err)
			want := 2
			if optional {
				want = 5
			}
			require.Equal(t, want, codexPressureOf(current).Limit)
		})
	}
}
