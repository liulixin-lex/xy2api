package repository

import (
	"context"
	"testing"

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
