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

func TestOperatorGroupMoneyFieldsNeedBalanceFlag(t *testing.T) {
	s, _, groups := operatorUserFixture()
	groups.rows[1].Platform = PlatformOpenAI
	groups.rows[1].ForceOpenAIFast = false
	ctx := operatorRulesContext(s)
	for _, tc := range []struct {
		input *UpdateGroupInput
		field string
	}{
		{&UpdateGroupInput{Platform: PlatformComposite}, "platform"},
		{&UpdateGroupInput{ForceOpenAIFast: operatorPtr(true)}, "force_openai_fast"},
	} {
		operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, tc.input), "ADMIN_FIELD_FORBIDDEN", tc.field, "")
	}
	for _, field := range []string{"Platform", "ForceOpenAIFast"} {
		require.Contains(t, OperatorGroupPriceFields, field)
		require.NotContains(t, OperatorGroupFreeFields, field)
	}
	for _, in := range []*UpdateGroupInput{{Platform: PlatformComposite}, {ForceOpenAIFast: operatorPtr(true)}} {
		require.NoError(t, s.operatorGroupUpdateRules(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust), 1, in))
	}
	for _, field := range []string{"MaxReasoningEffort", "ReasoningEffortMappings"} {
		require.Contains(t, OperatorGroupPriceFields, field)
		require.NotContains(t, OperatorGroupFreeFields, field)
	}
	operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{MaxReasoningEffort: operatorPtr("high")}), "ADMIN_FIELD_FORBIDDEN", "max_reasoning_effort", "")
	operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{ReasoningEffortMappings: &[]ReasoningEffortMapping{{From: "low", To: "high"}}}), "ADMIN_FIELD_FORBIDDEN", "reasoning_effort_mappings", "")
	require.NoError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{ImagePrice1K: operatorPtr(-1.0)}))
}

func TestOperatorGroupEntityLoadNotFoundAndUnavailable(t *testing.T) {
	s, _, groups := operatorUserFixture()
	ctx := operatorRulesContext(s)
	require.ErrorIs(t, s.operatorGroupUpdateRules(ctx, 99, &UpdateGroupInput{}), ErrGroupNotFound)
	require.ErrorIs(t, s.operatorAllowedGroups(ctx, []int64{99}, nil, "allowed_groups"), ErrGroupNotFound)
	require.ErrorIs(t, s.operatorReplaceGroupRules(ctx, 99, 1), ErrUserNotFound)
	require.ErrorIs(t, s.operatorReplaceGroupRules(ctx, 1, 99), ErrGroupNotFound)

	s.apiKeyRepo = &operatorGuardAPIKeys{}
	ctx = operatorRulesContext(s)
	require.ErrorIs(t, s.operatorAPIKeyGroupRules(ctx, 99, operatorPtr(int64(2))), ErrAPIKeyNotFound)
	s.apiKeyRepo = &operatorGuardAPIKeys{key: &APIKey{UserID: 99}}
	ctx = operatorRulesContext(s)
	require.ErrorIs(t, s.operatorAPIKeyGroupRules(ctx, 1, operatorPtr(int64(2))), ErrUserNotFound)

	s.groupRepo = nil
	ctx = operatorRulesContext(s)
	operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{}), "OPERATOR_ACCESS_UNAVAILABLE", "", "")
	operatorRuleError(t, s.operatorAllowedGroups(ctx, []int64{2}, nil, "allowed_groups"), "OPERATOR_ACCESS_UNAVAILABLE", "", "")
	operatorRuleError(t, s.operatorReplaceGroupRules(ctx, 1, 2), "OPERATOR_ACCESS_UNAVAILABLE", "", "")
	s.groupRepo = groups
	groups.err = ErrGroupNotEmpty
	ctx = operatorRulesContext(s)
	operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{}), "OPERATOR_ACCESS_UNAVAILABLE", "", "")
	groups.err = nil
	s.userRepo = nil
	ctx = operatorRulesContext(s)
	operatorRuleError(t, s.operatorReplaceGroupRules(ctx, 1, 2), "OPERATOR_ACCESS_UNAVAILABLE", "", "")
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

func TestOperatorGroupNormalizedMoneyRoundTrip(t *testing.T) {
	s, _, groups := operatorUserFixture()
	group := groups.rows[1]
	group.Platform = PlatformOpenAI
	group.MaxReasoningEffort = "high"
	group.ReasoningEffortMappings = []ReasoningEffortMapping{{From: "low", To: "high"}}
	ctx := operatorRulesContext(s)
	require.NoError(t, s.operatorGroupUpdateRules(ctx, 1, &UpdateGroupInput{MaxReasoningEffort: operatorPtr(" HIGH "), ReasoningEffortMappings: &[]ReasoningEffortMapping{{From: " LOW ", To: " HIGH "}}}))
	for _, name := range []string{"ImagePrice1K", "ImagePrice2K", "ImagePrice4K", "VideoPrice480P", "VideoPrice720P", "VideoPrice1080P", "WebSearchPricePerCall", "SearchPricePer1k", "AudioRealtimePricePerMin", "AudioTTSPricePerMillionChars", "AudioSTTPricePerHour"} {
		t.Run(name, func(t *testing.T) {
			input := &UpdateGroupInput{Name: "renamed"}
			reflect.ValueOf(input).Elem().FieldByName(name).Set(reflect.ValueOf(operatorPtr(-1.0)))
			require.NoError(t, s.operatorGroupUpdateRules(ctx, 1, input))
			reflect.ValueOf(input).Elem().FieldByName(name).Set(reflect.ValueOf(operatorPtr(0.0)))
			operatorRuleError(t, s.operatorGroupUpdateRules(ctx, 1, input), "ADMIN_FIELD_FORBIDDEN", operatorGroupJSONNames[name], "")
		})
	}
}
