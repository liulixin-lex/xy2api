package migrations

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestAnthropicCompatProfileMigration(t *testing.T) {
	migration, err := FS.ReadFile("296_anthropic_compat_profile_final.sql")
	require.NoError(t, err)
	dsn := os.Getenv("XY2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("XY2API_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TEMP TABLE accounts (id bigint PRIMARY KEY, platform text, type text, extra jsonb);
        INSERT INTO accounts VALUES
          (1,'anthropic','apikey','{"anthropic_compat_profile":"claude_code_v1","anthropic_compat_identity_seed":"keep","anthropic_compat_new_sessions":false,"nested":{"keep":true}}'),
          (2,'anthropic','apikey','{"anthropic_compat_profile":"claude_code_v2","keep":2}'),
          (3,'anthropic','apikey','{"anthropic_compat_profile":"claude_code","keep":3}'),
          (4,'anthropic','apikey','{"anthropic_compat_profile":"off","keep":4}'),
          (5,'anthropic','oauth','{"anthropic_compat_profile":"claude_code_v1"}'),
          (6,'openai','apikey','{"anthropic_compat_profile":"claude_code_v2"}'),
          (7,'anthropic','apikey','{"keep":7}'),
          (8,'anthropic','apikey',NULL);
        CREATE TEMP TABLE original_accounts AS SELECT * FROM accounts;`)
	require.NoError(t, err)
	for _, want := range []int64{2, 0} {
		result, err := tx.Exec(string(migration))
		require.NoError(t, err)
		affected, err := result.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, want, affected)
		var mismatches int
		require.NoError(t, tx.QueryRow(`SELECT count(*) FROM accounts a JOIN original_accounts o USING(id)
            WHERE a.extra IS DISTINCT FROM CASE WHEN a.id IN (1,2)
              THEN jsonb_set(o.extra,'{anthropic_compat_profile}','"claude_code"'::jsonb) ELSE o.extra END`).Scan(&mismatches))
		require.Zero(t, mismatches, "only the two legacy Anthropic API-key profile fields may change")
	}
}
