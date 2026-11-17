//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedQualitySingleConnectionConfiguration(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		t.Run(map[bool]string{false: "cycle", true: "exclusive"}[exclusive], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			db.SetMaxOpenConns(1)
			repo := &schedQualityRepository{db: db}
			mock.ExpectBegin()
			mock.ExpectExec("SELECT set_config").WillReturnResult(sqlmock.NewResult(0, 1))
			if exclusive {
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(schedQualityCycleLockID).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WithArgs(schedQualityCycleLockID).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
			}
			mock.ExpectQuery("SELECT value FROM settings").WithArgs(service.SettingKeyGroupQualityGatePolicies).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(`{"version":3,"groups":{}}`))
			mock.ExpectQuery("SELECT id FROM groups").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
			mock.ExpectQuery("SELECT value FROM settings").WithArgs(service.SettingKeySchedQualityTTFTRule).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(`{"version":2,"confidence":0.9,"tolerance":0.2,"peer_margin":0.05}`))
			mock.ExpectCommit()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			callback := func(tx service.SchedQualityTx) error {
				policies, e := tx.Policies()
				if e != nil {
					return e
				}
				require.EqualValues(t, 3, policies[7].Version)
				require.Equal(t, q.DefaultPolicy(), policies[7].Policy)
				rule, e := tx.TTFTRule()
				if e != nil {
					return e
				}
				require.EqualValues(t, 2, rule.Version)
				require.Equal(t, .9, rule.Params.Confidence)
				return nil
			}
			if exclusive {
				err = repo.RunExclusive(ctx, callback)
			} else {
				var locked bool
				locked, err = repo.RunCycle(ctx, callback)
				require.True(t, locked)
			}
			require.NoError(t, err)
			require.Zero(t, db.Stats().WaitCount, "transaction config reads must not borrow another connection")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
