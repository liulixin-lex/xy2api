//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// batchStatusUserRepoStub 只实现批量状态能力；嵌入的 UserRepository 为 nil，
// 任何其他仓储调用都会 panic，从而证明批量路径不会逐个读写用户。
type batchStatusUserRepoStub struct {
	UserRepository
	calls   []batchStatusRepoCall
	updated []int64
	err     error
}

type batchStatusRepoCall struct {
	userIDs []int64
	status  string
}

func (s *batchStatusUserRepoStub) BatchUpdateStatus(_ context.Context, userIDs []int64, status string) ([]int64, error) {
	s.calls = append(s.calls, batchStatusRepoCall{userIDs: append([]int64(nil), userIDs...), status: status})
	return s.updated, s.err
}

func TestAdminService_BatchUpdateUserStatus(t *testing.T) {
	t.Run("dedupes ids and invalidates only changed users", func(t *testing.T) {
		repo := &batchStatusUserRepoStub{updated: []int64{3, 5}}
		invalidator := &authCacheInvalidatorStub{}
		svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}

		result, err := svc.BatchUpdateUserStatus(context.Background(), []int64{3, 0, 5, 3, -1, 8}, StatusDisabled, 1)

		require.NoError(t, err)
		require.Equal(t, []batchStatusRepoCall{{userIDs: []int64{3, 5, 8}, status: StatusDisabled}}, repo.calls)
		require.Equal(t, BatchUserStatusResult{Requested: 3, UpdatedIDs: []int64{3, 5}}, result)
		require.Equal(t, []int64{3, 5}, invalidator.userIDs)
	})

	t.Run("enables users", func(t *testing.T) {
		repo := &batchStatusUserRepoStub{updated: []int64{4}}
		svc := &adminServiceImpl{userRepo: repo}

		result, err := svc.BatchUpdateUserStatus(context.Background(), []int64{4}, StatusActive, 1)

		require.NoError(t, err)
		require.Equal(t, StatusActive, repo.calls[0].status)
		require.Equal(t, BatchUserStatusResult{Requested: 1, UpdatedIDs: []int64{4}}, result)
	})

	t.Run("rejects unknown status before touching the repository", func(t *testing.T) {
		repo := &batchStatusUserRepoStub{}
		svc := &adminServiceImpl{userRepo: repo}

		for _, status := range []string{"", "deleted", "Disabled"} {
			_, err := svc.BatchUpdateUserStatus(context.Background(), []int64{1}, status, 1)
			require.ErrorIs(t, err, ErrInvalidBatchUserStatus, status)
			require.Equal(t, "INVALID_USER_STATUS", infraerrors.Reason(err))
		}
		require.Empty(t, repo.calls)
	})

	t.Run("no valid ids is a no-op", func(t *testing.T) {
		repo := &batchStatusUserRepoStub{}
		svc := &adminServiceImpl{userRepo: repo}

		result, err := svc.BatchUpdateUserStatus(context.Background(), []int64{0, -2}, StatusDisabled, 1)

		require.NoError(t, err)
		require.Equal(t, BatchUserStatusResult{}, result)
		require.Empty(t, repo.calls)
	})

	t.Run("repository without the capability fails closed", func(t *testing.T) {
		svc := &adminServiceImpl{userRepo: &userRepoStub{}}

		_, err := svc.BatchUpdateUserStatus(context.Background(), []int64{1}, StatusDisabled, 1)

		require.ErrorIs(t, err, ErrUserStatusBatchUnavailable)
	})

	t.Run("repository error is returned without invalidating caches", func(t *testing.T) {
		repoErr := errors.New("db down")
		repo := &batchStatusUserRepoStub{err: repoErr}
		invalidator := &authCacheInvalidatorStub{}
		svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}

		_, err := svc.BatchUpdateUserStatus(context.Background(), []int64{1}, StatusDisabled, 1)

		require.ErrorIs(t, err, repoErr)
		require.Empty(t, invalidator.userIDs)
	})
}
