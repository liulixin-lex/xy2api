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

func TestCreateAccountRecordProbeDefault(t *testing.T) {
	for _, tc := range []struct {
		name, accountType string
		extra             map[string]any
		want              any
	}{
		{"default", service.AccountTypeAPIKey, nil, true},
		{"preserve disable", service.AccountTypeAPIKey, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: false}, false},
		{"oauth", service.AccountTypeOAuth, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectQuery(`INSERT INTO "accounts"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
			account := &service.Account{Name: "created", Platform: service.PlatformGemini, Type: tc.accountType, Extra: tc.extra, Status: service.StatusActive}
			require.NoError(t, createAccountRecord(context.Background(), client, account))
			require.Equal(t, tc.want, account.Extra[service.UpstreamBillingProbeEnabledExtraKey])
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
