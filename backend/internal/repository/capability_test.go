package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCapabilityAccountSaveKeepsLockedLearningState(t *testing.T) {
	for _, tc := range []struct {
		name, current string
		want          any
	}{
		{"latest observation", `{"capability_learned_responses":{"fingerprint":"latest"}}`, map[string]any{"fingerprint": "latest"}},
		{"cleared observation", `{"capability_learned_responses":null}`, nil},
		{"no observation", `{}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectQuery(`(?s)SELECT.*FOR NO KEY UPDATE`).
				WithArgs(int64(41), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"test"}`, nil).
				WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "ollama_group_unchanged", "ollama_proxy_unchanged", "enabled", "rate_sync_enabled", "snapshot", "ollama_session", "ollama_auto", "ollama_snapshot", "opencode_group_unchanged", "opencode_auto", "opencode_snapshot", "current_extra"}).
					AddRow(true, false, true, nil, nil, nil, nil, nil, nil, false, nil, nil, []byte(tc.current)))
			account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{
				"capability_learned_responses": map[string]any{"fingerprint": "stale"},
				"capability_learned_chat":      map[string]any{"fingerprint": "injected"},
				"new_admin_setting":            true,
			}}
			extra, err := lockAndMergeAccountProbeExtra(context.Background(), client, account, nil, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, extra["capability_learned_responses"])
			require.NotContains(t, extra, "capability_learned_chat")
			require.Equal(t, true, extra["new_admin_setting"])
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCapabilityMarkLimitLocksCurrentRowAndDoesNotWriteOverflow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)
	current := map[string]any{}
	for i := 0; i < service.CapabilityMarkLimit; i++ {
		current[fmt.Sprintf("capability_marks_%d", i)] = service.CapabilityMark{Model: fmt.Sprintf("fixture-%d", i), Protocol: "responses", Shape: "*", Reason: "model_unsupported", Fingerprint: "synthetic-fingerprint", ExpiresAt: time.Now().Add(time.Hour)}
	}
	raw, err := json.Marshal(current)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra FROM accounts WHERE id = \$1 AND deleted_at IS NULL FOR NO KEY UPDATE`).WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow(raw))
	// No UPDATE or outbox write is allowed when the locked row is full.
	mock.ExpectRollback()
	incoming := service.CapabilityMark{Model: "overflow-fixture", Protocol: "responses", Shape: "*", Reason: "model_unsupported", Fingerprint: "synthetic-fingerprint", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, repo.UpdateExtra(context.Background(), 41, map[string]any{"capability_marks_overflow": incoming}))
	require.NoError(t, mock.ExpectationsWereMet())
}
