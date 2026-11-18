//go:build unit

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTrialLeaseTestCache(t *testing.T) (service.SchedTrialLeases, *redis.Client, *miniredis.Miniredis, time.Time) {
	t.Helper()
	mr := miniredis.RunT(t)
	now := time.Unix(10000, 0).UTC()
	mr.SetTime(now)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewSchedTrialLeaseCache(rdb), rdb, mr, now
}

func requireTrialAcquire(t *testing.T, cache service.SchedTrialLeases, groupID int64, member string, now time.Time) string {
	t.Helper()
	ok, generation, err := cache.Acquire(context.Background(), 1, "model-a", groupID, member, now)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, generation)
	return generation
}

func TestSchedTrialLeaseConcurrentAcquire(t *testing.T) {
	cache, rdb, _, now := newTrialLeaseTestCache(t)
	ctx := context.Background()
	type result struct {
		ok  bool
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 20)
	for i := 0; i < 20; i++ {
		go func() {
			<-start
			ok, _, err := cache.Acquire(ctx, 1, "model-a", int64(i%2+1), service.TrialLeaseSessionMember(fmt.Sprintf("session-%d", i)), now)
			results <- result{ok: ok, err: err}
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 20; i++ {
		r := <-results
		require.NoError(t, r.err)
		if r.ok {
			successes++
		}
	}
	require.Equal(t, 3, successes, "groups share exactly three atomic reservations")
	count, err := rdb.ZCard(ctx, schedTrialLeaseKey(1, "model-a")).Result()
	require.NoError(t, err)
	require.EqualValues(t, 3, count)
}

func TestSchedTrialLeaseSharedCapacityAndRenewal(t *testing.T) {
	cache, rdb, mr, now := newTrialLeaseTestCache(t)
	ctx := context.Background()
	member := service.TrialLeaseSessionMember("same-session")
	for _, group := range []int64{1, 10, 100} {
		requireTrialAcquire(t, cache, group, member, now)
	}
	ok, err := cache.Room(ctx, 1, "model-a", 1, member, now)
	require.NoError(t, err)
	require.True(t, ok, "an existing holder has room when full")
	ok, err = cache.Room(ctx, 1, "model-a", 2, member, now)
	require.NoError(t, err)
	require.False(t, ok)
	ok, _, err = cache.Acquire(ctx, 1, "model-a", 2, member, now)
	require.NoError(t, err)
	require.False(t, ok)
	for _, scope := range []struct {
		account int64
		model   string
	}{{2, "model-a"}, {1, "model-b"}} {
		ok, _, err = cache.Acquire(ctx, scope.account, scope.model, 2, member, now)
		require.NoError(t, err)
		require.True(t, ok, "different account/model has its own capacity")
	}
	mr.FastForward(5 * time.Minute)
	requireTrialAcquire(t, cache, 1, member, now.Add(5*time.Minute))
	count, err := rdb.ZCard(ctx, schedTrialLeaseKey(1, "model-a")).Result()
	require.NoError(t, err)
	require.EqualValues(t, 3, count, "reacquisition must not consume another slot")
	require.Equal(t, time.Hour, mr.TTL(schedTrialLeaseKey(1, "model-a")))
	// At the original idle deadline only the reacquired holder remains.
	_, err = cache.Room(ctx, 1, "model-a", 1, member, now.Add(service.TrialLeaseIdle))
	require.NoError(t, err)
	members, err := rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"1:" + member}, members)
}

func TestSchedTrialLeaseIdleBoundaryAndRoomDoesNotRenew(t *testing.T) {
	cache, rdb, mr, now := newTrialLeaseTestCache(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		requireTrialAcquire(t, cache, 1, service.TrialLeaseSessionMember(fmt.Sprint(i)), now)
	}
	mr.FastForward(service.TrialLeaseIdle - time.Millisecond)
	before := now.Add(service.TrialLeaseIdle - time.Millisecond)
	ok, err := cache.Room(ctx, 1, "model-a", 1, service.TrialLeaseSessionMember("new"), before)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = cache.Room(ctx, 1, "model-a", 1, service.TrialLeaseSessionMember("0"), before)
	require.NoError(t, err)
	require.True(t, ok)
	mr.FastForward(time.Millisecond)
	ok, err = cache.Room(ctx, 1, "model-a", 1, service.TrialLeaseSessionMember("new"), now.Add(service.TrialLeaseIdle))
	require.NoError(t, err)
	require.True(t, ok)
	count, err := rdb.ZCard(ctx, schedTrialLeaseKey(1, "model-a")).Result()
	require.NoError(t, err)
	require.Zero(t, count, "idle >= 600s is expired, even after Room")
	requireTrialAcquire(t, cache, 1, service.TrialLeaseSessionMember("new"), now.Add(service.TrialLeaseIdle))
	mr.FastForward(time.Hour)
	require.False(t, mr.Exists(schedTrialLeaseKey(1, "model-a")), "unused keys must expire")
}

func TestSchedTrialLeaseTouchOnlyLiveMembers(t *testing.T) {
	cache, rdb, mr, now := newTrialLeaseTestCache(t)
	ctx := context.Background()
	a, b := service.TrialLeaseSessionMember("a"), service.TrialLeaseSessionMember("b")
	requireTrialAcquire(t, cache, 1, a, now)
	requireTrialAcquire(t, cache, 1, b, now)
	require.NoError(t, cache.Touch(ctx, 1, "model-a", 1, a, now.Add(5*time.Minute)))
	require.NoError(t, cache.Touch(ctx, 1, "model-a", 1, service.TrialLeaseSessionMember("missing"), now.Add(5*time.Minute)))
	// Delayed activity must not move a lease's last activity backwards.
	require.NoError(t, cache.Touch(ctx, 1, "model-a", 1, a, now.Add(4*time.Minute)))
	requireTrialAcquire(t, cache, 1, a, now.Add(3*time.Minute))
	require.NoError(t, cache.Touch(ctx, 1, "model-a", 1, b, now.Add(service.TrialLeaseIdle)))
	members, err := rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"1:" + a}, members, "Touch must not resurrect expired or missing members")
	_, err = cache.Room(ctx, 1, "model-a", 1, a, now.Add(15*time.Minute-time.Millisecond))
	require.NoError(t, err)
	count, err := rdb.ZCard(ctx, schedTrialLeaseKey(1, "model-a")).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, cache.Touch(ctx, 1, "model-a", 1, a, now.Add(15*time.Minute)))
	require.False(t, mr.Exists(schedTrialLeaseKey(1, "model-a")))
}

func TestSchedTrialLeaseReleaseAndRequestNamespace(t *testing.T) {
	cache, rdb, mr, now := newTrialLeaseTestCache(t)
	ctx := context.Background()
	request := service.TrialLeaseRequestMember("request-1")
	session := service.TrialLeaseSessionMember("request-1")
	lookalike := service.TrialLeaseSessionMember(request)
	requireTrialAcquire(t, cache, 1, request, now)
	requireTrialAcquire(t, cache, 1, session, now)
	requireTrialAcquire(t, cache, 10, lookalike, now)
	ok, _, err := cache.Acquire(ctx, 1, "model-a", 1, service.TrialLeaseRequestMember("request-2"), now)
	require.NoError(t, err)
	require.False(t, ok, "sessionless requests count towards capacity")
	require.NoError(t, cache.Release(ctx, 1, "model-a", 1, request))
	require.NoError(t, cache.Release(ctx, 1, "model-a", 1, request))
	members, err := rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"1:" + session, "10:" + lookalike}, members)
	requireTrialAcquire(t, cache, 1, request, now)
	require.NoError(t, cache.ReleaseGroup(ctx, 1, "model-a", 1))
	require.NoError(t, cache.ReleaseGroup(ctx, 1, "model-a", 1))
	members, err = rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"10:" + lookalike}, members, "group 1 cannot release group 10")
	require.NoError(t, cache.Release(ctx, 1, "model-a", 10, lookalike))
	require.False(t, mr.Exists(schedTrialLeaseKey(1, "model-a")))
	// Even a real session equal to the encoded request identity stays distinct
	// when all three members belong to the same group.
	for _, member := range []string{request, session, lookalike} {
		requireTrialAcquire(t, cache, 1, member, now)
	}
	require.NoError(t, cache.Release(ctx, 1, "model-a", 1, request))
	members, err = rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"1:" + session, "1:" + lookalike}, members)
}

func TestSchedTrialLeaseEveryOperationPrunesIdle(t *testing.T) {
	for _, operation := range []string{"room", "acquire", "touch", "release", "release_group"} {
		t.Run(operation, func(t *testing.T) {
			cache, rdb, mr, now := newTrialLeaseTestCache(t)
			ctx := context.Background()
			old := service.TrialLeaseSessionMember("old")
			live := service.TrialLeaseSessionMember("live")
			requireTrialAcquire(t, cache, 10, old, now)
			requireTrialAcquire(t, cache, 10, live, now.Add(time.Second))
			at := now.Add(service.TrialLeaseIdle)
			mr.FastForward(service.TrialLeaseIdle)
			mr.SetTime(at) // FastForward updates TTLs, not the TIME command.
			var err error
			switch operation {
			case "room":
				_, err = cache.Room(ctx, 1, "model-a", 1, "s:new", at)
			case "acquire":
				_, _, err = cache.Acquire(ctx, 1, "model-a", 1, "s:new", at)
			case "touch":
				err = cache.Touch(ctx, 1, "model-a", 1, "s:missing", at)
			case "release":
				err = cache.Release(ctx, 1, "model-a", 1, "s:missing")
			case "release_group":
				err = cache.ReleaseGroup(ctx, 1, "model-a", 1)
			}
			require.NoError(t, err)
			_, err = rdb.ZScore(ctx, schedTrialLeaseKey(1, "model-a"), "10:"+old).Result()
			require.ErrorIs(t, err, redis.Nil, "all operations must prune the boundary")
			_, err = rdb.ZScore(ctx, schedTrialLeaseKey(1, "model-a"), "10:"+live).Result()
			require.NoError(t, err, "a live member in another group must survive")
		})
	}
}

func TestSchedTrialLeaseRedisErrors(t *testing.T) {
	cache, _, mr, now := newTrialLeaseTestCache(t)
	ctx := context.Background()
	mr.SetError("ERR synthetic storage failure")
	ok, err := cache.Room(ctx, 1, "model-a", 1, "s:a", now)
	require.False(t, ok)
	require.ErrorContains(t, err, "synthetic storage failure")
	ok, _, err = cache.Acquire(ctx, 1, "model-a", 1, "s:a", now)
	require.False(t, ok)
	require.ErrorContains(t, err, "synthetic storage failure")
	require.ErrorContains(t, cache.Touch(ctx, 1, "model-a", 1, "s:a", now), "synthetic storage failure")
	require.ErrorContains(t, cache.Release(ctx, 1, "model-a", 1, "s:a"), "synthetic storage failure")
	require.ErrorContains(t, cache.ReleaseGroup(ctx, 1, "model-a", 1), "synthetic storage failure")
}

func TestSchedTrialLeasePromotionConcurrentCandidates(t *testing.T) {
	for _, order := range []string{"failure_first", "release_after_commit"} {
		t.Run(order, func(t *testing.T) {
			leases, rdb, _, now := newTrialLeaseTestCache(t)
			ctx := context.Background()
			first, second, session := service.TrialLeaseRequestMember("request-one"), service.TrialLeaseRequestMember("request-two"), service.TrialLeaseSessionMember("session")
			requireTrialAcquire(t, leases, 7, first, now)
			generation := requireTrialAcquire(t, leases, 7, second, now)
			canceled, commit := make(chan struct{}), make(chan struct{})
			released := make(chan error, 1)
			go func() { close(canceled); <-commit; released <- leases.Release(ctx, 1, "model-a", 7, first) }()
			<-canceled
			if order == "failure_first" {
				close(commit)
				require.NoError(t, <-released)
			}
			bindings := NewGatewayCache(rdb).(service.OpenAIStickyVersionedCache)
			changed, commitErr := bindings.CommitSessionBinding(ctx, 7, "openai:session", service.StickySessionBinding{}, 1, time.Hour)
			require.NoError(t, commitErr)
			require.True(t, changed)
			require.NoError(t, leases.Promote(ctx, 1, "model-a", 7, second, session, generation, now.Add(time.Second)))
			if order == "release_after_commit" {
				close(commit)
				require.NoError(t, <-released)
			}
			members, err := rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Result()
			require.NoError(t, err)
			require.Equal(t, []string{"7:" + session}, members)
			binding, bindErr := bindings.GetSessionBinding(ctx, 7, "openai:session")
			require.NoError(t, bindErr)
			require.EqualValues(t, 1, binding.AccountID)
			require.NoError(t, leases.Promote(ctx, 1, "model-a", 7, second, session, generation, now))
			score, err := rdb.ZScore(ctx, schedTrialLeaseKey(1, "model-a"), "7:"+session).Result()
			require.NoError(t, err)
			require.EqualValues(t, now.Add(time.Second).UnixMilli(), score)
		})
	}
}

func TestSchedTrialLeasePromotionMissingSourceAndFullCapacity(t *testing.T) {
	for _, mode := range []string{"missing", "expired", "full"} {
		t.Run(mode, func(t *testing.T) {
			leases, rdb, _, now := newTrialLeaseTestCache(t)
			ctx := context.Background()
			request, session := service.TrialLeaseRequestMember("request"), service.TrialLeaseSessionMember("committed")
			generation := requireTrialAcquire(t, leases, 7, request, now)
			if mode != "expired" {
				require.NoError(t, leases.Release(ctx, 1, "model-a", 7, request))
			}
			if mode == "expired" {
				now = now.Add(service.TrialLeaseIdle)
			}
			if mode == "full" {
				for i := 0; i < service.TrialLeaseLimit; i++ {
					requireTrialAcquire(t, leases, 7, service.TrialLeaseSessionMember(fmt.Sprint(i)), now)
				}
			}
			require.NoError(t, leases.Promote(ctx, 1, "model-a", 7, request, session, generation, now))
			members, err := rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Result()
			require.NoError(t, err)
			require.Contains(t, members, "7:"+session)
			require.NotContains(t, members, "7:"+request)
			want := 1
			if mode == "full" {
				want = service.TrialLeaseLimit + 1
			}
			require.Len(t, members, want)
		})
	}
}

func TestSchedTrialGenerationFencesLatePromotionAcrossGroups(t *testing.T) {
	leases, rdb, _, at := newTrialLeaseTestCache(t)
	ctx := context.Background()
	old := requireTrialAcquire(t, leases, 7, "r:old", at)
	other := requireTrialAcquire(t, leases, 8, "r:other", at)
	require.NoError(t, leases.ReleaseGroup(ctx, 1, "model-a", 7))
	newRound := requireTrialAcquire(t, leases, 7, "r:new", at.Add(21*time.Minute))
	require.NotEqual(t, old, newRound)
	require.ErrorIs(t, leases.Promote(ctx, 1, "model-a", 7, "r:old", "s:old", old, at.Add(22*time.Minute)), service.ErrTrialGenerationChanged)
	require.NoError(t, leases.Promote(ctx, 1, "model-a", 8, "r:other", "s:other", other, at.Add(22*time.Minute)))
	require.NoError(t, leases.Promote(ctx, 1, "model-a", 7, "r:new", "s:new", newRound, at.Add(22*time.Minute)))
	require.ElementsMatch(t, []string{"8:s:other", "7:s:new"}, rdb.ZRange(ctx, schedTrialLeaseKey(1, "model-a"), 0, -1).Val())
}

func TestSchedTrialReleaseFailureLeavesGenerationUntilNextSuccessfulRelease(t *testing.T) {
	leases, rdb, mr, at := newTrialLeaseTestCache(t)
	ctx := context.Background()
	token := requireTrialAcquire(t, leases, 7, "r:old", at)
	mr.SetError("ERR synthetic release failure")
	require.Error(t, leases.ReleaseGroup(ctx, 1, "model-a", 7))
	mr.SetError("")
	// The failed Redis reset did not advance its fence. The independent
	// authoritative check must decline this token if the DB reset committed.
	require.NoError(t, leases.Promote(ctx, 1, "model-a", 7, "r:old", "s:old", token, at.Add(time.Minute)))
	_, err := leases.Room(ctx, 1, "model-a", 7, "s:new", at.Add(11*time.Minute))
	require.NoError(t, err)
	require.Zero(t, rdb.ZCard(ctx, schedTrialLeaseKey(1, "model-a")).Val())
}

func TestSchedTrialLateAcquireCannotOccupyThirdSeat(t *testing.T) {
	for _, delay := range []time.Duration{time.Minute, service.TrialLeaseIdle, 16 * time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			cache, rdb, mr, observed := newTrialLeaseTestCache(t)
			ctx := context.Background()
			requireTrialAcquire(t, cache, 7, "r:previous", observed)
			require.NoError(t, cache.ReleaseGroup(ctx, 1, "model-a", 7))
			mr.SetTime(observed.Add(delay))
			acquired, _, err := cache.Acquire(ctx, 1, "model-a", 7, "r:old", observed)
			require.NoError(t, err)
			require.Equal(t, delay < service.TrialLeaseIdle, acquired)
			score, err := rdb.ZScore(ctx, schedTrialLeaseKey(1, "model-a"), "7:r:old").Result()
			if acquired {
				require.NoError(t, err)
				require.EqualValues(t, observed.UnixMilli(), score)
			} else {
				require.ErrorIs(t, err, redis.Nil)
			}
			newAt := observed.Add(25 * time.Minute)
			mr.SetTime(newAt)
			for _, member := range []string{"r:first", "r:second"} {
				requireTrialAcquire(t, cache, 8, member, newAt)
			}
			ok, err := cache.Room(ctx, 1, "model-a", 8, "r:third", newAt)
			require.NoError(t, err)
			require.True(t, ok)
			_, err = rdb.ZScore(ctx, schedTrialLeaseKey(1, "model-a"), "7:r:old").Result()
			require.ErrorIs(t, err, redis.Nil)
		})
	}
}

func TestSchedTrialGenerationExpiresWithNewEpochOnReuse(t *testing.T) {
	leases, rdb, mr, now := newTrialLeaseTestCache(t)
	ctx := context.Background()
	old := requireTrialAcquire(t, leases, 7, "r:old", now)
	mr.FastForward(schedTrialGenerationTTL - time.Hour)
	require.NoError(t, leases.ReleaseGroup(ctx, 1, "model-a", 7))
	mr.FastForward(schedTrialGenerationTTL)
	require.Zero(t, rdb.DBSize(ctx).Val())
	next := now.Add(2 * schedTrialGenerationTTL)
	mr.SetTime(next)
	current := requireTrialAcquire(t, leases, 7, "r:current", next)
	require.NotEqual(t, old, current)
	require.ErrorIs(t, leases.Promote(ctx, 1, "model-a", 7, "r:old", "s:old", old, next), service.ErrTrialGenerationChanged)
}
