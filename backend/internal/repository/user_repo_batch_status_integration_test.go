//go:build integration

package repository

import (
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (s *UserRepoSuite) TestBatchUpdateStatusDisablesOnlyChangeableUsers() {
	first := s.mustCreateUser(&service.User{Email: "batch-status-first@test.com"})
	second := s.mustCreateUser(&service.User{Email: "batch-status-second@test.com"})
	admin := s.mustCreateUser(&service.User{Email: "batch-status-admin@test.com", Role: service.RoleAdmin})
	alreadyDisabled := s.mustCreateUser(&service.User{Email: "batch-status-disabled@test.com", Status: service.StatusDisabled})
	deleted := s.mustCreateUser(&service.User{Email: "batch-status-deleted@test.com"})
	s.Require().NoError(s.repo.Delete(s.ctx, deleted.ID))

	updated, err := s.repo.BatchUpdateStatus(s.ctx,
		[]int64{first.ID, second.ID, admin.ID, alreadyDisabled.ID, deleted.ID, 987654321},
		service.StatusDisabled,
	)
	s.Require().NoError(err)
	s.ElementsMatch([]int64{first.ID, second.ID}, updated)

	for id, want := range map[int64]string{
		first.ID:           service.StatusDisabled,
		second.ID:          service.StatusDisabled,
		admin.ID:           service.StatusActive,
		alreadyDisabled.ID: service.StatusDisabled,
	} {
		got, err := s.repo.GetByID(s.ctx, id)
		s.Require().NoError(err)
		s.Equal(want, got.Status, "user %d", id)
	}
	_, err = s.repo.GetByID(s.ctx, deleted.ID)
	s.Require().ErrorIs(err, service.ErrUserNotFound)
}

func (s *UserRepoSuite) TestBatchUpdateStatusEnablesUsersIncludingAdmins() {
	disabled := s.mustCreateUser(&service.User{Email: "batch-enable-disabled@test.com", Status: service.StatusDisabled})
	active := s.mustCreateUser(&service.User{Email: "batch-enable-active@test.com"})
	disabledAdmin := s.mustCreateUser(&service.User{
		Email:  "batch-enable-admin@test.com",
		Role:   service.RoleAdmin,
		Status: service.StatusDisabled,
	})

	updated, err := s.repo.BatchUpdateStatus(s.ctx, []int64{disabled.ID, active.ID, disabledAdmin.ID}, service.StatusActive)
	s.Require().NoError(err)
	s.ElementsMatch([]int64{disabled.ID, disabledAdmin.ID}, updated)

	for _, id := range []int64{disabled.ID, active.ID, disabledAdmin.ID} {
		got, err := s.repo.GetByID(s.ctx, id)
		s.Require().NoError(err)
		s.Equal(service.StatusActive, got.Status, "user %d", id)
	}
}
