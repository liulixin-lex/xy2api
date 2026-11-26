//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	dbent "github.com/liulixin-lex/xy2api/ent"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGeminiCooldownTransactionLeaseAndRollback(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, client, nil)
	account := mustCreateAccount(t, client, &service.Account{Name: "gemini-recovery-" + uuid.NewString(), Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"base_url": "https://relay.example.com", "api_key": "fixture-key"}})
	t.Cleanup(func() { _, _ = client.Account.Delete().Where(dbaccount.IDEQ(account.ID)).Exec(ctx) })
	outboxCount := func() int {
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1", account.ID).Scan(&count))
		return count
	}
	before := outboxCount()
	// Competing processes serialize the same recovery lease under the account lock.
	var winners atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- repo.MutateGeminiCooldown(ctx, account.ID, func(a *service.Account, now time.Time) (bool, error) {
				if a.Extra[service.GeminiCooldownExtraKey] != nil {
					return false, nil
				}
				a.Extra[service.GeminiCooldownExtraKey] = map[string]any{"fixture": map[string]any{"lease": "owner", "lease_until": now.Add(time.Minute).Format(time.RFC3339Nano)}}
				winners.Add(1)
				return true, nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), winners.Load())
	require.Equal(t, before+1, outboxCount())
	expected := errors.New("rollback")
	err := repo.MutateGeminiCooldown(ctx, account.ID, func(a *service.Account, _ time.Time) (bool, error) {
		a.Extra["must_rollback"] = true
		return true, expected
	})
	require.ErrorIs(t, err, expected)
	fresh, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, fresh.Extra, "must_rollback")
	require.NotEmpty(t, fresh.Extra[service.GeminiCooldownExtraKey])
	require.Equal(t, before+1, outboxCount())

	// Account edits use the row's latest runtime state, including a prior clear.
	stale, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.MutateGeminiCooldown(ctx, account.ID, func(a *service.Account, _ time.Time) (bool, error) {
		delete(a.Extra, service.GeminiCooldownExtraKey)
		return true, nil
	}))
	stale.Name += "-edited"
	require.NoError(t, repo.Update(ctx, stale))
	fresh, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, fresh.Extra, service.GeminiCooldownExtraKey)

	// A caller-owned transaction must retain both state and outbox until commit.
	before = outboxCount()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.MutateGeminiCooldown(dbent.NewTxContext(ctx, tx), account.ID, func(a *service.Account, _ time.Time) (bool, error) {
		a.Extra["outer_transaction"] = true
		return true, nil
	}))
	require.NoError(t, tx.Rollback())
	fresh, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, fresh.Extra, "outer_transaction")
	require.Equal(t, before, outboxCount())

	// Failure after the state UPDATE (during outbox insert) rolls back both.
	name := fmt.Sprintf("gemini_outbox_failure_%d", account.ID)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.account_id = %d THEN RAISE EXCEPTION 'gemini injected outbox failure'; END IF; RETURN NEW; END $$`, name, account.ID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON scheduler_outbox", name))
		_, _ = integrationDB.ExecContext(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", name))
	})
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON scheduler_outbox FOR EACH ROW EXECUTE FUNCTION %s()", name, name))
	require.NoError(t, err)
	err = repo.MutateGeminiCooldown(ctx, account.ID, func(a *service.Account, _ time.Time) (bool, error) {
		a.Extra["outbox_failure"] = true
		return true, nil
	})
	require.ErrorContains(t, err, "gemini injected outbox failure")
	fresh, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, fresh.Extra, "outbox_failure")
	require.Equal(t, before, outboxCount())
}
