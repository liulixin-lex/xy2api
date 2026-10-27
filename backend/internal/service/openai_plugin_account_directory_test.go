package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type pluginDirRepoStub struct {
	AccountRepository
	byPlatform map[string][]Account
}

func (r *pluginDirRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	return r.byPlatform[platform], nil
}

func TestListPluginAccountsScopeAndSchedulable(t *testing.T) {
	future := time.Now().Add(time.Hour)
	parentID := int64(1)
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Name: "primary", Credentials: map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh"},
			Extra: map[string]any{"existing_key": "visible", "openai_compact_mode": "auto", "codex_turn_ticket:gpt-6-astra": map[string]any{"state": "private-state"}}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, TempUnschedulableUntil: &future, TempUnschedulableReason: "upstream cooldown"},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, RateLimitResetAt: &future},
		{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, ParentAccountID: &parentID},
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
	}
	svc := &OpenAIGatewayService{accountRepo: &pluginDirRepoStub{byPlatform: map[string][]Account{PlatformOpenAI: accounts}}}
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	infos, err := svc.ListPluginAccounts(context.Background(), scope, "", "")
	require.NoError(t, err)
	require.Len(t, infos, 3)
	got := map[int64]PluginAccountInfo{}
	for _, info := range infos {
		got[info.ID] = info
	}
	require.True(t, got[1].Schedulable)
	require.False(t, got[2].Schedulable)
	require.False(t, got[3].Schedulable)
	require.NotContains(t, got, int64(4))
	require.NotContains(t, got, int64(5))
	metadata := string(got[1].MetadataJSON)
	require.NotContains(t, metadata, "fixture-access")
	require.NotContains(t, metadata, "fixture-refresh")
	require.NotContains(t, metadata, "private-state")
	require.Contains(t, metadata, "visible")
	require.Contains(t, metadata, "openai_compact_mode")
	require.Contains(t, string(got[2].MetadataJSON), "upstream cooldown")
	require.Contains(t, accounts[0].Extra, "codex_turn_ticket:gpt-6-astra", "snapshot creation must not mutate the source account")
}

func TestPluginAccountSnapshotClassifiesAccountFields(t *testing.T) {
	stripped := map[string]struct{}{
		"Credentials": {}, "Groups": {}, "AccountGroups": {}, "IQPreserveTokenRotation": {},
		"IQTargetsPatch": {}, "IQTargets": {}, "IQRequeueModels": {},
	}
	safe := map[string]struct{}{
		"ID": {}, "Name": {}, "Notes": {}, "Platform": {}, "Type": {}, "Extra": {}, "Proxy": {}, "ProxyID": {}, "ProxyFallbackOriginID": {}, "ProxyFallbackOriginName": {},
		"Concurrency": {}, "Priority": {}, "RateMultiplier": {}, "LoadFactor": {}, "Status": {}, "ErrorMessage": {}, "LastUsedAt": {}, "ExpiresAt": {},
		"AutoPauseOnExpired": {}, "CreatedAt": {}, "UpdatedAt": {}, "Schedulable": {}, "RateLimitedAt": {}, "RateLimitResetAt": {}, "OverloadUntil": {},
		"TempUnschedulableUntil": {}, "TempUnschedulableReason": {}, "SessionWindowStart": {}, "SessionWindowEnd": {}, "SessionWindowStatus": {},
		"ParentAccountID": {}, "QuotaDimension": {}, "GroupIDs": {},
	}
	typ := reflect.TypeOf(Account{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		if _, ok := stripped[field.Name]; ok {
			continue
		}
		if _, ok := safe[field.Name]; !ok {
			t.Fatalf("Account.%s needs an explicit plugin snapshot classification", field.Name)
		}
	}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"refresh_token": "private-refresh"}, Extra: map[string]any{"readable": true}}
	data := accountReadableSnapshotJSON(account)
	require.NotNil(t, data)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotContains(t, string(data), "private-refresh")
	require.Contains(t, string(data), "readable")
}
