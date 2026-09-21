package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/setting"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	if isCodexTicketGlobalKey(key) {
		return r.setCodexFenced(ctx, map[string]string{key: value}, "")
	}
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key := range settings {
		if isCodexTicketGlobalKey(key) {
			return r.setCodexFenced(ctx, settings, "")
		}
	}
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	if isCodexTicketGlobalKey(key) {
		return r.setCodexFenced(ctx, nil, key)
	}
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}

func isCodexTicketGlobalKey(key string) bool {
	return key == service.SettingKeyOpenAICodexTicketEnabled || key == service.SettingKeyOpenAICodexTicketHarvestProxyURL || key == service.SettingKeyCodexTicketProxyPool
}
func (r *settingRepository) setCodexFenced(ctx context.Context, values map[string]string, deleteKey string) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if _, err = c.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421023)"); err != nil {
		return err
	}
	for key, expected := range service.CodexSettingExpectations(ctx) {
		current, e := c.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		value := ""
		if current != nil {
			value = current.Value
		}
		if value != expected {
			return service.ErrCodexTicketConflict
		}
	}
	if _, legacyWrite := values[service.SettingKeyOpenAICodexTicketHarvestProxyURL]; legacyWrite {
		pool, e := c.Setting.Query().Where(setting.KeyEQ(service.SettingKeyCodexTicketProxyPool)).Only(ctx)
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		if pool != nil {
			// Once migrated, use the versioned pool endpoint. Never overwrite it
			// through a stale whole-settings form, including a single-entry pool.
			return service.ErrCodexTicketConflict
		}
	}
	for key, value := range values {
		if err = c.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(time.Now()).OnConflictColumns(setting.FieldKey).UpdateNewValues().Exec(ctx); err != nil {
			return err
		}
	}
	if deleteKey != "" {
		if _, err = c.Setting.Delete().Where(setting.KeyEQ(deleteKey)).Exec(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CompareAndSwapCodexSetting shares the lock used by account publication fences.
func (r *settingRepository) CompareAndSwapCodexSetting(ctx context.Context, key, old, next string) (bool, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if _, err = c.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421023)"); err != nil {
		return false, err
	}
	for key, expected := range service.CodexSettingExpectations(ctx) {
		current, e := c.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
		if e != nil && !ent.IsNotFound(e) {
			return false, e
		}
		value := ""
		if current != nil {
			value = current.Value
		}
		if value != expected {
			return false, nil
		}
	}
	value, err := c.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return false, err
	}
	current := ""
	if value != nil {
		current = value.Value
	}
	if current != old {
		return false, nil
	}
	if !json.Valid([]byte(next)) {
		return false, errors.New("invalid STATE settings JSON")
	}
	if err = c.Setting.Create().SetKey(key).SetValue(next).SetUpdatedAt(time.Now()).OnConflictColumns(setting.FieldKey).UpdateNewValues().Exec(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
