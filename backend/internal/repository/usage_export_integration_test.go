package repository

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/liulixin-lex/xy2api/internal/service"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"github.com/stretchr/testify/require"
)

func TestUsageExportSnapshotSQL(t *testing.T) {
	db, conn, schema := exportQueryFixture(t)
	_ = db
	_ = schema
	ctx := context.Background()
	for _, sort := range []string{"created_at", "model", "id"} {
		t.Run(sort, func(t *testing.T) {
			tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			o := usageexport.Options{Format: "csv", Timezone: "UTC", Language: "en", SortBy: sort, SortOrder: "asc", Version: 1}
			o.Filters.UserID = 1
			var result [][]string
			err = usageExportSnapshot(ctx, tx, o, func(row []string) error { result = append(result, row); return nil })
			require.NoError(t, err)
			require.Len(t, result, 2)
			require.Equal(t, "client", result[0][2])
			require.Equal(t, "0.12345678", result[0][13])

			if sort == "id" {
				_, err = db.Exec(`UPDATE ` + schema + `.api_keys SET name='changed';DELETE FROM ` + schema + `.usage_logs WHERE id=2;INSERT INTO ` + schema + `.usage_logs(id,created_at,user_id,api_key_id) VALUES(3,now(),1,1)`)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, `CLOSE usage_export_cursor`)
				require.NoError(t, err)
				var again [][]string
				require.NoError(t, usageExportSnapshot(ctx, tx, o, func(row []string) error { again = append(again, row); return nil }))
				require.Equal(t, result, again)
			}
			require.NoError(t, tx.Commit())
		})
	}
}

func TestUsageExportS3(t *testing.T) {
	endpoint := os.Getenv("USAGE_EXPORT_TEST_S3")
	if endpoint == "" {
		t.Skip("isolated S3 service required")
	}
	ctx := context.Background()
	cfg := &service.BackupS3Config{Endpoint: endpoint, Region: "us-east-1", Bucket: "usage-export-test", AccessKeyID: "exporttest", SecretAccessKey: "exporttestpassword", ForcePathStyle: true}
	base, err := NewS3BackupStoreFactory()(ctx, cfg)
	require.NoError(t, err)
	s3store, ok := base.(*S3BackupStore)
	require.True(t, ok)
	_, err = s3store.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &cfg.Bucket})
	if err != nil {
		require.NoError(t, base.HeadBucket(ctx))
	}
	defer func() { _, _ = s3store.client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &cfg.Bucket}) }()
	store := exportS3Store{base, "exports"}
	file := filepath.Join(t.TempDir(), "fixture.csv")
	require.NoError(t, os.WriteFile(file, []byte("\xef\xbb\xbftime,model\r\nnow,test\r\n"), 0600))
	require.NoError(t, store.Put(ctx, "task/1.csv", file))
	body, err := store.Open(ctx, "task/1.csv")
	require.NoError(t, err)
	b, err := io.ReadAll(body)
	_ = body.Close()
	require.NoError(t, err)
	expected, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, expected, b)
	require.NoError(t, store.Delete(ctx, "task/1.csv"))
	_, err = store.Open(ctx, "task/1.csv")
	require.Error(t, err)
}

func exportQueryFixture(t *testing.T) (*sql.DB, *sql.Conn, string) {
	dsn := os.Getenv("USAGE_EXPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	schema := fmt.Sprintf("export_query_%d", time.Now().UnixNano())
	_, err = db.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(ctx, `SET search_path TO `+schema)
	require.NoError(t, err)
	// Types and field names mirror the export projection; no production data.
	textFields := strings.Fields("model requested_model reasoning_effort requested_reasoning_effort inbound_endpoint ip_address billing_mode upstream_model upstream_response_model upstream_endpoint request_id upstream_request_id user_agent")
	numberFields := strings.Fields("request_type image_count input_tokens output_tokens cache_read_tokens cache_creation_tokens rate_multiplier actual_cost total_cost first_token_ms duration_ms api_key_id user_id account_id group_id input_cost output_cost cache_read_cost cache_creation_cost account_rate_multiplier account_stats_cost billing_type")
	cols := []string{"id bigint PRIMARY KEY", "created_at timestamptz NOT NULL", "stream boolean DEFAULT false", "openai_ws_mode boolean DEFAULT false", "native_compaction_v2 boolean DEFAULT false", "upstream_model_mismatch boolean"}
	for _, v := range textFields {
		cols = append(cols, v+" text")
	}
	for _, v := range numberFields {
		cols = append(cols, v+" numeric DEFAULT 0")
	}
	_, err = conn.ExecContext(ctx, `CREATE TABLE usage_logs (`+strings.Join(cols, ",")+`);CREATE TABLE api_keys(id bigint PRIMARY KEY,name text);CREATE TABLE users(id bigint PRIMARY KEY,email text);CREATE TABLE accounts(id bigint PRIMARY KEY,name text);CREATE TABLE groups(id bigint PRIMARY KEY,name text);INSERT INTO api_keys VALUES(1,'key');INSERT INTO users VALUES(1,'user@example.test');INSERT INTO accounts VALUES(1,'account');INSERT INTO groups VALUES(1,'group');INSERT INTO usage_logs(id,created_at,requested_model,model,api_key_id,user_id,account_id,group_id,actual_cost,total_cost,rate_multiplier) VALUES(1,'2026-09-22','client','backend',1,1,1,1,0.12345678,0.2,1),(2,'2026-09-22','client','backend',1,1,1,1,0.12345678,0.2,1)`)
	require.NoError(t, err)

	return db, conn, schema
}
