package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/liulixin-lex/xy2api/internal/service"
)

type adminOperatorPermissionRepository struct{ db *sql.DB }

func NewAdminOperatorPermissionRepository(db *sql.DB) service.AdminOperatorPermissionRepository {
	return &adminOperatorPermissionRepository{db: db}
}
func (r *adminOperatorPermissionRepository) Get(ctx context.Context, userID int64) ([]byte, bool, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT permissions FROM admin_operator_permissions WHERE user_id = $1`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get operator permissions: %w", err)
	}
	return raw, true, nil
}
func (r *adminOperatorPermissionRepository) Upsert(ctx context.Context, userID int64, raw []byte, updatedBy int64) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO admin_operator_permissions (user_id, permissions, updated_by, updated_at) VALUES ($1, $2::jsonb, $3, NOW()) ON CONFLICT (user_id) DO UPDATE SET permissions = EXCLUDED.permissions, updated_by = EXCLUDED.updated_by, updated_at = NOW()`, userID, string(raw), updatedBy)
	if err != nil {
		return fmt.Errorf("upsert operator permissions: %w", err)
	}
	return nil
}
