//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// countingPlatformRepo counts schedulable-list calls to prove the negative
// cache skips selection.
type countingPlatformRepo struct {
	groupListingRepo
	lists int
}

func (r *countingPlatformRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	r.lists++
	return r.groupListingRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

func (r *countingPlatformRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	r.lists++
	return r.groupListingRepo.ListSchedulableByPlatform(ctx, platform)
}

func (r *countingPlatformRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]Account, error) {
	r.lists++
	return r.groupListingRepo.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, platforms)
}

func (r *countingPlatformRepo) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]Account, error) {
	r.lists++
	return r.groupListingRepo.ListSchedulableByPlatforms(ctx, platforms)
}

func TestNegativeCacheBurstOfRetries(t *testing.T) {
	// Spec: 50 requests within 2 s after a no-account result -> only the
	// first performs account selection.
	until := time.Now().Add(time.Hour)
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 81, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x",
	}})
	counting := &countingPlatformRepo{groupListingRepo: svc.accountRepo.(groupListingRepo)}
	svc.accountRepo = counting
	groupID := int64(7)
	for i := 0; i < 50; i++ {
		_, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
		require.ErrorIs(t, err, ErrNoAvailableAccounts)
	}
	require.Equal(t, 1, counting.lists)

	// Failover attempts (with exclusions) never use the cache.
	_, _ = svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", map[int64]struct{}{99: {}}, "", 0)
	require.Equal(t, 2, counting.lists)

	// Account recovery invalidates the entry.
	resilience.ProcessNegativeCache().InvalidateGroup(7)
	_, _ = svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.Equal(t, 3, counting.lists)
}

func TestNegativeCacheOffWhenErrorsDisabled(t *testing.T) {
	until := time.Now().Add(time.Hour)
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 82, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, Concurrency: 1,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x",
	}})
	withResilienceSettings(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
	counting := &countingPlatformRepo{groupListingRepo: svc.accountRepo.(groupListingRepo)}
	svc.accountRepo = counting
	groupID := int64(7)
	for i := 0; i < 3; i++ {
		_, _ = svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
	}
	require.Equal(t, 3, counting.lists, "errors switch off: no negative cache")
}

func TestNegativeCacheSkipsConfigAndProfitResults(t *testing.T) {
	withResilienceSettings(t, nil)
	groupID := int64(9)
	ctx := context.Background()
	for _, err := range []error{
		fmt.Errorf("%w: channel pricing restriction", ErrNoAvailableAccounts),
		fmt.Errorf("%w supporting model: m (total=2 eligible=0 profit_threshold=2 profit_invalid_account_rate=0)", ErrNoAvailableAccounts),
		ErrClaudeCodeOnly,
	} {
		resilienceNegativeCachePut(ctx, &groupID, "m", "", nil, err)
		require.NoError(t, resilienceNegativeCacheHit(ctx, &groupID, "m", "", nil), err.Error())
	}
	resilienceNegativeCachePut(ctx, &groupID, "m", "", nil, fmt.Errorf("%w supporting model: m (total=1 profit_threshold=0 profit_invalid_account_rate=0)", ErrNoAvailableAccounts))
	require.ErrorIs(t, resilienceNegativeCacheHit(ctx, &groupID, "m", "", nil), ErrNoAvailableAccounts)
	require.NoError(t, resilienceNegativeCacheHit(ctx, &groupID, "other-model", "", nil))
}

func TestNegativeCacheHitIsRecognisedAndScopedByVariant(t *testing.T) {
	withResilienceSettings(t, nil)
	groupID := int64(19)
	ctx := context.Background()
	noAccount := fmt.Errorf("%w supporting model: m (total=1 profit_threshold=0 profit_invalid_account_rate=0)", ErrNoAvailableAccounts)

	// A replayed result must be recognisable, or the selection epilogue
	// stores it again and the entry never expires under steady traffic.
	resilienceNegativeCachePut(ctx, &groupID, "m", "", nil, noAccount)
	hit := resilienceNegativeCacheHit(ctx, &groupID, "m", "", nil)
	require.ErrorIs(t, hit, ErrNoAvailableAccounts)
	require.ErrorIs(t, hit, errNegativeCached)

	// An OpenAI selection that needs another transport or capability is not
	// answered by this result.
	chat := "openai:" + string(OpenAIUpstreamTransportAny) + ":chat_completions:openai"
	responses := "openai:" + string(OpenAIUpstreamTransportAny) + "::openai"
	resilienceNegativeCachePut(ctx, &groupID, "m2", chat, nil, noAccount)
	require.ErrorIs(t, resilienceNegativeCacheHit(ctx, &groupID, "m2", chat, nil), errNegativeCached)
	require.NoError(t, resilienceNegativeCacheHit(ctx, &groupID, "m2", responses, nil))
	require.NoError(t, resilienceNegativeCacheHit(ctx, &groupID, "m2", "", nil))
}

func TestReservationErrorDistinguishesHeldBalance(t *testing.T) {
	withResilienceSettings(t, nil)
	cache := newMemInflightCache(1)
	svc := newInflightSvc(t, cache, 60)
	user := &User{ID: 5}
	res, err := svc.ReserveInflight(context.Background(), user, nil, nil, 0.9)
	require.NoError(t, err)
	_, err = svc.ReserveInflight(context.Background(), user, nil, nil, 0.9)
	require.ErrorIs(t, err, ErrInflightBalanceReserved)
	require.Contains(t, err.Error(), "available $1.0000, reserved $0.9000")
	// A request the balance alone cannot cover gains nothing from waiting.
	_, err = svc.ReserveInflight(context.Background(), user, nil, nil, 1.5)
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.NotErrorIs(t, err, ErrInflightBalanceReserved)
	res.HandlerDone()

	// A zero balance stays "insufficient balance".
	empty := newMemInflightCache(0)
	svc2 := newInflightSvc(t, empty, 60)
	first, err := svc2.ReserveInflight(context.Background(), &User{ID: 6}, nil, nil, 0.1)
	require.NoError(t, err)
	_, err = svc2.ReserveInflight(context.Background(), &User{ID: 6}, nil, nil, 0.1)
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.NotErrorIs(t, err, ErrInflightBalanceReserved)
	first.HandlerDone()
}
