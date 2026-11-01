package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/liulixin-lex/xy2api/ent"
	_ "github.com/liulixin-lex/xy2api/ent/runtime"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

type captureEntQueryMatcher struct {
	actual *string
}

func (m captureEntQueryMatcher) Match(_, actual string) error {
	if m.actual == nil {
		return fmt.Errorf("query capture target is nil")
	}
	*m.actual = actual
	return nil
}

func TestListSchedulableAccountLoadsUsesSingleProjectionQuery(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)

	mock.ExpectQuery("schedulable account load projection").
		WillReturnRows(sqlmock.NewRows([]string{"id", "concurrency", "load_factor"}).
			AddRow(int64(11), 3, nil).
			AddRow(int64(12), 2, 7))

	loads, err := repo.ListSchedulableAccountLoads(context.Background())
	require.NoError(t, err)
	require.Len(t, loads, 2)
	require.Equal(t, int64(11), loads[0].ID)
	require.Equal(t, 3, loads[0].MaxConcurrency)
	require.Equal(t, int64(12), loads[1].ID)
	require.Equal(t, 7, loads[1].MaxConcurrency)
	require.NoError(t, mock.ExpectationsWereMet(), "projection path must execute exactly one query")

	normalized := normalizeSQLWhitespace(capturedSQL)
	selectClause, _, found := strings.Cut(normalized, " FROM ")
	require.True(t, found, "unexpected projection SQL: %s", normalized)
	require.Equal(t, 2, strings.Count(selectClause, ","), "projection must select exactly three columns: %s", selectClause)
	require.Contains(t, selectClause, `"id"`)
	require.Contains(t, selectClause, `"concurrency"`)
	require.Contains(t, selectClause, `"load_factor"`)
	require.NotContains(t, selectClause, "credentials")
	require.NotContains(t, selectClause, "extra")
	require.NotContains(t, selectClause, "proxy_id")
	require.NotContains(t, normalized, "account_groups")
	require.NotContains(t, normalized, "proxies")
	for _, predicateColumn := range []string{
		"status",
		"schedulable",
		"temp_unschedulable_until",
		"expires_at",
		"auto_pause_on_expired",
		"overload_until",
		"rate_limit_reset_at",
		"deleted_at",
	} {
		require.Contains(t, normalized, predicateColumn)
	}
	_, orderClause, hasOrder := strings.Cut(normalized, " ORDER BY ")
	require.True(t, hasOrder, "projection query must preserve schedulable account order: %s", normalized)
	require.Contains(t, orderClause, `"priority" ASC`)
}

func TestListOpsAccountsForStatsPreservesServabilityFields(t *testing.T) {
	var projection string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		if expected == "ops members" {
			projection = actual
		}
		return nil
	})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)
	expiry := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("ops members").WillReturnRows(sqlmock.NewRows([]string{"id", "type", "platform", "status", "schedulable", "rate_multiplier", "extra", "expires_at", "auto_pause_on_expired", "temp_unschedulable_reason"}).AddRow(1, "apikey", "openai", "active", true, 0.4, `{"quota_limit":5,"quota_used":5,"upstream_billing_probe":{"status":"ok"}}`, expiry, true, "balance:exhausted"))
	mock.ExpectQuery("member bindings").WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id"}).AddRow(1, 10))
	mock.ExpectQuery("group settings").WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "profit_control_enabled", "rate_multiplier", "profit_min_margin"}).AddRow(10, "openai", true, 2, 0.2))
	rows, err := repo.ListOpsAccountsForStats(context.Background(), "", nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "apikey", rows[0].Type)
	require.Equal(t, 0.4, *rows[0].RateMultiplier)
	require.True(t, rows[0].IsQuotaExceeded())
	require.NotNil(t, rows[0].Extra["upstream_billing_probe"])
	require.True(t, rows[0].AutoPauseOnExpired)
	require.True(t, expiry.Equal(*rows[0].ExpiresAt))
	require.Equal(t, "balance:exhausted", rows[0].TempUnschedulableReason)
	require.Len(t, rows[0].Groups, 1)
	require.True(t, rows[0].Groups[0].ProfitControlEnabled)
	selectClause, _, _ := strings.Cut(projection, " FROM ")
	for _, field := range []string{"type", "rate_multiplier", "extra", "expires_at", "auto_pause_on_expired", "temp_unschedulable_reason"} {
		require.Contains(t, selectClause, `"`+field+`"`)
	}
	require.NotContains(t, selectClause, "credentials")
	require.NoError(t, mock.ExpectationsWereMet())
}
