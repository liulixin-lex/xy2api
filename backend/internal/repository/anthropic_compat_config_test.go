package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAnthropicCompatBulkSeedDecisionIsLockedAndParameterized(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, credentials, extra FROM accounts[\s\S]*ORDER BY id FOR NO KEY UPDATE`).
		WithArgs(pq.Array([]int64{1, 2})).WillReturnRows(sqlmock.NewRows([]string{"id", "credentials", "extra"}).
		AddRow(1, `{"api_key":"fixture","base_url":"https://api.anthropic.com/"}`, `{"anthropic_compat_profile":"claude_code"}`).
		AddRow(2, `{"api_key":"other"}`, `{"anthropic_compat_profile":"claude_code"}`))
	// The repository also locks rows for its independent IQ reset policy.
	mock.ExpectQuery(`SELECT id, platform, [\s\S]*ORDER BY id FOR NO KEY UPDATE`).
		WithArgs([]byte(`{"api_key":" fixture "}`), pq.Array([]int64{1, 2})).
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "changed"}))
	mock.ExpectExec(`UPDATE accounts SET [\s\S]*NOT \(id = ANY\(\$3::bigint\[\]\)\)[\s\S]*WHERE id = ANY\(\$4\)`).
		WithArgs([]byte(`{"api_key":" fixture "}`), []byte(`{"anthropic_compat_profile":"claude_code"}`), pq.Array([]int64{2}), pq.Array([]int64{1, 2})).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newAccountRepositoryWithSQL(client, db, nil)
	count, err := repo.BulkUpdate(context.Background(), []int64{1, 2}, service.AccountBulkUpdate{Credentials: map[string]any{"api_key": " fixture "}, Extra: map[string]any{service.AnthropicCompatProfileExtraKey: "claude_code"}, EnsureAnthropicCompatSeed: true})
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAnthropicCompatExtraSeedDecisionIsLockedAndParameterized(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, credentials, extra FROM accounts[\s\S]*ORDER BY id FOR NO KEY UPDATE`).
		WithArgs(pq.Array([]int64{1})).WillReturnRows(sqlmock.NewRows([]string{"id", "credentials", "extra"}).AddRow(1, `{"api_key":"fixture"}`, `{"anthropic_compat_profile":"claude_code"}`))
	mock.ExpectExec(`UPDATE accounts SET extra = [\s\S]*NOT \(id = ANY\(\$3::bigint\[\]\)\)`).
		WithArgs(`{"anthropic_apikey_auth_scheme":"x_api_key","anthropic_compat_profile":"claude_code"}`, int64(1), pq.Array([]int64{})).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := newAccountRepositoryWithSQL(client, db, nil)
	require.NoError(t, repo.UpdateExtra(context.Background(), 1, map[string]any{service.AnthropicCompatProfileExtraKey: "claude_code", "anthropic_apikey_auth_scheme": service.AnthropicAPIKeyAuthSchemeXAPIKey}))
	require.NoError(t, mock.ExpectationsWereMet())
}

// Set XY2API_TEST_POSTGRES_DSN to a disposable local database to execute the
// actual expression and Go identity decision without the application schema.
func TestAnthropicCompatSeedSQLPostgres(t *testing.T) {
	dsn := os.Getenv("XY2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("XY2API_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE accounts (id bigint PRIMARY KEY, platform text, type text, credentials jsonb, extra jsonb, deleted_at timestamptz)`)
	require.NoError(t, err)
	seed := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name               string
		credentials, extra map[string]any
		changed            bool
	}{
		{name: "key_space", credentials: map[string]any{"api_key": " \tfixture\n"}},
		{name: "url_space_slashes", credentials: map[string]any{"base_url": " https://api.anthropic.com/// \n"}},
		{name: "empty_url", credentials: map[string]any{"base_url": " /// "}},
		{name: "auth_default", extra: map[string]any{"anthropic_apikey_auth_scheme": "x_api_key"}},
		{name: "auth_empty", extra: map[string]any{"anthropic_apikey_auth_scheme": ""}},
		{name: "auth_ignored", credentials: map[string]any{"auth_scheme": "unused"}},
		{name: "key_change", credentials: map[string]any{"api_key": "changed"}, changed: true},
		{name: "url_change", credentials: map[string]any{"base_url": "https://relay.example/v1"}, changed: true},
		{name: "auth_change", extra: map[string]any{"anthropic_apikey_auth_scheme": "authorization_bearer"}, changed: true},
		{name: "auth_invalid_is_x_api_key", extra: map[string]any{"anthropic_apikey_auth_scheme": "bearer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err = tx.ExecContext(ctx, `INSERT INTO accounts VALUES (1,'anthropic','apikey','{"api_key":"fixture"}',jsonb_build_object('anthropic_compat_profile','claude_code','anthropic_compat_identity_seed',$1::text),NULL) ON CONFLICT (id) DO UPDATE SET credentials=excluded.credentials,extra=excluded.extra`, seed)
			require.NoError(t, err)
			ids, err := anthropicCompatRotationIDs(ctx, tx, []int64{1}, tc.credentials, tc.extra)
			require.NoError(t, err)
			require.Equal(t, tc.changed, len(ids) > 0)
			credentials, extra := tc.credentials, tc.extra
			if credentials == nil {
				credentials = map[string]any{}
			}
			if extra == nil {
				extra = map[string]any{}
			}
			rawCred, _ := json.Marshal(credentials)
			rawExtra, _ := json.Marshal(extra)
			expr := ensureAnthropicCompatSeedSQL("extra || $2::jsonb", "$3")
			var gotSeed, profile string
			err = tx.QueryRowContext(ctx, `UPDATE accounts SET credentials=credentials || $1::jsonb,extra=`+expr+` WHERE id=1 RETURNING extra->>'anthropic_compat_identity_seed',extra->>'anthropic_compat_profile'`, rawCred, rawExtra, pq.Array(ids)).Scan(&gotSeed, &profile)
			require.NoError(t, err)
			require.Equal(t, tc.changed, gotSeed != seed)
			require.Len(t, gotSeed, 64)
			require.Equal(t, "claude_code", profile)
		})
	}
	t.Run("initial_seeds_are_per_account", func(t *testing.T) {
		_, err := tx.ExecContext(ctx, `TRUNCATE accounts; INSERT INTO accounts VALUES (1,'anthropic','apikey','{"api_key":"fixture"}','{"anthropic_compat_profile":"claude_code"}',NULL),(2,'anthropic','apikey','{"api_key":"fixture"}','{"anthropic_compat_profile":"claude_code"}',NULL)`)
		require.NoError(t, err)
		rows, err := tx.QueryContext(ctx, `UPDATE accounts SET extra=`+ensureAnthropicCompatSeedSQL("extra", "$1")+` RETURNING extra->>'anthropic_compat_identity_seed',extra->>'anthropic_compat_profile'`, pq.Array([]int64{}))
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		seeds := map[string]bool{}
		for rows.Next() {
			var seed, profile string
			require.NoError(t, rows.Scan(&seed, &profile))
			require.Len(t, seed, 64)
			require.Equal(t, "claude_code", profile)
			seeds[seed] = true
		}
		require.NoError(t, rows.Err())
		require.Len(t, seeds, 2)
	})

}
