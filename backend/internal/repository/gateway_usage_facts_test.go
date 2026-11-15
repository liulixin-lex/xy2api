//go:build unit

package repository

import (
	"database/sql/driver"
	"encoding/json"
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
			require.Equal(t, "stop", frozen.ConversionNotices[0].Field)
			require.True(t, json.Valid(frozen.PricingSource))
			prepared := prepareUsageLogInsert(restored.Usage)
			require.Len(t, prepared.args, len(usageLogInsertArgTypes))
			key := usageLogBatchKey("fixture", 2)
			batch, _ := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
			best, _ := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
			for _, column := range []string{"request_scope", "usage_observed", "usage_source", "terminal_event_seen", "terminal_event_valid", "response_body_type", "degradations", "pricing_source"} {
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
