//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
	"strings"
)

func TestOpsStatusOriginMigration(t *testing.T) {
	db := integrationDB
	schema := "errclass_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := db.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE") })
	table := schema + ".ops_error_logs"
	_, err = db.Exec("CREATE TABLE " + table + " (id BIGSERIAL PRIMARY KEY, upstream_status_code INTEGER, status_code INTEGER); INSERT INTO " + table + "(upstream_status_code,status_code) VALUES (502,503)")
	require.NoError(t, err)
	begin := func() *sql.Tx {
		t.Helper()
		tx, err := db.Begin()
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback() })
		_, err = tx.Exec(fmt.Sprintf("SET LOCAL search_path TO %s", schema))
		require.NoError(t, err)
		return tx
	}
	body, err := migrations.FS.ReadFile("316_ops_status_origin.sql")
	require.NoError(t, err)
	holder := begin()
	_, err = holder.Exec("LOCK TABLE ops_error_logs IN ACCESS SHARE MODE")
	require.NoError(t, err)
	blocked := begin()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	waitStarted := time.Now()
	_, err = blocked.ExecContext(ctx, string(body))
	require.GreaterOrEqual(t, time.Since(waitStarted), 4*time.Second)
	var pgerr *pq.Error
	require.ErrorAs(t, err, &pgerr)
	require.Equal(t, pq.ErrorCode("55P03"), pgerr.Code, "lock_timeout must bound the hot-table wait")
	require.NoError(t, blocked.Rollback())
	_, err = db.ExecContext(ctx, "UPDATE "+table+" SET status_code=status_code")
	require.NoError(t, err, "failed migration must not queue behind the reader and block writers")
	require.NoError(t, holder.Rollback())
	for range 2 {
		tx := begin()
		_, err = tx.Exec(string(body))
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}
	var origin, client sql.NullInt64
	require.NoError(t, db.QueryRow("SELECT status_origin,client_status FROM "+table+" WHERE id=1").Scan(&origin, &client))
	require.False(t, origin.Valid)
	require.False(t, client.Valid)
	_, err = db.Exec("INSERT INTO " + table + "(upstream_status_code,status_code,status_origin,client_status) VALUES (502,503,1,200), (504,504,2,200)")
	require.NoError(t, err)
	var upstream, internal, wire, source int
	require.NoError(t, db.QueryRow("SELECT upstream_status_code,status_code,client_status,status_origin FROM "+table+" WHERE id=2").Scan(&upstream, &internal, &wire, &source))
	require.Equal(t, []int{502, 503, 200, 1}, []int{upstream, internal, wire, source})
}
