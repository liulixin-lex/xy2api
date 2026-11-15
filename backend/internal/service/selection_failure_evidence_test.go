//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type exhaustionRepo struct {
	schedulerTestOpenAIAccountRepo
	diagnosisErr error
	getErr       error
	listErr      error
}

func (r exhaustionRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]Account, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group, platform)
}

func (r exhaustionRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.schedulerTestOpenAIAccountRepo.GetByID(ctx, id)
}

func (r exhaustionRepo) ListModelAvailabilityCandidates(ctx context.Context, group *int64, platforms []string, all bool) ([]Account, error) {
	if r.diagnosisErr != nil {
		return nil, r.diagnosisErr
	}
	return r.schedulerTestOpenAIAccountRepo.ListModelAvailabilityCandidates(ctx, group, platforms, all)
}

func TestSelectionExhaustionEvidence(t *testing.T) {
	future := time.Now().Add(time.Hour)
	account := func(id int64) Account {
		return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{7}}
	}
	for _, mode := range []string{"empty", "model", "rate", "balance", "mixed", "excluded", "capacity", "dependency", "db_dependency", "db_rate"} {
		t.Run(mode, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.LastResort.Enabled = false; s.Errors.Enabled = false })
			a, b := account(1), account(2)
			accounts := []Account{a}
			var exclusions map[int64]struct{}
			var cause error
			want := map[SelectionReason]int{}
			models, requests := 1, 1
			switch mode {
			case "empty":
				accounts = nil
				models, requests = 0, 0
				want[SelectionReasonNoMembers] = 1
			case "model":
				accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"other": "other"}}
				models, requests = 0, 0
				want[SelectionReasonModelNotSupported] = 1
			case "rate":
				accounts[0].RateLimitResetAt = &future
				want[SelectionReasonRateLimited] = 1
			case "balance":
				accounts[0].TempUnschedulableUntil = &future
				accounts[0].TempUnschedulableReason = resilience.BalancePauseReason("Insufficient account balance")
				want[SelectionReasonBalanceExhausted] = 1
			case "mixed":
				a.RateLimitResetAt = &future
				b.TempUnschedulableUntil = &future
				b.TempUnschedulableReason = resilience.BalancePauseReason("Insufficient account balance")
				accounts = []Account{a, b}
				models, requests = 2, 2
				want[SelectionReasonRateLimited] = 1
				want[SelectionReasonBalanceExhausted] = 1
			case "excluded":
				exclusions = map[int64]struct{}{1: {}}
				want[SelectionReasonExcluded] = 1
			case "capacity":
				want[SelectionReasonConcurrencyFull] = 1
			case "db_dependency":
				cause = errors.New("account recheck database unavailable")
				want[SelectionReasonDependencyError] = 1
			case "db_rate":
				accounts[0].RateLimitResetAt = &future
				want[SelectionReasonRateLimited] = 1
			case "dependency":
				cause = errors.New("availability database unavailable")
				accounts = nil
				want[SelectionReasonDependencyError] = 1
				models, requests = 0, 0
			}
			repo := exhaustionRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
			if mode == "dependency" {
				repo.diagnosisErr = cause
			}
			if mode == "db_dependency" {
				repo.getErr = cause
			}
			svc := &OpenAIGatewayService{accountRepo: repo, cfg: newSchedulerTestOpenAIWSV2Config()}
			if mode == "db_dependency" || mode == "db_rate" {
				svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&a}, accountsByID: map[int64]*Account{1: &a}}}
			}
			if mode == "capacity" {
				svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false}, loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 100}}})
			}
			ctx := WithOpenAISelectionNoWait(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
			result, _, err := svc.SelectAccountWithScheduler(ctx, ptrInt64Selection(7), "", "", "m", exclusions, OpenAIUpstreamTransportHTTPSSE, false)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			e, ok := SelectionExhaustionFromError(err)
			require.True(t, ok)
			require.Equal(t, mode != "dependency", e.Known)
			require.Equal(t, models, e.ModelSupported)
			require.Equal(t, requests, e.RequestSupported)
			require.Equal(t, 0, e.Immediate)
			require.Equal(t, want, e.Reasons)
			if cause != nil {
				require.ErrorIs(t, err, cause)
			}
			if mode == "excluded" || mode == "mixed" || mode == "dependency" {
				require.False(t, e.GroupLevel)
			}
		})
	}
	t.Run("listing_dependency_keeps_internal_identity", func(t *testing.T) {
		withResilienceSettings(t, nil)
		cause := errors.New("scheduler lookup unavailable")
		repo := exhaustionRepo{listErr: cause}
		svc := &OpenAIGatewayService{accountRepo: repo, cfg: newSchedulerTestOpenAIWSV2Config()}
		_, _, err := svc.SelectAccountWithScheduler(context.Background(), ptrInt64Selection(7), "", "", "m", nil, OpenAIUpstreamTransportHTTPSSE, false)
		require.ErrorIs(t, err, cause)
		require.False(t, errors.Is(err, ErrNoAvailableAccounts))
		e, ok := SelectionExhaustionFromError(err)
		require.True(t, ok)
		require.False(t, e.Known)
		require.ErrorIs(t, e.LastCause, cause)
	})
	t.Run("last_upstream_cause", func(t *testing.T) {
		upstream := &UpstreamFailoverError{StatusCode: 429, ResponseBody: []byte(`{"error":{"message":"quota"}}`)}
		original := errors.Join(noAvailableOpenAISelectionError("m", false, ""), upstream)
		e := selectionExhaustionEvidence(withSelectionFailureEvidence(context.Background()), ModelAvailabilityDiagnosis{Known: true}, original)
		observed := selectionErrorWithExhaustion(original, e)
		require.Same(t, upstream, e.LastCause)
		require.ErrorIs(t, observed, upstream)
		require.ErrorIs(t, observed, ErrNoAvailableAccounts)
		require.False(t, e.GroupLevel)
	})
	t.Run("complete_persistent_scan", func(t *testing.T) {
		a, b, c := account(1), account(2), account(3)
		a.RateLimitResetAt = &future
		c.Credentials = map[string]any{"model_mapping": map[string]any{"other": "other"}}
		svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{a, b, c}}}
		diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), ptrInt64Selection(7), "m", PlatformOpenAI)
		require.True(t, diag.Known)
		require.Equal(t, 3, diag.Members)
		require.Equal(t, 2, diag.ModelSupported)
		require.Equal(t, 2, diag.RequestSupported)
	})
}

func TestSelectionRetryNotBeforeAllPaths(t *testing.T) {
	for _, mode := range []string{"normal", "sticky", "legacy", "db", "last_resort", "no_wait"} {
		t.Run(mode, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
			synctest.Test(t, func(t *testing.T) {
				a := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{7}}
				if mode == "last_resort" {
					until := time.Now().Add(time.Hour)
					a.OverloadUntil = &until
				}
				f := newSelectionRuntimeFixture(t, a)
				st := resilience.NewRequestState()
				deadline := time.Now().Add(time.Second)
				st.NoteAccountRetryNotBefore(a.ID, resilience.UpstreamRecoveryHint{NotBefore: deadline})
				ctx := resilience.WithRequestState(context.Background(), st)
				if mode == "no_wait" {
					ctx = WithOpenAISelectionNoWait(ctx)
				}
				session := ""
				if mode == "sticky" || mode == "legacy" {
					session = "s"
					f.cache.sessionBindings[f.svc.openAISessionCacheKey(session)] = a.ID
				}
				if mode == "db" {
					f.svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&a}, accountsByID: map[int64]*Account{1: &a}}}
				}
				selectAccount := func() (*AccountSelectionResult, error) {
					if mode == "legacy" {
						return f.svc.SelectAccountWithLoadAwareness(ctx, ptrInt64Selection(7), session, "m", nil)
					}
					result, _, err := f.svc.SelectAccountWithScheduler(ctx, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportHTTPSSE, false)
					return result, err
				}
				result, err := selectAccount()
				require.Nil(t, result)
				require.Error(t, err)
				require.Empty(t, f.attempts, "future provider deadline must veto before taking any slot")
				hint, ok := st.AccountRetryNotBefore(1)
				require.True(t, ok)
				require.Equal(t, deadline, hint.NotBefore)
				time.Sleep(time.Second)
				result, err = selectAccount()
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, int64(1), result.Account.ID)
				require.Equal(t, []int64{1}, f.attempts)
				if mode == "last_resort" {
					require.Equal(t, int64(1), st.LastResort())
				}
				result.ReleaseFunc()
			})
		})
	}
}
