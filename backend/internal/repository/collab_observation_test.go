//go:build unit

package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"github.com/stretchr/testify/require"
)

func collabFixture() json.RawMessage {
	return resilience.EncodeCollabObservation(&resilience.CollabObservation{Thread: resilience.CollabDigest(strings.Repeat("a", 64)), StopReason: resilience.CollabReasonCanceled, SendTotal: 2})
}

func collabSparseFixture() json.RawMessage {
	return json.RawMessage(`{"v":3,"thread":"` + strings.Repeat("a", 64) + `","stop_reason":"canceled","send_total":2}`)
}

func TestCollabObservationUsageSingleBatchAndScan(t *testing.T) {
	row := &service.UsageLog{UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "synthetic-observation", Model: "synthetic", CreatedAt: time.Now().UTC(), CollabObservation: collabFixture()}
	frozen := service.BillingUsageSnapshot(row)
	payload, err := json.Marshal(service.UsageBillingCommand{Usage: frozen})
	require.NoError(t, err)
	var restored service.UsageBillingCommand
	require.NoError(t, json.Unmarshal(payload, &restored))
	row.CollabObservation[0] = 'x'
	require.Equal(t, collabFixture(), restored.Usage.CollabObservation)
	prepared := prepareUsageLogInsert(restored.Usage)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, string(collabFixture()), prepared.args[len(prepared.args)-3])
	key := usageLogBatchKey(prepared.requestID, 2)
	batch, args := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
	best, bestArgs := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Equal(t, 3, strings.Count(batch, "collab_observation"))
	require.Equal(t, 3, strings.Count(best, "collab_observation"))
	require.Equal(t, string(collabFixture()), args[len(args)-3])
	require.Equal(t, string(collabFixture()), bestArgs[len(bestArgs)-3])

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	columns := strings.Split(usageLogSelectColumns, ",")
	values := []driver.Value{int64(8)}
	for _, arg := range prepared.args {
		v, e := driver.DefaultParameterConverter.ConvertValue(arg)
		require.NoError(t, e)
		values = append(values, v)
	}
	for _, raw := range []driver.Value{nil, []byte(collabFixture()), []byte(collabSparseFixture()), []byte("{malformed}")} {
		values[len(values)-3] = raw
		mock.ExpectQuery("SELECT fixture").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
		got, err := scanUsageLog(db.QueryRow("SELECT fixture"))
		require.NoError(t, err)
		if b, ok := raw.([]byte); ok && json.Valid(b) {
			require.Equal(t, collabFixture(), got.CollabObservation)
		} else {
			require.Nil(t, got.CollabObservation)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCollabObservationOptionalWritesCannotLoseUsageRows(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"valid":  collabFixture(),
		"sparse": collabSparseFixture(),
		"absent": nil,
		"nul":    json.RawMessage("{\"v\":2,\"thread\":\"\\u0000\"}"),
		"utf8":   append([]byte("{\"v\":2,\"thread\":\""), append([]byte{0xff}, []byte("\"}")...)...),
		"deep":   json.RawMessage("{\"v\":2,\"extra\":" + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + "}"),
	} {
		for _, path := range []string{"single", "no_result", "batch", "best_effort", "unbilled"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				var queries []string
				db, mock := newSQLCapturingMock(t, &queries)
				now := time.Now().UTC()
				row := &service.UsageLog{UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "synthetic-write", CreatedAt: now, CollabObservation: raw}
				prepared := prepareUsageLogInsert(row)
				var expected any
				if name == "valid" || name == "sparse" {
					expected = string(collabFixture())
				}
				require.Equal(t, expected, prepared.args[len(prepared.args)-3])
				repo := newUsageLogRepositoryWithSQL(nil, db)
				switch path {
				case "single":
					mock.ExpectQuery("INSERT").WithArgs(anySliceToDriverValues(prepared.args)...).WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(7, now))
					inserted, err := repo.createSingle(context.Background(), db, row)
					require.NoError(t, err)
					require.True(t, inserted)
					require.EqualValues(t, 7, row.ID)
				case "no_result":
					mock.ExpectExec("INSERT").WithArgs(anySliceToDriverValues(prepared.args)...).WillReturnResult(sqlmock.NewResult(7, 1))
					require.NoError(t, execUsageLogInsertNoResult(context.Background(), db, prepared))
				case "batch":
					key := usageLogBatchKey(row.RequestID, 2)
					_, args := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
					require.Equal(t, expected, args[len(args)-3])
					payload, err := json.Marshal([]usageLogBatchRow{{RequestID: row.RequestID, APIKeyID: 2, ID: 7, CreatedAt: now, Inserted: true}})
					require.NoError(t, err)
					mock.ExpectQuery("WITH").WithArgs(anySliceToDriverValues(args)...).WillReturnRows(sqlmock.NewRows([]string{"json_agg"}).AddRow(payload))
					inserted, _, _, err := repo.batchInsertUsageLogs(db, []string{key}, map[string]usageLogInsertPrepared{key: prepared})
					require.NoError(t, err)
					require.True(t, inserted[key])
				case "best_effort":
					_, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
					require.Equal(t, expected, args[len(args)-3])
					mock.ExpectExec("WITH").WithArgs(anySliceToDriverValues(args)...).WillReturnResult(sqlmock.NewResult(7, 1))
					result := make(chan error, 1)
					repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{{prepared: prepared, apiKeyID: 2, resultCh: result}})
					require.NoError(t, <-result)
				case "unbilled":
					args := make([]driver.Value, 31)
					for i := range args {
						args[i] = sqlmock.AnyArg()
					}
					args[30] = expected
					mock.ExpectExec("INSERT").WithArgs(args...).WillReturnResult(sqlmock.NewResult(7, 1))
					require.NoError(t, repo.CreateUnbilledRequest(context.Background(), &service.GatewayUnbilledRequest{RequestID: row.RequestID, CreatedAt: now, APIKeyID: 2, Result: resilience.RequestCanceled, CollabObservation: raw}))
				}
				require.Len(t, queries, 1, "observation must not issue a second statement")
				require.NotContains(t, queries[0], "UPDATE")
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

func TestCollabObservationInvalidBindingDoesNotPanic(t *testing.T) {
	// The optional writer is gone; absent or malformed input never invokes it.
	require.NotPanics(t, func() {
		require.Nil(t, collabObservationSQL(nil))
		require.Nil(t, collabObservationSQL([]byte("{invalid")))
	})
}

func TestCollabObservationCorruptRecordDoesNotBreakBillingIntent(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage("{bad"), json.RawMessage(strings.Repeat("x", resilience.CollabObservationMaxBytes+1))} {
		row := service.BillingUsageSnapshot(&service.UsageLog{CollabObservation: raw, ActualCost: 1, InputTokens: 10})
		require.Nil(t, row.CollabObservation)
		_, err := json.Marshal(service.UsageBillingCommand{Usage: row})
		require.NoError(t, err)
		require.Equal(t, 1.0, row.ActualCost)
		require.Equal(t, 10, row.InputTokens)
		prepared := prepareUsageLogInsert(row)
		require.Nil(t, prepared.args[len(prepared.args)-3])
	}
}

func TestCollabObservationAdminUnionProjectsBothSources(t *testing.T) {
	require.Equal(t, 2, strings.Count(adminRequestsCTE, "collab_observation"))

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, source := range []string{"usage", "unbilled"} {
		for _, raw := range []driver.Value{nil, []byte(collabFixture()), []byte(collabSparseFixture()), []byte("{malformed}")} {
			values := []driver.Value{source, int64(7), "synthetic-request", int64(4), int64(0), false, false, time.Now(), int64(1), int64(2), nil, nil, "synthetic", int64(2), true, nil, nil, int64(20), nil, nil, nil, nil, raw}
			columns := make([]string, len(values))
			for i := range columns {
				columns[i] = fmt.Sprint(i)
			}
			mock.ExpectQuery("SELECT fixture").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
			got, err := scanAdminUsageRequest(db.QueryRow("SELECT fixture"))
			require.NoError(t, err)
			require.Equal(t, source, got.Source)
			require.Equal(t, service.RequestTypeStream, got.Log.RequestType)
			require.Nil(t, got.InputTokens)
			if b, ok := raw.([]byte); ok && json.Valid(b) {
				require.Equal(t, collabFixture(), got.Log.CollabObservation)
			} else {
				require.Nil(t, got.Log.CollabObservation)
			}
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCollabObservationBatchWritesDocumentInInsert(t *testing.T) {
	for _, best := range []bool{false, true} {
		t.Run(fmt.Sprint(best), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			row := &service.UsageLog{APIKeyID: 2, RequestID: "synthetic-batch", CreatedAt: time.Now().UTC(), CollabObservation: collabFixture()}
			prepared := prepareUsageLogInsert(row)
			key := usageLogBatchKey(row.RequestID, 2)
			if best {
				mock.ExpectExec("WITH input").WithArgs(anySliceToDriverValues(prepared.args)...).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				raw, err := json.Marshal([]usageLogBatchRow{{RequestID: row.RequestID, APIKeyID: 2, ID: 4, CreatedAt: row.CreatedAt, Inserted: true}})
				require.NoError(t, err)
				mock.ExpectQuery("WITH input").WithArgs(anySliceToDriverValues(append([]any{0}, prepared.args...))...).WillReturnRows(sqlmock.NewRows([]string{"json_agg"}).AddRow(raw))
			}
			repo := newUsageLogRepositoryWithSQL(nil, db)
			if best {
				result := make(chan error, 1)
				repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{{prepared: prepared, apiKeyID: 2, resultCh: result}})
				require.NoError(t, <-result)
			} else {
				inserted, _, _, err := repo.batchInsertUsageLogs(db, []string{key}, map[string]usageLogInsertPrepared{key: prepared})
				require.NoError(t, err)
				require.True(t, inserted[key])
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCollabObservationNeverEntersExportProjection(t *testing.T) {
	for _, format := range []string{"csv", "xlsx"} {
		t.Run(format, func(t *testing.T) {
			var captured []string
			db, mock := newSQLCapturingMock(t, &captured)
			mock.ExpectBegin()
			tx, err := db.Begin()
			require.NoError(t, err)
			mock.ExpectExec("DECLARE").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("FETCH").WillReturnRows(sqlmock.NewRows([]string{"record"}))
			err = usageExportSnapshot(context.Background(), tx, usageexport.Options{Format: format, Version: usageexport.ServiceTierFormatVersion}, func([]string) error { t.Fatal("unexpected export row"); return nil })
			require.NoError(t, err)
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.Len(t, captured, 2)
			for _, query := range captured {
				require.NotContains(t, query, "collab_observation")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCollabObservationSingleInsertCreatesUsageRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	row := &service.UsageLog{APIKeyID: 2, RequestID: "synthetic-slow-write", CreatedAt: time.Now().UTC(), CollabObservation: collabFixture()}
	prepared := prepareUsageLogInsert(row)
	mock.ExpectQuery("INSERT INTO usage_logs").WithArgs(anySliceToDriverValues(prepared.args)...).WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(7, row.CreatedAt))
	// A slow optional update is forbidden: there is no expected second write.
	// An unexpected query fails the mock as well as the one-insert assertions.
	started := time.Now()
	inserted, err := newUsageLogRepositoryWithSQL(nil, db).createSingle(context.Background(), db, row)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCollabObservationSparseBillingSnapshotStaysV2(t *testing.T) {
	row := &service.UsageLog{CollabObservation: collabSparseFixture(), ActualCost: 1, InputTokens: 10}
	frozen := service.BillingUsageSnapshot(row)
	require.Equal(t, collabFixture(), frozen.CollabObservation)
	require.Equal(t, row.ActualCost, frozen.ActualCost)
	require.Equal(t, row.InputTokens, frozen.InputTokens)
	payload, err := json.Marshal(service.UsageBillingCommand{Usage: frozen})
	require.NoError(t, err)
	var restored service.UsageBillingCommand
	require.NoError(t, json.Unmarshal(payload, &restored))
	prepared := prepareUsageLogInsert(restored.Usage)
	require.Equal(t, string(collabFixture()), prepared.args[len(prepared.args)-1])
}
