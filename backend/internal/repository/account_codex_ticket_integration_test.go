//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketRepositoryCrossInstanceLeaseAndRollback(t *testing.T) {
	ctx := context.Background()
	one := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	two := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "state-transaction", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "fixture"}}
	require.NoError(t, one.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	var wg sync.WaitGroup
	wins := make(chan string, 2)
	errs := make(chan error, 2)
	for i, repo := range []*accountRepository{one, two} {
		wg.Add(1)
		go func(i int, r *accountRepository) {
			defer wg.Done()
			token := fmt.Sprint(i)
			_, e := r.MutateCodexTicket(ctx, a.ID, 1, func(account *service.Account, active int, now time.Time) (bool, error) {
				if active >= 1 {
					return false, nil
				}
				until := now.Add(time.Minute)
				account.Extra["codex_ticket_runtime"] = map[string]any{"gpt-6-astra": map[string]any{"lease_token": token, "lease_until": until}}
				wins <- token
				return true, nil
			})
			errs <- e
		}(i, repo)
	}
	wg.Wait()
	close(wins)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.Len(t, wins, 1)
	before, e := one.GetByID(ctx, a.ID)
	require.NoError(t, e)
	rawBefore, e := json.Marshal(before.Extra)
	require.NoError(t, e)
	_, e = two.MutateCodexTicket(ctx, a.ID, 0, func(account *service.Account, _ int, _ time.Time) (bool, error) {
		account.Extra["codex_ticket_runtime"] = nil
		return true, errors.New("injected publication failure")
	})
	require.Error(t, e)
	after, e := one.GetByID(ctx, a.ID)
	require.NoError(t, e)
	rawAfter, e := json.Marshal(after.Extra)
	require.NoError(t, e)
	require.JSONEq(t, string(rawBefore), string(rawAfter))
	// Expired worker ownership can be replaced atomically by the other instance.
	_, e = one.MutateCodexTicket(ctx, a.ID, 0, func(account *service.Account, _ int, now time.Time) (bool, error) {
		account.Extra["codex_ticket_runtime"] = map[string]any{"gpt-6-astra": map[string]any{"lease_token": "expired", "lease_until": now.Add(-time.Minute)}}
		return true, nil
	})
	require.NoError(t, e)
	_, e = two.MutateCodexTicket(ctx, a.ID, 1, func(account *service.Account, active int, now time.Time) (bool, error) {
		require.Zero(t, active)
		account.Extra["codex_ticket_runtime"] = map[string]any{"gpt-6-astra": map[string]any{"lease_token": "replacement", "lease_until": now.Add(time.Minute)}}
		return true, nil
	})
	require.NoError(t, e)
	_, e = one.MutateCodexTicket(ctx, a.ID, 0, func(account *service.Account, _ int, _ time.Time) (bool, error) {
		raw, _ := json.Marshal(account.Extra["codex_ticket_runtime"])
		var rt map[string]struct {
			Token string `json:"lease_token"`
		}
		require.NoError(t, json.Unmarshal(raw, &rt))
		require.Equal(t, "replacement", rt["gpt-6-astra"].Token)
		return false, nil
	})
	require.NoError(t, e)
}

func TestCodexTicketRepositoryLegacyScopeMigration(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	_, e := integrationDB.Exec(`DELETE FROM settings WHERE key IN ('codex_ticket_account_migration_v1',$1)`, service.SettingKeyOpenAICodexTicketEnabled)
	require.NoError(t, e)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM settings WHERE key='codex_ticket_account_migration_v1'`)
	})
	create := func(name string) *service.Account {
		a := &service.Account{Name: name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "fixture"}}
		require.NoError(t, repo.Create(ctx, a))
		t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
		return a
	}
	old := create("legacy-state-scope")
	// Simulate an account created by the pre-upgrade program, which had no per-account setting.
	_, e = integrationDB.Exec(`UPDATE accounts SET extra=extra-'codex_ticket_config' WHERE id=$1`, old.ID)
	require.NoError(t, e)
	during := create("new-before-migration-marker")
	cfg := config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra", "gpt-5.6-sol"}, FailClosed: false}
	readyRepo, initErr := ProvideStateReadyAccountRepository(testEntClient(t), integrationDB, nil, &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: cfg}})
	require.NoError(t, initErr)
	require.NotNil(t, readyRepo)
	fresh, e := repo.GetByID(ctx, old.ID)
	require.NoError(t, e)
	raw, e := json.Marshal(fresh.Extra["codex_ticket_config"])
	require.NoError(t, e)
	var settings struct {
		Enabled bool     `json:"enabled"`
		Models  []string `json:"models"`
		Policy  string   `json:"missing_policy"`
	}
	require.NoError(t, json.Unmarshal(raw, &settings))
	require.True(t, settings.Enabled)
	require.Len(t, settings.Models, 2)
	require.Equal(t, "allow_unprotected", settings.Policy)
	duringAfter, e := repo.GetByID(ctx, during.ID)
	require.NoError(t, e)
	require.Empty(t, service.OpenAICodexTicketStatuses(duringAfter, cfg, time.Now()), "new accounts stay disabled even before the first migration marker is committed")
	newAccount := create("new-state-default-off")
	require.NoError(t, repo.MigrateCodexTicketAccounts(ctx, cfg))
	fresh, e = repo.GetByID(ctx, newAccount.ID)
	require.NoError(t, e)
	require.Empty(t, service.OpenAICodexTicketStatuses(fresh, cfg, time.Now()))
	// Ordinary edits cannot install imported runtime state or replace private settings.
	old.Extra = map[string]any{"codex_ticket_config": map[string]any{"enabled": false}, "codex_ticket_runtime": map[string]any{"forged": true}}
	require.NoError(t, repo.Update(ctx, old))
	fresh, e = repo.GetByID(ctx, old.ID)
	require.NoError(t, e)
	after, e := json.Marshal(fresh.Extra["codex_ticket_config"])
	require.NoError(t, e)
	require.JSONEq(t, string(raw), string(after))
	require.Nil(t, fresh.Extra["codex_ticket_runtime"])
}

func TestCodexTicketRepositoryGlobalLimitAcrossAccountsAndPoolFence(t *testing.T) {
	ctx := context.Background()
	one := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	two := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	ids := make([]int64, 2)
	for i := range ids {
		a := &service.Account{Name: fmt.Sprintf("state-cluster-%d", i), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "fixture"}}
		require.NoError(t, one.Create(ctx, a))
		ids[i] = a.ID
		t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	}
	var wg sync.WaitGroup
	wins := make(chan int64, 2)
	errs := make(chan error, 2)
	for i, repo := range []*accountRepository{one, two} {
		wg.Add(1)
		go func(i int, r *accountRepository) {
			defer wg.Done()
			_, err := r.MutateCodexTicket(ctx, ids[i], 1, func(a *service.Account, active int, now time.Time) (bool, error) {
				if active >= 1 {
					return false, nil
				}
				a.Extra["codex_ticket_runtime"] = map[string]any{"gpt-6-astra": map[string]any{"lease_token": "worker", "lease_until": now.Add(time.Minute), "retry_after": now.Add(20 * time.Minute)}}
				wins <- a.ID
				return true, nil
			})
			errs <- err
		}(i, repo)
	}
	wg.Wait()
	close(wins)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, wins, 1, "cluster limit applies to distinct account row locks")
	winner := <-wins
	restored, err := two.GetByID(ctx, winner)
	require.NoError(t, err)
	runtime, _ := json.Marshal(restored.Extra["codex_ticket_runtime"])
	require.Contains(t, string(runtime), "retry_after")
	settings := NewSettingRepository(testEntClient(t))
	poolKey := service.SettingKeyOpenAICodexTicketHarvestProxyURL
	enabledKey := service.SettingKeyOpenAICodexTicketEnabled
	t.Cleanup(func() { _ = settings.Delete(ctx, poolKey); _ = settings.Delete(ctx, enabledKey) })
	require.NoError(t, settings.SetMultiple(ctx, map[string]string{poolKey: "http://old.example:8080", enabledKey: "true"}))
	fence := service.CodexTicketFence{Pool: "http://old.example:8080", FallbackEnabled: true}
	fenced := service.ContextWithCodexTicketFence(ctx, fence)
	entered, release := make(chan struct{}), make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		_, e := one.MutateCodexTicket(fenced, ids[0], 0, func(a *service.Account, _ int, _ time.Time) (bool, error) {
			close(entered)
			<-release
			a.Extra["fenced_publication"] = "old"
			return true, nil
		})
		mutationDone <- e
	}()
	<-entered
	settingDone := make(chan error, 1)
	go func() { settingDone <- settings.Set(ctx, poolKey, "http://new.example:8080") }()
	select {
	case e := <-settingDone:
		close(release)
		t.Fatalf("pool edit bypassed publication lock: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-mutationDone)
	require.NoError(t, <-settingDone)
	_, err = two.MutateCodexTicket(fenced, ids[0], 0, func(*service.Account, int, time.Time) (bool, error) {
		t.Fatal("old pool worker entered publication")
		return true, nil
	})
	require.ErrorIs(t, err, service.ErrCodexTicketConflict)
	fence.Pool = "http://new.example:8080"
	_, err = two.MutateCodexTicket(service.ContextWithCodexTicketFence(ctx, fence), ids[0], 0, func(a *service.Account, _ int, _ time.Time) (bool, error) {
		a.Extra["fenced_publication"] = "new"
		return true, nil
	})
	require.NoError(t, err)
}
