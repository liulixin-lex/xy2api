//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Review R01: a balance pause must take effect over a longer soft cooldown,
// and no soft cooldown may replace an active balance pause.

func TestBalancePauseReplacesLongerSoftCooldown(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	forbidden := []byte(`{"error":{"type":"permission_error","message":"forbidden"}}`)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, forbidden)
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.Reason403Cooldown), repo.tempReason)
	softUntil := repo.tempUntil

	// An in-flight request on the same account then reports an exhausted
	// balance: the 5 minute pause ends before the 10 minute cooldown.
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(balance403Body))
	require.True(t, resilience.IsBalancePause(repo.tempReason), "balance pause is written: %q", repo.tempReason)
	require.True(t, repo.tempUntil.Equal(softUntil), "the later end is kept")

	acc, _ := repo.GetByID(context.Background(), account.ID)
	tier, _, why := resiliencePenaltyOf(acc, "", time.Now())
	require.Equal(t, resilience.TierHard, tier)
	require.Equal(t, "balance_exhausted", why, "last resort must not use the account")
}

func TestSoftCooldownKeepsActiveBalancePause(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(balance403Body))
	require.True(t, resilience.IsBalancePause(repo.tempReason), repo.tempReason)
	pauseUntil := repo.tempUntil

	// A transient failure asking for a longer wait than the pause.
	svc.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{"Retry-After": {"110"}}, nil)
	require.True(t, resilience.IsBalancePause(repo.tempReason), "transient cooldown kept the balance pause: %q", repo.tempReason)

	// A counted 403 (10 minute soft cooldown) does not replace it either.
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"forbidden"}}`))
	require.True(t, resilience.IsBalancePause(repo.tempReason), "403 cooldown kept the balance pause: %q", repo.tempReason)
	require.True(t, repo.tempUntil.Equal(pauseUntil))
}

func TestSoftCooldownStillExtendsSoftCooldown(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, 524, http.Header{}, []byte(`gateway timeout`))
	first := repo.tempUntil
	svc.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{"Retry-After": {"90"}}, nil)
	require.True(t, repo.tempUntil.After(first), "a longer soft cooldown extends a shorter one")
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixTransient), repo.tempReason)
}

// Review R02: lifting the transient cooldown after a success must not clear
// a pause written between the read and the clear.
func TestLiftTransientCooldownKeepsPauseWrittenAfterRead(t *testing.T) {
	svc, repo, account, r := newPenaltyFixture(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"bad gateway"}}`))
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixTransient), repo.tempReason)
	r.lastResortUsed.Store(account.ID, time.Now())

	balanceUntil := time.Now().Add(5 * time.Minute)
	repo.afterGet = func() {
		repo.afterGet = nil
		repo.mu.Lock()
		repo.tempUntil = balanceUntil
		repo.tempReason = resilience.BalancePauseReason("concurrent request")
		repo.mu.Unlock()
	}
	r.NoteAccountSuccess(context.Background(), account)
	require.True(t, resilience.IsBalancePause(repo.tempReason), "the balance pause stays: %q", repo.tempReason)
	require.True(t, repo.tempUntil.Equal(balanceUntil))
	require.Zero(t, repo.clearCalls)
}

func TestShortHardPauseKeepsItsOwnEndOverASoftCooldown(t *testing.T) {
	// Every SetTempUnschedulable caller writes by tier while failure
	// handling is on. A short hard pause (a transport error, a health
	// breaker) replacing a 10 minute soft cooldown keeps its own end: a
	// single-account group gets its last resort back after 30 s instead of
	// waiting out the soft cooldown as a hard pause.
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	forbidden := []byte(`{"error":{"type":"permission_error","message":"forbidden"}}`)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, forbidden)
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.Reason403Cooldown), repo.tempReason)

	until := time.Now().Add(30 * time.Second)
	require.NoError(t, repo.SetTempUnschedulable(context.Background(), account.ID, until, "upstream transport error"))
	require.Equal(t, "upstream transport error", repo.tempReason)
	require.True(t, repo.tempUntil.Equal(until), "the hard pause keeps its own end")

	// A soft cooldown written meanwhile does not replace it.
	require.NoError(t, repo.SetTempUnschedulable(context.Background(), account.ID, time.Now().Add(time.Hour), resilience.ReasonPrefixTransient+"TRANSIENT 503"))
	require.Equal(t, "upstream transport error", repo.tempReason)

	// With failure handling off the write is the 0.3.0 one (only extends).
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	require.NoError(t, repo.SetTempUnschedulable(context.Background(), account.ID, time.Now().Add(time.Hour), resilience.ReasonPrefixTransient+"TRANSIENT 503"))
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixTransient))
}
