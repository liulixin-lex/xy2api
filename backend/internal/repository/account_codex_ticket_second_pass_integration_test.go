//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketRepositorySecondPassScale(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{1000, 10000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var queries, counts, locks atomic.Int64
			driver := dialect.Debug(entsql.OpenDB(dialect.Postgres, integrationDB), func(args ...any) {
				line := fmt.Sprint(args...)
				if strings.Contains(line, "query=") {
					queries.Add(1)
				}
				if strings.Contains(line, "SELECT count(*) FROM accounts a CROSS JOIN") {
					counts.Add(1)
				}
				if strings.Contains(line, "FOR UPDATE") {
					locks.Add(1)
				}
			})
			client := dbent.NewClient(dbent.Driver(driver))
			repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
			prefix := fmt.Sprintf("state-scale-%d-", n)
			extra := `{"codex_ticket_config":{"enabled":true,"models":["gpt-6-astra","gpt-5.6-sol"],"revision":"fixture"},"codex_turn_ticket:gpt-6-astra":{"state":"private-fixture","verified":true},"codex_turn_ticket:gpt-5.6-sol":{"state":"private-fixture","verified":true}}`
			rows, err := integrationDB.Query(`INSERT INTO accounts(name,platform,type,status,schedulable,credentials,extra) SELECT $1||i::text,'openai','oauth','active',true,'{"access_token":"secret-fixture"}'::jsonb,$2::jsonb FROM generate_series(1,$3) i RETURNING id`, prefix, extra, n)
			require.NoError(t, err)
			var ids []int64
			for rows.Next() {
				var id int64
				require.NoError(t, rows.Scan(&id))
				ids = append(ids, id)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			require.Len(t, ids, n)
			t.Cleanup(func() {
				_, e := integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=ANY($1)`, pq.Array(ids))
				require.NoError(t, e)
				_, e = integrationDB.Exec(`DELETE FROM accounts WHERE id=ANY($1)`, pq.Array(ids))
				require.NoError(t, e)
			})
			queries.Store(0)
			counts.Store(0)
			locks.Store(0)
			started := time.Now()
			for _, id := range ids {
				_, err = repo.MutateCodexTicket(ctx, id, 2, func(*service.Account, int, time.Time) (bool, error) { return false, nil })
				require.NoError(t, err)
			}
			baselineTime := time.Since(started)
			t.Logf("BASELINE admission pattern accounts=%d models=2 queries=%d lease_counts=%d row_locks=%d elapsed=%s", n, queries.Load(), counts.Load(), locks.Load(), baselineTime)
			require.EqualValues(t, n, counts.Load())
			queries.Store(0)
			counts.Store(0)
			locks.Store(0)
			started = time.Now()
			after := int64(0)
			seen, pages := 0, 0
			for {
				page, e := repo.ListCodexTicketScanPage(ctx, after, 100)
				require.NoError(t, e)
				pages++
				if len(page) == 0 {
					break
				}
				for _, a := range page {
					if strings.HasPrefix(a.Name, prefix) {
						t.Fatal("scan needlessly hydrated account name")
					}
					b, e := json.Marshal(a.Extra)
					require.NoError(t, e)
					require.NotContains(t, string(b), "private-fixture")
					require.NotContains(t, a.Credentials, "access_token")
				}
				seen += len(page)
				after = page[len(page)-1].ID
			}
			require.Equal(t, n, seen)
			require.Equal(t, n/100+1, pages)
			require.Zero(t, counts.Load())
			require.Zero(t, locks.Load())
			t.Logf("MODIFIED projection accounts=%d models=2 queries=%d pages=%d lease_counts=%d row_locks=%d elapsed=%s", n, queries.Load(), pages, counts.Load(), locks.Load(), time.Since(started))
		})
	}
}

func TestCodexTicketRepositorySecondPassBatchClaim(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	var ids []int64
	for i := 0; i < 4; i++ {
		a := &service.Account{Name: fmt.Sprintf("state-batch-%d", i), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true}
		require.NoError(t, repo.Create(ctx, a))
		ids = append(ids, a.ID)
		t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	}
	var wg sync.WaitGroup
	var won atomic.Int64
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := repo.MutateCodexTicketBatch(ctx, ids, 2, func(a *service.Account, active int, now time.Time) (bool, error) {
				if active >= 2 {
					return false, nil
				}
				a.Extra["codex_ticket_runtime"] = map[string]any{"gpt-6-astra": map[string]any{"lease_until": now.Add(time.Minute)}}
				won.Add(1)
				return true, nil
			})
			errors <- e
		}()
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		require.NoError(t, e)
	}
	require.EqualValues(t, 2, won.Load())
}

func TestCodexTicketRepositorySecondPassProxyIQFence(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	proxyRepo := newProxyRepositoryWithSQL(client, integrationDB)
	p := &service.Proxy{Name: "state-iq-proxy", Protocol: "http", Host: "before.example.invalid", Port: 8080, Status: service.StatusActive}
	require.NoError(t, proxyRepo.Create(ctx, p))
	t.Cleanup(func() { _, e := integrationDB.Exec(`DELETE FROM proxies WHERE id=$1`, p.ID); require.NoError(t, e) })
	a := &service.Account{Name: "state-proxy-iq", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, ProxyID: &p.ID, Credentials: map[string]any{"chatgpt_account_id": "fixture"}, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().Add(time.Second)
	claims, e := repo.ClaimIQChecks(ctx, now, 10)
	require.NoError(t, e)
	require.Len(t, claims, 1)
	started, e := repo.StartIQCheck(ctx, claims[0], now)
	require.NoError(t, e)
	require.True(t, started)
	before, e := repo.GetByID(ctx, a.ID)
	require.NoError(t, e)
	result := iqcheck.Grade("21")
	result.StateModel = "gpt-6-astra"
	// Build the probe's persisted identity fixture, then prove it is current before editing.
	identity := map[string]any{"account_id": a.ID, "type": a.Type, "proxy_id": p.ID, "proxy": p.URL(), "proxy_status": p.Status, "identity_namespace": "chatgpt:fixture"}
	for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id", "email", "client_id", "oauth_type", "openai_auth_type", "agent_identity", "header_override_enabled", "header_overrides", "user_agent", "device_id", "base_url"} {
		identity[key] = before.Credentials[key]
	}
	for _, key := range []string{"codex_fingerprint_mode", "codex_fingerprint_seed", "enable_tls_fingerprint", "tls_fingerprint_profile_id", "openai_responses_mode", "openai_passthrough"} {
		identity[key] = before.Extra[key]
	}
	encoded, e := json.Marshal(identity)
	require.NoError(t, e)
	hash := sha256.Sum256(encoded)
	result.StateFingerprint = hex.EncodeToString(hash[:])
	encoded, e = json.Marshal(before.Extra["codex_ticket_config"])
	require.NoError(t, e)
	var cfg struct {
		Revision string `json:"revision"`
	}
	require.NoError(t, json.Unmarshal(encoded, &cfg))
	result.StateRevision = cfg.Revision
	require.True(t, service.CodexTicketIQResultCurrent(before, result, now))
	tx, e := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, e)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, e = tx.ExecContext(ctx, `UPDATE proxies SET host='after.example.invalid' WHERE id=$1`, p.ID)
	require.NoError(t, e)
	completed := make(chan error, 1)
	waitStarted := time.Now()
	go func() { completed <- repo.CompleteIQCheck(ctx, claims[0], result, now.Add(time.Second)) }()
	require.Eventually(t, func() bool {
		var waiting int
		e := integrationDB.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE%'`).Scan(&waiting)
		return e == nil && waiting > 0
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-completed)
	t.Logf("IQ completion observed proxy lock wait and cancelled after release; elapsed=%s", time.Since(waitStarted))
	p.Host = "after-second-edit.example.invalid"
	require.NoError(t, proxyRepo.Update(ctx, p))
	after, e := repo.GetByID(ctx, a.ID)
	require.NoError(t, e)
	require.Equal(t, before.IQCheck.Status, after.IQCheck.Status)
	require.Empty(t, after.IQCheck.LeaseToken)
	records, e := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, e)
	require.Equal(t, "cancelled_by_account_change", records[0].Reason)
	var events int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM scheduler_outbox WHERE payload->'account_ids' @> jsonb_build_array($1::bigint)`, a.ID).Scan(&events))
	require.Positive(t, events)
}

func TestCodexTicketRepositorySecondPassMigrationScope(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	_, err := integrationDB.Exec(`DELETE FROM settings WHERE key IN ('codex_ticket_account_migration_v1','codex_ticket_envelope_migration_v2')`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, e := integrationDB.Exec(`DELETE FROM settings WHERE key IN ('codex_ticket_account_migration_v1','codex_ticket_envelope_migration_v2')`)
		require.NoError(t, e)
	})
	_, err = integrationDB.Exec(`INSERT INTO settings(key,value) VALUES ('codex_ticket_account_migration_v1','1')`)
	require.NoError(t, err)
	a := &service.Account{Name: "second-pass-scope", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	_, err = integrationDB.Exec(`UPDATE accounts SET extra=extra-'codex_ticket_config' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	cfg := config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, TTLSeconds: 3600}
	require.NoError(t, repo.MigrateCodexTicketAccounts(ctx, cfg))
	require.NoError(t, repo.MigrateCodexTicketAccounts(ctx, cfg))
	live, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, service.OpenAICodexTicketStatuses(live, cfg, time.Now()), "v2 must never rerun v1 opt-in")
}
