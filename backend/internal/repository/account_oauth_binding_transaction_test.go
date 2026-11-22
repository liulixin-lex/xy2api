//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/migrate"
	"github.com/liulixin-lex/xy2api/internal/pkg/adminaccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type oauthFlagAfterRead struct {
	service.AdminGroupRepository
	db    *sql.DB
	fired bool
}

type oauthDetachAfterRead struct {
	service.AdminAccountRepository
	db    *sql.DB
	fired bool
}

func (r *oauthDetachAfterRead) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	a, err := r.AdminAccountRepository.GetByID(ctx, id)
	if err == nil && !r.fired {
		r.fired = true
		_, err = r.db.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, id)
	}
	return a, err
}

func (r *oauthDetachAfterRead) GetByIDs(ctx context.Context, ids []int64) ([]*service.Account, error) {
	accounts, err := r.AdminAccountRepository.GetByIDs(ctx, ids)
	if err == nil && !r.fired && len(accounts) > 0 {
		r.fired = true
		_, err = r.db.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, accounts[0].ID)
	}
	return accounts, err
}

// Deterministically commit another administrator's flag change between the
// service's admission read and its account/binding persistence.
func (r *oauthFlagAfterRead) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	g, err := r.AdminGroupRepository.GetByIDLite(ctx, id)
	if err == nil && !r.fired {
		r.fired = true
		_, err = r.db.ExecContext(ctx, `UPDATE groups SET require_oauth_only=TRUE WHERE id=$1`, id)
	}
	return g, err
}

func TestOAuthBindingTransaction(t *testing.T) {
	for _, bin := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("local PostgreSQL executables unavailable")
		}
	}
	dir, err := os.MkdirTemp("", "review-admin-pg-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data := filepath.Join(dir, "data")
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("initdb", "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", fmt.Sprintf("-h '' -k %s -p %d", dir, port), "-w", "start")
	t.Cleanup(func() { run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop") })
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d dbname=postgres sslmode=disable", dir, port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	ctx := context.Background()
	require.NoError(t, migrate.Create(ctx, migrate.NewSchema(driver), []*schema.Table{migrate.ProxiesTable, migrate.AccountsTable, migrate.GroupsTable, migrate.AccountGroupsTable}, migrate.WithForeignKeys(false)))
	_, err = db.ExecContext(ctx, `CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT,account_id BIGINT,group_id BIGINT,payload JSONB,dedup_key TEXT,created_at TIMESTAMPTZ DEFAULT NOW())`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE UNIQUE INDEX scheduler_outbox_dedup_key ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE scheduled_test_plans(account_id BIGINT)`)
	require.NoError(t, err)
	groups := newGroupRepositoryWithSQL(client, db)
	accounts := newAccountRepositoryWithSQL(client, db, nil)
	for _, entry := range []string{"basic", "admin", "duplicate"} {
		t.Run("flag-committed/"+entry, func(t *testing.T) {
			platform := service.PlatformOpenAI
			if entry == "duplicate" {
				platform = service.PlatformAnthropic
			}
			group, err := client.Group.Create().SetName("flag-target-" + entry).SetPlatform(platform).Save(ctx)
			require.NoError(t, err)
			var sourceID int64
			if entry == "duplicate" {
				source, err := client.Account.Create().SetName("source").SetPlatform(platform).SetType(service.AccountTypeAPIKey).SetCredentials(map[string]any{"api_key": "synthetic"}).Save(ctx)
				require.NoError(t, err)
				sourceID = source.ID
				_, err = client.AccountGroup.Create().SetAccountID(source.ID).SetGroupID(group.ID).Save(ctx)
				require.NoError(t, err)
			}
			wrapper := &oauthFlagAfterRead{AdminGroupRepository: groups, db: db}
			before, err := client.Account.Query().Count(ctx)
			require.NoError(t, err)
			var created *service.Account
			switch entry {
			case "basic":
				created, err = service.NewAccountService(accounts, wrapper).Create(ctx, service.CreateAccountRequest{Name: "synthetic", Platform: platform, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic"}, GroupIDs: []int64{group.ID}, Concurrency: 1})
			case "admin":
				created, err = oauthTransactionAdmin(accounts, wrapper).CreateAccount(ctx, &service.CreateAccountInput{Name: "synthetic", Platform: platform, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic"}, GroupIDs: []int64{group.ID}, Concurrency: 1, SkipMixedChannelCheck: true})
			case "duplicate":
				created, err = oauthTransactionAdmin(accounts, wrapper).DuplicateAccount(ctx, sourceID, "", "")
			}
			require.ErrorContains(t, err, "OAuth-only")
			require.Nil(t, created)
			require.True(t, wrapper.fired)
			after, err := client.Account.Query().Count(ctx)
			require.NoError(t, err)
			require.Equal(t, before, after, "rejection must not leave a half-created account")
			actual, err := groups.GetByID(ctx, group.ID)
			require.NoError(t, err)
			require.True(t, actual.RequireOAuthOnly)
			expected := int64(0)
			if entry == "duplicate" {
				expected = 1
			}
			require.Equal(t, expected, actual.AccountCount)
			require.Equal(t, expected, actual.OAuthOnlyMismatchCount, "only the original duplicate source may remain")
			t.Log("rejected after committed flag change; no new member or partial account")
		})
	}
	for _, entry := range []string{"basic", "admin", "bulk", "filtered-bulk"} {
		t.Run("detached-legacy/"+entry, func(t *testing.T) {
			group, account := oauthTransactionMember(t, client, "detached-"+entry, service.AccountTypeAPIKey, true)
			wrapper := &oauthDetachAfterRead{AdminAccountRepository: accounts, db: db}
			ids := []int64{group.ID}
			admin := oauthTransactionAdmin(wrapper, groups)
			var err error
			switch entry {
			case "basic":
				_, err = service.NewAccountService(wrapper, groups).Update(ctx, account.ID, service.UpdateAccountRequest{GroupIDs: &ids})
			case "admin":
				_, err = admin.UpdateAccount(ctx, account.ID, &service.UpdateAccountInput{GroupIDs: &ids, SkipMixedChannelCheck: true})
			default:
				input := &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, GroupIDs: &ids, SkipMixedChannelCheck: true}
				if entry == "filtered-bulk" {
					input.AccountIDs = nil
					input.Filters = &service.BulkUpdateAccountFilters{Search: account.Name}
				}
				result, bulkErr := admin.BulkUpdateAccounts(ctx, input)
				require.NoError(t, bulkErr)
				require.Equal(t, 1, result.Failed)
				require.Zero(t, result.Success)
				require.Contains(t, result.Results[0].Error, "OAuth-only")
			}
			if entry == "basic" || entry == "admin" {
				require.ErrorContains(t, err, "OAuth-only")
			}
			require.True(t, wrapper.fired)
			actual, err := groups.GetByID(ctx, group.ID)
			require.NoError(t, err)
			require.True(t, actual.RequireOAuthOnly)
			require.Zero(t, actual.AccountCount)
			require.Zero(t, actual.OAuthOnlyMismatchCount)
			t.Log("detached legacy member rejected; binding not recreated")
		})
	}
	t.Run("existing-and-disabled", func(t *testing.T) {
		group, account := oauthTransactionMember(t, client, "retained", service.AccountTypeAPIKey, true)
		require.NoError(t, accounts.BindGroups(ctx, account.ID, []int64{group.ID}))
		name := "renamed-retained"
		_, err := service.NewAccountService(accounts, groups).Update(ctx, account.ID, service.UpdateAccountRequest{Name: &name})
		require.NoError(t, err)
		_, err = oauthTransactionAdmin(accounts, groups).UpdateAccount(ctx, account.ID, &service.UpdateAccountInput{Name: name})
		require.NoError(t, err)
		disabled, err := client.Group.Create().SetName("disabled-flag").SetPlatform(service.PlatformOpenAI).Save(ctx)
		require.NoError(t, err)
		require.NoError(t, accounts.BindGroups(ctx, account.ID, []int64{group.ID, disabled.ID}))
		for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeSetupToken} {
			other, err := client.Account.Create().SetName("allowed").SetPlatform(service.PlatformOpenAI).SetType(kind).SetCredentials(map[string]any{}).Save(ctx)
			require.NoError(t, err)
			require.NoError(t, accounts.BindGroups(ctx, other.ID, []int64{group.ID}))
		}
	})
	t.Run("binding-rejection-rolls-back", func(t *testing.T) {
		old, account := oauthTransactionMember(t, client, "old", service.AccountTypeAPIKey, false)
		denied, err := client.Group.Create().SetName("denied").SetPlatform(service.PlatformOpenAI).SetRequireOauthOnly(true).Save(ctx)
		require.NoError(t, err)
		before, err := accounts.GetByID(ctx, account.ID)
		require.NoError(t, err)
		for _, scoped := range []bool{false, true} {
			bindCtx := ctx
			var tx *dbent.Tx
			if scoped {
				tx, err = client.Tx(ctx)
				require.NoError(t, err)
				bindCtx = adminaccess.WithPrincipal(dbent.NewTxContext(ctx, tx), &adminaccess.Principal{})
			}
			err = accounts.BindGroups(bindCtx, account.ID, []int64{denied.ID})
			require.ErrorContains(t, err, "OAuth-only")
			if tx != nil {
				require.NoError(t, tx.Rollback())
			}
			actual, err := accounts.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, []int64{old.ID}, actual.GroupIDs)
			require.True(t, before.UpdatedAt.Equal(actual.UpdatedAt), "revision bump is rolled back with a rejected binding")
		}
	})
	t.Run("write-failure-rolls-back-creation", func(t *testing.T) {
		target, err := client.Group.Create().SetName("failing-target").SetPlatform(service.PlatformOpenAI).Save(ctx)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `CREATE FUNCTION reject_oauth_test_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic binding write failure'; END $$; CREATE TRIGGER oauth_test_reject BEFORE INSERT ON account_groups FOR EACH ROW EXECUTE FUNCTION reject_oauth_test_binding()`)
		require.NoError(t, err)
		defer func() {
			_, err := db.ExecContext(ctx, `DROP TRIGGER oauth_test_reject ON account_groups; DROP FUNCTION reject_oauth_test_binding()`)
			require.NoError(t, err)
		}()
		for _, copyAccount := range []bool{false, true} {
			before, err := client.Account.Query().Count(ctx)
			require.NoError(t, err)
			account := &service.Account{Name: "rolled-back", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, GroupIDs: []int64{target.ID}}
			if copyAccount {
				err = accounts.CreateWithAccountGroups(ctx, account, []service.AccountGroup{{GroupID: target.ID}})
			} else {
				err = accounts.Create(ctx, account)
			}
			require.ErrorContains(t, err, "synthetic binding write failure")
			after, err := client.Account.Query().Count(ctx)
			require.NoError(t, err)
			require.Equal(t, before, after)
			var events int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1`, account.ID).Scan(&events))
			require.Zero(t, events)
		}
	})
	t.Run("copy-filters-current-flag-and-type", func(t *testing.T) {
		source, account := oauthTransactionMember(t, client, "copy-source", service.AccountTypeOAuth, false)
		target, err := client.Group.Create().SetName("copy-target").SetPlatform(service.PlatformOpenAI).SetRequireOauthOnly(true).Save(ctx)
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET type='apikey' WHERE id=$1`, account.ID)
		require.NoError(t, err)
		result := make(chan error, 1)
		go func() {
			count, err := groups.CopyAccountMembers(ctx, target.ID, []int64{source.ID}, false, false)
			if err == nil && count != 0 {
				err = fmt.Errorf("copied %d ineligible members", count)
			}
			result <- err
		}()
		ratefloorWaitForLock(t, ctx, db, "ratefloor_copy_account_locks")
		require.NoError(t, tx.Commit())
		require.NoError(t, <-result)
		actual, err := groups.GetByID(ctx, target.ID)
		require.NoError(t, err)
		require.Zero(t, actual.AccountCount)
	})
	t.Run("binding-waits-for-current-flag", func(t *testing.T) {
		group, account := oauthTransactionMember(t, client, "wait-flag", service.AccountTypeAPIKey, false)
		require.NoError(t, accounts.BindGroups(ctx, account.ID, nil))
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `UPDATE groups SET require_oauth_only=TRUE WHERE id=$1`, group.ID)
		require.NoError(t, err)
		result := make(chan error, 1)
		go func() { result <- accounts.BindGroups(ctx, account.ID, []int64{group.ID}) }()
		ratefloorWaitForLock(t, ctx, db, "account_group_live_group_lock")
		require.NoError(t, tx.Commit())
		require.ErrorContains(t, <-result, "OAuth-only")
		actual, err := groups.GetByID(ctx, group.ID)
		require.NoError(t, err)
		require.Zero(t, actual.AccountCount)
	})
}

func oauthTransactionAdmin(accounts service.AdminAccountRepository, groups service.AdminGroupRepository) service.AdminService {
	return service.NewAdminService(nil, nil, groups, accounts, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func oauthTransactionMember(t *testing.T, client *dbent.Client, name, kind string, enabled bool) (*dbent.Group, *dbent.Account) {
	t.Helper()
	ctx := context.Background()
	group, err := client.Group.Create().SetName(name).SetPlatform(service.PlatformOpenAI).SetRequireOauthOnly(enabled).Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().SetName(name).SetPlatform(service.PlatformOpenAI).SetType(kind).SetStatus(service.StatusActive).SetCredentials(map[string]any{"api_key": "synthetic"}).Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(group.ID).Save(ctx)
	require.NoError(t, err)
	return group, account
}
