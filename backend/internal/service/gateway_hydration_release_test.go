//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

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
