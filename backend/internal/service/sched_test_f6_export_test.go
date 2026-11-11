//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type schedF6Accounts struct {
	AccountRepository
	account *Account
}

func (r *schedF6Accounts) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }
func (r *schedF6Accounts) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return []*Account{r.account}, nil
}
func (r *schedF6Accounts) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return []Account{*r.account}, nil
}
func (r *schedF6Accounts) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	return []Account{*r.account}, nil
}
func (r *schedF6Accounts) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	return []Account{*r.account}, nil
}
func (r *schedF6Accounts) UpdateExtra(context.Context, int64, map[string]any) error { return nil }
func (r *schedF6Accounts) UpdateLastUsed(context.Context, int64) error              { return nil }

type schedF6Slots struct{ ConcurrencyCache }

func (schedF6Slots) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (schedF6Slots) ReleaseAccountSlot(context.Context, int64, string) error { return nil }
func (schedF6Slots) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (schedF6Slots) ReleaseUserSlot(context.Context, int64, string) error { return nil }
func (schedF6Slots) GetAccountsLoadBatch(_ context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	out := map[int64]*AccountLoadInfo{}
	for _, a := range accounts {
		out[a.ID] = &AccountLoadInfo{AccountID: a.ID}
	}
	return out, nil
}

type schedF6Usage struct {
	UsageLogRepository
	writes int
}

func (s *schedF6Usage) Create(context.Context, *UsageLog) (bool, error) { s.writes++; return true, nil }

// NewSchedTestF6Fixture installs the real recorder so isolation is checked at its boundary.
func NewSchedTestF6Fixture(t *testing.T, account *Account, up HTTPUpstream) (*OpenAIGatewayService, *ConcurrencyService, *BillingCacheService, *config.Config, func()) {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.JWT.Secret = "sched-f6-secret"
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	repo := &schedF6Accounts{account: account}
	usage := &schedF6Usage{}
	slots := NewConcurrencyService(schedF6Slots{})
	billing := NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	token := NewOpenAITokenProvider(repo, nil, nil)
	gw := NewOpenAIGatewayService(repo, usage, nil, nil, nil, nil, nil, cfg, nil, slots, NewBillingService(cfg, nil), nil, billing, up, &DeferredService{}, token, nil, nil, nil, nil, nil, nil)
	store := &fakeSchedAttemptStore{}
	recorder := NewSchedAttemptRecorderService(store)
	observed := 0
	recorder.SetObserver(func(schedquality.AttemptSample) { observed++ })
	gw.SetSchedAttemptRecorder(recorder)
	assertIsolated := func() {
		require.Zero(t, usage.writes)
		require.Zero(t, observed)
		require.Zero(t, recorder.count)
		require.Empty(t, recorder.memory.entries)
		require.Nil(t, gw.openaiRecentSession)
		owner, err := gw.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 0, "resp_sched_f6")
		require.NoError(t, err)
		require.Zero(t, owner)
	}
	return gw, slots, billing, cfg, assertIsolated
}

func TestSchedTestHarnessFinishesUnfinished(t *testing.T) {
	recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
	gw := &OpenAIGatewayService{schedAttemptRecorder: recorder}
	forward := &fakeSchedTestForwarder{Fn: func(ctx context.Context, _ *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
		gw.beginSchedAttempt(ctx, a, body, len(body), true)
		return nil, nil
	}}
	_, samples, _, err := schedTestForward(context.Background(), forward, &Account{ID: 1, Platform: PlatformOpenAI}, "m", []byte(`{"model":"m"}`), false, 0)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	require.Equal(t, schedquality.OutcomeOther, samples[0].Outcome)
	require.Equal(t, "unfinished", samples[0].Detail)
	require.WithinDuration(t, time.Now(), samples[0].FinishedAt, time.Second)
}
