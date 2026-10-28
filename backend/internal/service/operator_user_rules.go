package service

import (
	"context"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"slices"
	"strings"
)

func (s *adminServiceImpl) operatorUserCreateRules(ctx context.Context, input *CreateUserInput) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	if input.Role != "" {
		return operatorFieldError("role")
	}
	if sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		return nil
	}
	if input.Balance != nil && *input.Balance != 0 {
		return operatorFieldError("balance")
	}
	if input.Balance == nil {
		input.Balance = new(float64)
	}
	if isReservedEmail(strings.TrimSpace(input.Email)) {
		return operatorFieldError("email")
	}
	return s.operatorAllowedGroups(ctx, input.AllowedGroups, nil, "allowed_groups")
}
func (s *adminServiceImpl) operatorSkipsSignupGrants(ctx context.Context) bool {
	sc, _, err := s.operatorScope(ctx)
	return err != nil || (sc != nil && !sc.Has(operatoraccess.FlagUsersBalanceAdjust))
}
func (s *adminServiceImpl) operatorUserUpdateRules(ctx context.Context, id int64, input *UpdateUserInput) error {
	sc, svc, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	if svc.userRepo == nil {
		return operatorUnavailable()
	}
	user, err := svc.userRepo.GetByIDIncludeDeleted(ctx, id)
	if errors.Is(err, ErrUserNotFound) || (err == nil && user == nil) {
		return ErrUserNotFound
	}
	if err != nil {
		return operatorUnavailable()
	}
	if user.Role != RoleUser || user.ID == sc.UserID {
		return operatorTargetError()
	}
	if input.Role != "" {
		return operatorFieldError("role")
	}
	if sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		return nil
	}
	if input.Balance != nil {
		return operatorFieldError("balance")
	}
	if input.GroupRates != nil {
		return operatorFieldError("group_rates")
	}
	if input.Email != "" && isReservedEmail(input.Email) && !strings.EqualFold(strings.TrimSpace(input.Email), strings.TrimSpace(user.Email)) {
		return operatorFieldError("email")
	}
	if input.AllowedGroups != nil {
		return s.operatorAllowedGroups(ctx, *input.AllowedGroups, user.AllowedGroups, "allowed_groups")
	}
	return nil
}
func (s *adminServiceImpl) operatorCheckUserTargets(ctx context.Context, ids []int64) error {
	sc, svc, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	positive := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			positive = append(positive, id)
		}
	}
	return svc.operatorTargetUsers(ctx, sc, positive, "user_ids")
}
func (s *adminServiceImpl) operatorBindIdentityRules(ctx context.Context, input AdminBindAuthIdentityInput) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	if len(input.Metadata) > 0 {
		return operatorFieldError("metadata")
	}
	if input.Channel != nil && len(input.Channel.Metadata) > 0 {
		return operatorFieldError("channel.metadata")
	}
	return nil
}
func (s *adminServiceImpl) operatorAllowedGroups(ctx context.Context, ids, existing []int64, field string) error {
	for _, id := range ids {
		if slices.Contains(existing, id) {
			continue
		}
		if s.groupRepo == nil {
			return operatorUnavailable()
		}
		group, err := s.groupRepo.GetByID(ctx, id)
		if err != nil || group == nil {
			return operatorUnavailable()
		}
		if group.IsExclusive && group.SubscriptionType != "subscription" {
			return operatorFieldError(field)
		}
	}
	return nil
}
