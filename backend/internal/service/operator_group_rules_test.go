//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
	"reflect"
	"slices"
	"testing"
)

func TestOperatorGroupRules(t *testing.T) {
	s, _, groups := operatorUserFixture()
	groups.rows[1].RateMultiplier = 1
	groups.rows[1].Status = StatusActive
	ctx := operatorRulesContext(s)
	for _, in := range []*CreateGroupInput{{}, {IsExclusive: true, FallbackGroupID: operatorPtr(int64(2))}, {IsExclusive: true, AvailabilityFallbackGroupID: operatorPtr(int64(2))}} {
		require.Error(t, s.operatorGroupCreateRules(ctx, in))
	}
	require.NoError(t, s.operatorGroupCreateRules(ctx, &CreateGroupInput{IsExclusive: true}))
	for _, in := range []*UpdateGroupInput{{RateMultiplier: operatorPtr(2.0)}, {IsExclusive: operatorPtr(false)}, {FallbackGroupID: operatorPtr(int64(2))}} {
		require.Error(t, s.operatorGroupUpdateRules(ctx, 1, in))
	}
	require.NoError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{RateMultiplier: operatorPtr(1.0), FallbackGroupID: operatorPtr(int64(0))}))
	groups.rows[1].Status = StatusDisabled
	operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{Status: StatusActive}), "ADMIN_FIELD_FORBIDDEN", "status", "")
	require.NoError(t, s.operatorGroupUpdateRules(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust), 1, &UpdateGroupInput{RateMultiplier: operatorPtr(2.0)}))
	s.apiKeyRepo = &operatorGuardAPIKeys{key: &APIKey{UserID: 1}}
	operatorRuleError(t, s.operatorAPIKeyGroupRules(ctx, 1, operatorPtr(int64(2))), "ADMIN_FIELD_FORBIDDEN", "group_id", "")
	require.NoError(t, s.operatorAPIKeyGroupRules(ctx, 1, operatorPtr(int64(1))))
	require.NoError(t, s.operatorAPIKeyGroupRules(ctx, 1, operatorPtr(int64(3))))
	operatorRuleError(t, s.operatorReplaceGroupRules(ctx, 1, 2), "ADMIN_FIELD_FORBIDDEN", "new_group_id", "")
	require.NoError(t, s.operatorReplaceGroupRules(ctx, 1, 3))
	operatorRuleError(t, s.operatorReplaceGroupRules(ctx, 1, 4), "ADMIN_FIELD_FORBIDDEN", "new_group_id", "")
}
func TestOperatorGroupNullLimitIsAChange(t *testing.T) {
	s, _, groups := operatorUserFixture()
	ctx := operatorRulesContext(s)
	groups.rows[1].DailyLimitUSD = operatorPtr(10.0)
	operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{DailyLimitUSD: operatorPtr(-1.0)}), "ADMIN_FIELD_FORBIDDEN", "daily_limit_usd", "")
	groups.rows[1].DailyLimitUSD = nil
	require.NoError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{DailyLimitUSD: operatorPtr(-1.0)}))
}
func TestOperatorGroupFieldListCoversInputs(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[CreateGroupInput](), reflect.TypeFor[UpdateGroupInput]()} {
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			require.True(t, slices.Contains(OperatorGroupPriceFields, name) || slices.Contains(OperatorGroupFreeFields, name), name)
			if slices.Contains(OperatorGroupPriceFields, name) {
				require.NotEmpty(t, operatorGroupJSONNames[name], name)
			}
		}
	}
}
func TestOperatorCopyAccountsNeedsBalanceFlag(t *testing.T) {
	s, _, _ := operatorUserFixture()
	ctx := operatorRulesContext(s)
	operatorRuleError(t, s.operatorGroupCreateRules(ctx, &CreateGroupInput{IsExclusive: true, CopyAccountsFromGroupIDs: []int64{2}}), "ADMIN_FIELD_FORBIDDEN", "copy_accounts_from_group_ids", "")
	operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{CopyAccountsFromGroupIDs: []int64{2}}), "ADMIN_FIELD_FORBIDDEN", "copy_accounts_from_group_ids", "")
	require.NoError(t, s.operatorGroupCreateRules(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust), &CreateGroupInput{CopyAccountsFromGroupIDs: []int64{2}}))
}
func TestOperatorDeleteGroupWithAccountsNeedsBalanceFlag(t *testing.T) {
	s, _, groups := operatorUserFixture()
	s.emptyGroupDeleteRepo = groups
	ctx := operatorRulesContext(s)
	groups.notEmpty = true
	require.ErrorIs(t, s.DeleteGroup(ctx, 1), ErrGroupNotEmpty)
	require.Zero(t, groups.cascade)
	groups.notEmpty = false
	require.NoError(t, s.DeleteGroup(ctx, 1))
	require.Equal(t, 2, groups.empty)
	require.NoError(t, s.DeleteGroup(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust), 1))
	require.Equal(t, 1, groups.cascade)
	require.NoError(t, s.DeleteGroup(operatoraccess.WithSuperAdmin(context.Background(), 2), 1))
	require.Equal(t, 2, groups.cascade)
}
