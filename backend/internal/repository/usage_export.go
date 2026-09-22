package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func NewUsageExportEngine(db *sql.DB, cfg *config.Config) (*usageexport.Engine, error) {
	if cfg.UsageExport.Directory == "./data/usage-exports" {
		if base := os.Getenv("DATA_DIR"); base != "" {
			cfg.UsageExport.Directory = filepath.Join(base, "usage-exports")
		}
	}
	var store usageexport.Store
	if cfg.UsageExport.Storage.Type == "s3" {
		s := cfg.UsageExport.Storage
		if s.Bucket == "" || s.AccessKeyID == "" || s.SecretAccessKey == "" {
			return nil, fmt.Errorf("usage_export S3 credentials required")
		}
		base, err := NewS3BackupStoreFactory()(context.Background(), &service.BackupS3Config{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, AccessKeyID: s.AccessKeyID, SecretAccessKey: s.SecretAccessKey, ForcePathStyle: s.ForcePathStyle})
		if err != nil {
			return nil, err
		}
		store = exportS3Store{base, strings.Trim(s.Prefix, "/")}
	} else if cfg.UsageExport.Storage.Type != "local" {
		return nil, fmt.Errorf("invalid usage_export storage type")
	}
	engine, err := usageexport.New(db, cfg.UsageExport, usageExportSnapshot, store)
	if err != nil {
		return nil, err
	}
	engine.Start()
	return engine, nil
}

type exportS3Store struct {
	store  service.BackupObjectStore
	prefix string
}

func (s exportS3Store) Put(ctx context.Context, key, file string) error {
	_, err := s.store.UploadFile(ctx, path.Join(s.prefix, key), file, "application/octet-stream")
	return err
}
func (s exportS3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.store.Download(ctx, path.Join(s.prefix, key))
}
func (s exportS3Store) Delete(ctx context.Context, key string) error {
	return s.store.Delete(ctx, path.Join(s.prefix, key))
}

func usageExportSnapshot(ctx context.Context, tx *sql.Tx, o usageexport.Options, emit func([]string) error) error {
	where, args := usageExportWhere(o.Filters)
	// Explicit projection: no prompt, credentials or non-export payload enters the spool.
	fields := strings.Fields("id created_at model requested_model reasoning_effort requested_reasoning_effort inbound_endpoint ip_address request_type stream openai_ws_mode billing_mode image_count input_tokens output_tokens cache_read_tokens cache_creation_tokens rate_multiplier actual_cost total_cost first_token_ms duration_ms api_key_id")
	if o.Format == "xlsx" {
		fields = append(fields, strings.Fields("user_id account_id group_id upstream_model upstream_response_model upstream_model_mismatch upstream_endpoint input_cost output_cost cache_read_cost cache_creation_cost account_rate_multiplier account_stats_cost request_id upstream_request_id user_agent")...)
	}
	pairs := make([]string, 0, len(fields)+4)
	for _, f := range fields {
		pairs = append(pairs, fmt.Sprintf("'%s',ul.%s", f, f))
	}
	pairs = append(pairs, "'key_name',k.name")
	joins := ` LEFT JOIN api_keys k ON k.id=ul.api_key_id`
	if o.Format == "xlsx" {
		pairs = append(pairs, "'user_email',u.email", "'account_name',a.name", "'group_name',g.name")
		joins += ` LEFT JOIN users u ON u.id=ul.user_id LEFT JOIN accounts a ON a.id=ul.account_id LEFT JOIN groups g ON g.id=ul.group_id`
	}
	order := usageLogOrderBy(pagination.PaginationParams{SortBy: o.SortBy, SortOrder: o.SortOrder})
	outerOrder := strings.NewReplacer("requested_model", "ul.requested_model", "model)", "ul.model)", "created_at", "ul.created_at", "id ", "ul.id ").Replace(order)
	query := `DECLARE usage_export_cursor NO SCROLL CURSOR FOR SELECT jsonb_build_object(` + strings.Join(pairs, ",") + `) FROM (SELECT ` + strings.Join(fields, ",") + ` FROM usage_logs ` + where + `) ul` + joins + ` ORDER BY ` + outerOrder
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	for {
		rows, err := tx.QueryContext(ctx, `FETCH FORWARD 2000 FROM usage_export_cursor`)
		if err != nil {
			return err
		}
		n := 0
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				_ = rows.Close()
				return err
			}
			var r usageexport.Record
			if err = json.Unmarshal(b, &r); err != nil {
				_ = rows.Close()
				return err
			}
			line, er := usageexport.Project(r, o)
			if er == nil {
				er = emit(line)
			}
			if er != nil {
				_ = rows.Close()
				return er
			}
			n++
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}
