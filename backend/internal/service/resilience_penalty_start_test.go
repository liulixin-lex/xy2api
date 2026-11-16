//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestTransientCooldownSuccessUsesAttemptStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset time.Duration
		clears int
	}{
		{"started_before_cooldown", -time.Second, 0},
		{"same_timestamp", 0, 0},
		{"started_after_cooldown", time.Second, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo, account, r := newPenaltyFixture(t, PlatformAnthropic)
			written := time.Now().Add(-2 * time.Second)
			repo.account.UpdatedAt = written.Add(10 * time.Second) // unrelated usage write after the attempt
			repo.tempUntil = written.Add(time.Minute)
			repo.tempReason = resilience.ReasonPrefixTransient + "TRANSIENT synthetic [set_at=" + strconv.FormatInt(written.UnixNano(), 10) + "]"
			r.lastResortUsed.Store(account.ID, written)
			st := resilience.NewRequestState()
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: written.Add(tc.offset)})
			r.NoteAccountSuccess(resilience.WithRequestState(context.Background(), st), account)
			require.Equal(t, tc.clears, repo.clearCalls)
			if tc.clears == 0 {
				_, retained := r.lastResortUsed.Load(account.ID)
				require.True(t, retained, "an old success must not consume a newer recovery opportunity")
			}
		})
	}
}

func TestTransientCooldownRequiresTimingEvidence(t *testing.T) {
	for _, reason := range []string{"transient:TRANSIENT old", "transient:TRANSIENT bad [set_at=invalid]", "transient:TRANSIENT bad [set_at=-1]"} {
		_, repo, account, r := newPenaltyFixture(t, PlatformAnthropic)
		repo.tempUntil = time.Now().Add(time.Minute)
		repo.tempReason = reason
		r.lastResortUsed.Store(account.ID, time.Now())
		st := resilience.NewRequestState()
		st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: time.Now()})
		r.NoteAccountSuccess(resilience.WithRequestState(context.Background(), st), account)
		require.Zero(t, repo.clearCalls)
	}
}
