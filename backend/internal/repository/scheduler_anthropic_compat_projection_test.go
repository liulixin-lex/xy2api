//go:build unit

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerProjectionPreservesAnthropicCompatDrainSetting(t *testing.T) {
	for _, acceptsNew := range []bool{false, true} {
		metadata := buildSchedulerMetadataAccount(service.Account{Extra: map[string]any{service.AnthropicCompatNewSessionsExtraKey: acceptsNew}})
		require.Contains(t, metadata.Extra, service.AnthropicCompatNewSessionsExtraKey)
		require.Equal(t, acceptsNew, metadata.Extra[service.AnthropicCompatNewSessionsExtraKey])
	}
}

// Exercise scheduling on the same light account projection Redis publishes.
// No provenance store or signature lookup participates in any case.
type compatAdmissionSnapshot struct {
	service.SchedulerCache
	accounts []*service.Account
}

func (s *compatAdmissionSnapshot) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	result := make([]*service.Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		projected := buildSchedulerMetadataAccount(*account)
		result = append(result, &projected)
	}
	return result, true, nil
}
func (s *compatAdmissionSnapshot) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range s.accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return nil, nil
}

func TestAnthropicCompatAdmissionUsesStickyOwnerAndMapping(t *testing.T) {
	for _, loadAware := range []bool{false, true} {
		for _, tc := range []struct {
			name                                         string
			owner                                        int64
			draining                                     bool
			model                                        string
			missingSeed, requiredBetaBlocked, onlyCompat bool
			want                                         int64
		}{
			{name: "new_session_draining", draining: true, model: "public-model", want: 504},
			{name: "bound_session_draining", owner: 503, draining: true, model: "public-model", want: 503},
			{name: "bound_elsewhere_draining", owner: 504, draining: true, model: "public-model", want: 504},
			{name: "unknown_signatures_admitted", model: "public-model", want: 503},
			{name: "model_switch", owner: 503, model: "gpt-5", want: 504},
			{name: "missing_seed", model: "public-model", missingSeed: true, want: 504},
			{name: "required_beta_blocked", model: "public-model", requiredBetaBlocked: true, want: 504},
			{name: "no_candidate", draining: true, model: "public-model", onlyCompat: true},
		} {
			t.Run(fmt.Sprintf("load_aware=%t/%s", loadAware, tc.name), func(t *testing.T) {
				cache, _ := newSchedulerCacheUnitWithRedis(t)
				sticky := NewGatewayCache(cache.rdb)
				account := &service.Account{ID: 503, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
					Credentials: map[string]any{"api_key": "fixture", "base_url": "https://custom.example", "model_mapping": map[string]any{"public-model": "claude-sonnet-5-5"}},
					Extra:       map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCode, service.AnthropicCompatNewSessionsExtraKey: !tc.draining, service.AnthropicCompatIdentitySeedExtraKey: strings.Repeat("ab", 32)}}
				if tc.missingSeed {
					delete(account.Extra, service.AnthropicCompatIdentitySeedExtraKey)
				}
				ordinary := &service.Account{ID: 504, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 10}
				snapshot := &compatAdmissionSnapshot{accounts: []*service.Account{account}}
				if !tc.onlyCompat {
					snapshot.accounts = append(snapshot.accounts, ordinary)
				}
				projection := buildSchedulerMetadataAccount(*account)
				require.NotContains(t, projection.Credentials, "base_url")
				cfg := &config.Config{}
				var settings *service.SettingService
				if tc.requiredBetaBlocked {
					settings = service.NewSettingService(&compatAdmissionSettings{}, cfg)
				}
				gateway := service.NewGatewayService(nil, nil, nil, nil, nil, nil, nil, sticky, cfg, service.NewSchedulerSnapshotService(snapshot, nil, nil, nil, cfg), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil, nil, nil, nil, nil, nil)
				// Includes more than the deleted 128-block history cap and unregistered signatures.
				history := []byte(`{"messages":[{"role":"assistant","content":[` + strings.Repeat(`{"type":"thinking","thinking":"old","signature":"unknown"},`, 130) + `{"type":"text","text":"answer"}]}]}`)
				ctx := service.WithAnthropicCompatRequest(context.Background(), 1, 2, history, "messages", "session", nil)
				if tc.owner > 0 {
					require.NoError(t, sticky.SetSessionAccountID(ctx, 0, "session", tc.owner, time.Hour))
				}
				var selected *service.Account
				var err error
				if loadAware {
					var result *service.AccountSelectionResult
					result, err = gateway.SelectAccountWithLoadAwareness(ctx, nil, "session", tc.model, nil, "", 0)
					if result != nil {
						selected = result.Account
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
					}
				} else {
					selected, err = gateway.SelectAccountForModelWithExclusions(ctx, nil, "session", tc.model, nil)
				}
				if tc.want == 0 {
					require.ErrorIs(t, err, service.ErrNoAvailableAccounts)
					require.Nil(t, selected)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tc.want, selected.ID)
				if tc.want == 503 {
					require.Equal(t, "https://custom.example", selected.GetBaseURL())
				}
			})
		}
	}
}

type compatAdmissionSettings struct{ service.SettingRepository }

func (*compatAdmissionSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyBetaPolicySettings {
		return `{"rules":[{"beta_token":"claude-code-20250219","action":"block","scope":"all"}]}`, nil
	}
	return "", service.ErrSettingNotFound
}

// Count real sticky-cache reads while still exercising projected scheduling accounts.
type ordinaryStickyReadCounter struct {
	service.GatewayCache
	reads int
}

func (c *ordinaryStickyReadCounter) GetSessionAccountID(ctx context.Context, group int64, hash string) (int64, error) {
	c.reads++
	return c.GatewayCache.GetSessionAccountID(ctx, group, hash)
}
func TestLegacyOrdinarySchedulingDoesNotReadCompatStickyOwner(t *testing.T) {
	for _, forcePlatform := range []bool{false, true} {
		for _, bound := range []bool{false, true} {
			t.Run(fmt.Sprintf("single=%t/bound=%t", forcePlatform, bound), func(t *testing.T) {
				cache, _ := newSchedulerCacheUnitWithRedis(t)
				sticky := &ordinaryStickyReadCounter{GatewayCache: NewGatewayCache(cache.rdb)}
				account := &service.Account{ID: 503, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://ordinary.example"}, Extra: map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileOff}}
				snapshot := &compatAdmissionSnapshot{accounts: []*service.Account{account}}
				cfg := &config.Config{}
				gateway := service.NewGatewayService(nil, nil, nil, nil, nil, nil, nil, sticky, cfg, service.NewSchedulerSnapshotService(snapshot, nil, nil, nil, cfg), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				ctx := context.Background()
				if forcePlatform {
					ctx = context.WithValue(ctx, ctxkey.ForcePlatform, service.PlatformAnthropic)
				}
				if bound {
					require.NoError(t, sticky.SetSessionAccountID(ctx, 0, "ordinary", account.ID, time.Hour))
				}
				selected, err := gateway.SelectAccountForModelWithExclusions(ctx, nil, "ordinary", "claude-sonnet-5-5", nil)
				require.NoError(t, err)
				require.Equal(t, account.ID, selected.ID)
				require.Equal(t, 1, sticky.reads, "ordinary pools need only the existing sticky selection read")
			})
		}
	}
}
