package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Covers the real repository through a fake SQL driver: validation must see a
// locked row, failures roll back the initial insert, and success commits once.
func TestCachePolicyRepositoryTransaction(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "commit"
		if reject {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			repo := &settingRepository{client: client}
			key := "cache_observability_policy"
			old := `{"rev":4,"spec":{"enabled":false}}`
			next := `{"rev":5,"spec":{"enabled":true}}`
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO "settings"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			mock.ExpectQuery(`SELECT .* FROM "settings" .* FOR UPDATE`).WithArgs(key).
				WillReturnRows(sqlmock.NewRows([]string{"id", "key", "value", "updated_at"}).AddRow(1, key, old, time.Now()))
			rejected := errors.New("revision conflict")
			if reject {
				mock.ExpectRollback()
			} else {
				mock.ExpectExec(`UPDATE "settings"`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT .* FROM "settings"`).WillReturnRows(sqlmock.NewRows([]string{"id", "key", "value", "updated_at"}).AddRow(1, key, next, time.Now()))
				mock.ExpectCommit()
			}
			err = repo.UpdateCachePolicy(context.Background(), key, func(raw string) (string, error) {
				require.Equal(t, old, raw)
				if reject {
					return "", rejected
				}
				return next, nil
			})
			if reject {
				require.ErrorIs(t, err, rejected)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
func TestCachePolicyRepositoryRejectsUnrelatedSettings(t *testing.T) {
	r := &settingRepository{}
	require.Error(t, r.UpdateCachePolicy(context.Background(), "gateway_resilience_settings", nil))
}

func TestCachePolicyAuditCoverageRepository(t *testing.T) {
	for _, failing := range []bool{false, true} {
		name := "aggregate"
		if failing {
			name = "query_error"
		}
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repository := NewAuditLogRepository(db)
			query := mock.ExpectQuery("SELECT COUNT\\(DISTINCT").WithArgs("%admin.cache.policies.master.update%", "PUT")
			queryError := errors.New("unavailable")
			if failing {
				query.WillReturnError(queryError)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"count", "min", "max"}).AddRow(2, 1, 3))
			}
			result, err := repository.List(context.Background(), &service.AuditLogFilter{
				Action: "admin.cache.policies.master.update", Method: "PUT",
				CachePolicyOnly: true, CachePolicyRevisionOnly: true, CachePolicyCoverage: true,
			})
			if failing {
				require.ErrorIs(t, err, queryError)
			} else {
				require.NoError(t, err)
				require.Equal(t, &service.CachePolicyRevisionCoverage{Count: 2, MinRev: 1, MaxRev: 3}, result.PolicyRevisions)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
