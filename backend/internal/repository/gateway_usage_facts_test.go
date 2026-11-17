//go:build unit

package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayUsageFactsSurviveIntentAndSQL(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		known, present, seen, valid bool
		source, kind                string
	}{
		{name: "unknown"},
		{"missing", true, false, false, false, "missing", "json"},
		{"explicit_zero", true, true, false, false, "upstream_json", "json"},
		{"valid_sse", true, true, true, true, "upstream_sse_terminal", "sse"},
		{"invalid_terminal", true, true, true, false, "upstream_sse_progressive", "sse"},
		{"missing_terminal", true, false, false, false, "missing", "sse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := &service.UsageLog{UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "fixture", Model: "fixture", CreatedAt: time.Now().UTC(), RequestScope: resilience.GatewayRequestScopeText, ConversionNotices: []apicompat.ConversionNotice{{Field: "stop", Action: "omit"}}, PricingSource: json.RawMessage(`{"matched_key":"fixture","source":"litellm","revision":"fixture-revision","effective_prices":{"input":0}}`)}
			row.DispatchPhases = json.RawMessage(`{"body_decode_ms":12,"upstream_prepare_ms":34}`)
			if tc.known {
				row.UsageObserved = &tc.present
				row.UsageSource = &tc.source
				row.TerminalEventSeen = &tc.seen
				row.TerminalEventValid = &tc.valid
				row.ResponseBodyType = &tc.kind
			}
			frozen := service.BillingUsageSnapshot(row)
			payload, err := json.Marshal(service.UsageBillingCommand{Usage: frozen})
			require.NoError(t, err)
			var restored service.UsageBillingCommand
			require.NoError(t, json.Unmarshal(payload, &restored))
			row.ConversionNotices[0].Field = "mutated"
			row.PricingSource[2] = 'X'
			row.DispatchPhases[2] = 'X'
			require.JSONEq(t, `{"body_decode_ms":12,"upstream_prepare_ms":34}`, string(frozen.DispatchPhases))
			require.Equal(t, "stop", frozen.ConversionNotices[0].Field)
			require.True(t, json.Valid(frozen.PricingSource))
			prepared := prepareUsageLogInsert(restored.Usage)
			require.Len(t, prepared.args, len(usageLogInsertArgTypes))
			key := usageLogBatchKey("fixture", 2)
			batch, _ := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
			best, _ := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
			for _, column := range []string{"request_scope", "usage_observed", "usage_source", "terminal_event_seen", "terminal_event_valid", "response_body_type", "degradations", "pricing_source", "dispatch_phases"} {
				require.Equal(t, 3, strings.Count(batch, column))
				require.Equal(t, 3, strings.Count(best, column))
			}
			values := []driver.Value{int64(99)}
			for _, arg := range prepared.args {
				value, err := driver.DefaultParameterConverter.ConvertValue(arg)
				require.NoError(t, err)
				values = append(values, value)
			}
			columns := strings.Split(usageLogSelectColumns, ",")
			for i := range columns {
				columns[i] = strings.TrimSpace(columns[i])
			}
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			mock.ExpectQuery("SELECT fixture").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
			got, err := scanUsageLog(db.QueryRow("SELECT fixture"))
			require.NoError(t, err)
			require.Equal(t, frozen.RequestScope, got.RequestScope)
			require.Equal(t, frozen.UsageObserved, got.UsageObserved)
			require.Equal(t, frozen.UsageSource, got.UsageSource)
			require.Equal(t, frozen.TerminalEventSeen, got.TerminalEventSeen)
			require.Equal(t, frozen.TerminalEventValid, got.TerminalEventValid)
			require.Equal(t, frozen.ResponseBodyType, got.ResponseBodyType)
			require.Equal(t, frozen.ConversionNotices, got.ConversionNotices)
			require.JSONEq(t, string(frozen.PricingSource), string(got.PricingSource))
			require.JSONEq(t, string(frozen.DispatchPhases), string(got.DispatchPhases))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
	unknown := prepareUsageLogInsert(&service.UsageLog{})
	for _, arg := range unknown.args[90:] {
		value, err := driver.DefaultParameterConverter.ConvertValue(arg)
		require.NoError(t, err)
		require.Nil(t, value)
	}
}

// Capture the value actually bound by INSERT; the SELECT returns that value,
// so omitting or changing either persistence boundary fails the round trip.
type noticeArgument struct{ raw []byte }

func (a *noticeArgument) Match(value driver.Value) bool {
	switch value := value.(type) {
	case []byte:
		a.raw = append([]byte(nil), value...)
	case string:
		a.raw = []byte(value)
	default:
		return false
	}
	return json.Valid(a.raw)
}

func TestGatewayUnbilledAndOpsDegradationsReadBack(t *testing.T) {
	for _, result := range []int16{3, 4} {
		t.Run(fmt.Sprint(result), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			notices := []apicompat.ConversionNotice{{Field: "stop", Action: "omit"}, {Field: "reasoning.effort", Action: "map"}}
			row := &service.GatewayUnbilledRequest{RequestID: "failed-no-usage", CreatedAt: time.Now(), Result: result, ConversionNotices: notices}
			capture := &noticeArgument{}
			args := make([]driver.Value, 29)
			for i := range args {
				args[i] = sqlmock.AnyArg()
			}
			for i := 22; i < 27; i++ {
				args[i] = nil
			}
			args[27] = capture
			mock.ExpectExec("INSERT INTO gateway_unbilled_requests").WithArgs(args...).WillReturnResult(sqlmock.NewResult(1, 1))
			repo := newUsageLogRepositoryWithSQL(nil, db)
			require.NoError(t, repo.CreateUnbilledRequest(context.Background(), row))
			require.NotEmpty(t, capture.raw)
			mock.ExpectQuery("SELECT degradations, usage_observed, usage_source, terminal_event_seen, terminal_event_valid, response_body_type FROM gateway_unbilled_requests").WillReturnRows(sqlmock.NewRows([]string{"degradations", "usage_observed", "usage_source", "terminal_event_seen", "terminal_event_valid", "response_body_type"}).AddRow(capture.raw, nil, nil, nil, nil, nil))
			var raw []byte
			var observed, seen, valid sql.NullBool
			var source, kind sql.NullString
			require.NoError(t, db.QueryRow("SELECT degradations, usage_observed, usage_source, terminal_event_seen, terminal_event_valid, response_body_type FROM gateway_unbilled_requests").Scan(&raw, &observed, &source, &seen, &valid, &kind))
			var decoded []apicompat.ConversionNotice
			require.NoError(t, json.Unmarshal(raw, &decoded))
			require.Equal(t, notices, decoded)
			require.False(t, observed.Valid || source.Valid || seen.Valid || valid.Valid || kind.Valid)
			ops := &opsRepository{db: db}
			input := &service.OpsInsertErrorLogInput{RequestID: row.RequestID, GatewayRequestID: row.RequestID, CreatedAt: row.CreatedAt, ConversionNotices: notices}
			opsArgs := make([]driver.Value, 45)
			for i := range opsArgs {
				opsArgs[i] = sqlmock.AnyArg()
			}
			opsCapture := &noticeArgument{}
			opsArgs[44] = opsCapture
			mock.ExpectQuery("INSERT INTO ops_error_logs").WithArgs(opsArgs...).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
			id, err := ops.InsertErrorLog(context.Background(), input)
			require.NoError(t, err)
			// GetErrorLogByID has 51 selected values; nullable joins are kept nil.
			values := make([]driver.Value, 51)
			columns := make([]string, 51)
			for i := range values {
				values[i] = ""
				columns[i] = fmt.Sprint(i)
			}
			values[0], values[1], values[7], values[10], values[21], values[31] = int64(9), row.CreatedAt, int64(502), false, false, false
			for _, i := range []int{11, 12, 17, 22, 24, 25, 27, 29, 36, 38, 39, 40, 41, 42, 43, 44, 45, 48} {
				values[i] = nil
			}
			values[50] = opsCapture.raw
			mock.ExpectQuery("SELECT.*e.id").WithArgs(id).WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
			detail, err := ops.GetErrorLogByID(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, notices, detail.ConversionNotices)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
