//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDuplicateAccountDropsNewAPIToken(t *testing.T) {
	repo := newDuplicateAccountRepoStub()
	source := &Account{Name: "source", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Credentials: map[string]any{"api_key": "fixture-key", "newapi_access_token": "fixture-token", "newapi_user_id": "123"}}
	require.NoError(t, repo.Create(context.Background(), source))
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}
	duplicate, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:1", "")
	require.NoError(t, err)
	require.NotContains(t, duplicate.Credentials, "newapi_access_token")
	require.Equal(t, "fixture-token", source.GetCredential("newapi_access_token"))
}

func TestNewAPIUserIDValidation(t *testing.T) {
	for _, id := range []any{nil, "", "1", strings.Repeat("9", 20)} {
		require.NoError(t, validateNewAPIAccessCredentials(map[string]any{"newapi_user_id": id}))
	}
	for _, id := range []any{"x", "-1", " 1", "1.0", strings.Repeat("9", 21), 123, true} {
		require.ErrorIs(t, validateNewAPIAccessCredentials(map[string]any{"newapi_user_id": id}), ErrNewAPIUserIDInvalid)
	}
}

func TestCNBalanceRelayRunOnceSkips(t *testing.T) {
	repo := &fakeCNCheckRepo{byPlatform: map[string][]Account{}}
	for i, platform := range []string{PlatformKimi, PlatformDeepseek} {
		repo.byPlatform[platform] = []Account{{ID: int64(i + 1), Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"account_mode": "payg", "base_url": "https://relay.example/v1", "api_key": "fixture-key"}}}
	}
	loadRepo := &recordingCNBalanceLoadRepo{}
	svc := &CNProviderBalanceCheckService{accountRepo: repo, balanceService: NewCNProviderBalanceService(loadRepo, nil, nil, &config.Config{}), cfg: &config.Config{}}
	svc.runOnce()
	require.Empty(t, loadRepo.getByIDIDs)
}

func TestChannelMonitorRelayCNBalanceNeutral(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			fetcher, usage, quota, balance, accounts := newQuotaFetcherTestSetup(t)
			accounts.accounts[1] = &Account{ID: 1, Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": "payg", "base_url": "https://relay.example", "api_key": "fixture-key"}}
			snapshot := fetcher.Fetch(context.Background(), 1)
			require.True(t, snapshot.Success)
			require.False(t, snapshot.CredentialInvalid)
			require.False(t, snapshot.BalanceLow)
			require.Nil(t, snapshot.Balance)
			require.Empty(t, snapshot.Tiers)
			require.Empty(t, snapshot.Error)
			require.Zero(t, usage.getCalls())
			require.Zero(t, quota.calls)
			require.Zero(t, balance.calls)
		})
	}
}

// Apply the same top-level JSONB merge as the account repository so the test
// verifies stored tokens for every selected account, including null clearing.
type upstreamBalanceBulkTokenRepo struct {
	*upstreamBillingProbeAccountRepo
}

func (r *upstreamBalanceBulkTokenRepo) BulkUpdate(_ context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	r.bulkUpdates = append(r.bulkUpdates, updates)
	for _, id := range ids {
		for key, value := range updates.Credentials {
			r.accounts[id].Credentials[key] = value
		}
	}
	return int64(len(ids)), nil
}
func (r *upstreamBalanceBulkTokenRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}

func TestUpdateAccountRejectsInvalidNewAPIUserID(t *testing.T) {
	repo := &updateAccountCredsRepoStub{account: &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}}
	svc := &adminServiceImpl{accountRepo: repo}
	_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Credentials: map[string]any{"newapi_user_id": "abc"}})
	require.ErrorIs(t, err, ErrNewAPIUserIDInvalid)
	require.Zero(t, repo.updateCalls)
}

func TestUpdateAndBulkUpdateClearTokenOnProxyChange(t *testing.T) {
	for _, scenario := range []string{"set", "change", "clear", "same", "new-token"} {
		for _, bulk := range []bool{false, true} {
			name := scenario + "/single"
			if bulk {
				name = scenario + "/bulk"
			}
			t.Run(name, func(t *testing.T) {
				oldID, newID := int64(1), int64(2)
				a := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, ProxyID: &oldID, Credentials: map[string]any{"base_url": "https://relay.example", "api_key": "fixture-key", "newapi_access_token": "original-token"}}
				var delta map[string]any
				switch scenario {
				case "set":
					a.ProxyID = nil
				case "clear":
					newID = 0
				case "same":
					newID = oldID
				case "new-token":
					delta = map[string]any{"newapi_access_token": "replacement-token"}
				}
				repo := &upstreamBalanceBulkTokenRepo{&upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{1: a}}}
				svc := &adminServiceImpl{accountRepo: repo}
				if bulk {
					_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, ProxyID: &newID, Credentials: delta})
					require.NoError(t, err)
					require.Len(t, repo.bulkUpdates, 1)
					if scenario != "same" && scenario != "new-token" {
						require.Contains(t, repo.bulkUpdates[0].Credentials, "newapi_access_token")
						require.Nil(t, repo.bulkUpdates[0].Credentials["newapi_access_token"])
					}
				} else {
					_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{ProxyID: &newID, Credentials: delta})
					require.NoError(t, err)
				}
				a = repo.accounts[1]
				switch scenario {
				case "same":
					require.Equal(t, "original-token", a.GetCredential("newapi_access_token"))
				case "new-token":
					require.Equal(t, "replacement-token", a.GetCredential("newapi_access_token"))
				default:
					require.Empty(t, a.GetCredential("newapi_access_token"))
				}
			})
		}
	}
}

func TestNewAPIAccessTokenBulkOriginChangeClears(t *testing.T) {
	for _, scenario := range []string{"base-change", "explicit-token", "protocol-switch", "same-origin"} {
		t.Run(scenario, func(t *testing.T) {
			accounts := map[int64]*Account{}
			for _, id := range []int64{1, 2} {
				accounts[id] = &Account{ID: id, Platform: PlatformKimi, Type: AccountTypeAPIKey, Status: StatusActive, Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://first.example/v1", "api_protocol": "chat_completions", "newapi_access_token": "original-token"}}
			}
			// Only the first account changes effective origin on a protocol-only edit.
			accounts[1].Credentials["api_base_urls"] = map[string]any{"chat_completions": "https://second.example/v1"}
			repo := &upstreamBalanceBulkTokenRepo{&upstreamBillingProbeAccountRepo{accounts: accounts}}
			delta := map[string]any{"base_url": "https://second.example/v1"}
			switch scenario {
			case "explicit-token":
				delta["newapi_access_token"] = "replacement-token"
			case "protocol-switch":
				delta = map[string]any{"api_protocol": "adaptive"}
			case "same-origin":
				delta = map[string]any{"base_url": "https://first.example/another-path"}
			}
			svc := &adminServiceImpl{accountRepo: repo}
			result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, Credentials: delta})
			require.NoError(t, err)
			require.Equal(t, 2, result.Success)
			require.Len(t, repo.bulkUpdates, 1)
			for _, account := range accounts {
				switch scenario {
				case "explicit-token":
					require.Equal(t, "replacement-token", account.GetCredential("newapi_access_token"))
				case "same-origin":
					require.Equal(t, "original-token", account.GetCredential("newapi_access_token"))
				default:
					require.Nil(t, account.Credentials["newapi_access_token"])
				}
			}
		})
	}
}
