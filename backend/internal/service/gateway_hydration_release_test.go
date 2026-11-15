//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type hydrationReleaseRepo struct {
	AccountRepository
	account *Account
	err     error
}

func (r *hydrationReleaseRepo) GetByID(ctx context.Context, _ int64) (*Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.account, r.err
}

func TestGatewayHydrationFailureReleasesSlot(t *testing.T) {
	for _, mode := range []string{"missing", "repo_error", "canceled", "success", "nil_release"} {
		t.Run(mode, func(t *testing.T) {
			account := &Account{ID: 606}
			repo := &hydrationReleaseRepo{}
			ctx := context.Background()
			if mode == "repo_error" {
				repo.err = errors.New("hydration repository unavailable")
			}
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "success" {
				repo.account = account
			}
			snapshot := NewSchedulerSnapshotService(nil, nil, repo, nil, nil)
			svc := &GatewayService{schedulerSnapshot: snapshot}
			released := 0
			release := func() { released++ }
			if mode == "nil_release" {
				release = nil
			}
			result, err := svc.newSelectionResult(ctx, account, true, release, nil)
			t.Logf("case=%s result_nil=%v error=%v release_calls=%d", mode, result == nil, err, released)
			if mode == "success" {
				require.NoError(t, err)
				require.Zero(t, released)
				result.ReleaseFunc()
				require.Equal(t, 1, released)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				if mode != "nil_release" {
					require.Equal(t, 1, released)
				}
			}
		})
	}
}

func TestReviewLastResortHydrationReleasesOnce(t *testing.T) {
	for _, mode := range []string{"missing", "repo_error", "success"} {
		t.Run(mode, func(t *testing.T) {
			until := time.Now().Add(time.Minute)
			account := Account{ID: 606, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{7}, TempUnschedulableUntil: &until, TempUnschedulableReason: "transient:TRANSIENT HTTP 502"}
			svc, _ := newLastResortGatewayFixture(t, []Account{account})
			repo := &hydrationReleaseRepo{}
			if mode == "repo_error" {
				repo.err = errors.New("hydration unavailable")
			}
			if mode == "success" {
				repo.account = &account
			}
			svc.schedulerSnapshot = NewSchedulerSnapshotService(nil, nil, repo, nil, nil)
			var acquired, released []int64
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})
			groupID := int64(7)
			result := svc.resilienceGatewayLastResort(context.Background(), &Group{ID: 7, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}, &groupID, "", "claude-sonnet-4", nil, nil)
			require.Equal(t, []int64{606}, acquired)
			if mode == "success" {
				require.NotNil(t, result)
				require.Empty(t, released)
				result.ReleaseFunc()
			} else {
				require.Nil(t, result)
			}
			require.Equal(t, []int64{606}, released)
		})
	}
}
