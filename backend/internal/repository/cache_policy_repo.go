package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/liulixin-lex/xy2api/ent/setting"
)

// UpdateCachePolicy locks the settings row through commit. INSERT ON CONFLICT
// handles concurrent first writers; validation failure rolls the insert back.
func (r *settingRepository) UpdateCachePolicy(ctx context.Context, key string, update func(string) (string, error)) error {
	switch key {
	case "cache_feature_master", "cache_observability_policy", "cache_northstar_policy", "cache_upstream_identity_policy", "cache_anthropic_identity_policy", "cache_routing_policy":
	default:
		return fmt.Errorf("invalid cache policy key")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if err = c.Setting.Create().SetKey(key).SetValue("").SetUpdatedAt(time.Now()).OnConflictColumns(setting.FieldKey).Ignore().Exec(ctx); err != nil {
		return err
	}
	row, err := c.Setting.Query().Where(setting.KeyEQ(key)).ForUpdate().Only(ctx)
	if err != nil {
		return err
	}
	next, err := update(row.Value)
	if err != nil {
		return err
	}
	if err = c.Setting.UpdateOneID(row.ID).SetValue(next).SetUpdatedAt(time.Now()).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}
