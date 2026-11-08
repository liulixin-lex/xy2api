//go:build unit

package repository

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type budgetTimingScanner struct{ values map[string]any }

func (s budgetTimingScanner) Scan(dest ...any) error {
	columns := strings.Split(usageLogSelectColumns, ", ")
	for i, p := range dest {
		if value, ok := s.values[columns[i]]; ok {
			reflect.ValueOf(p).Elem().Set(reflect.ValueOf(value))
		}
	}
	return nil
}
func TestBudgetTimingRepositoryRoundTripShape(t *testing.T) {
	zero := int64(0)
	yes := true
	arrival := "arrival"
	cf := "cf"
	log := &service.UsageLog{}
	log.BodyBytes = &zero
	log.AnchorEligible = &yes
	log.AnchorBasis = &arrival
	log.CDNObserved = &cf
	log.BudgetStartMode = &arrival
	t0 := int64(1234)
	log.BudgetT0UnixMs = &t0
	prepared := prepareUsageLogInsert(log)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, &zero, prepared.args[67])
	require.Equal(t, &arrival, prepared.args[70])
	require.Equal(t, &yes, prepared.args[71])
	require.Equal(t, &cf, prepared.args[len(prepared.args)-3])
	loaded, err := scanUsageLog(budgetTimingScanner{map[string]any{"body_bytes": sql.NullInt64{Int64: 0, Valid: true}, "anchor_basis": sql.NullString{String: arrival, Valid: true}, "anchor_eligible": sql.NullBool{Bool: true, Valid: true}, "cdn_observed": sql.NullString{String: cf, Valid: true}, "budget_start_mode": sql.NullString{String: arrival, Valid: true}, "budget_t0_unix_ms": sql.NullInt64{Int64: t0, Valid: true}}})
	require.NoError(t, err)
	require.Equal(t, log.BodyBytes, loaded.BodyBytes)
	require.Equal(t, log.AnchorBasis, loaded.AnchorBasis)
	require.Equal(t, log.AnchorEligible, loaded.AnchorEligible)
	require.Equal(t, log.CDNObserved, loaded.CDNObserved)
	require.Equal(t, log.BudgetStartMode, loaded.BudgetStartMode)
	require.Equal(t, log.BudgetT0UnixMs, loaded.BudgetT0UnixMs)
	require.Equal(t, &arrival, prepared.args[len(prepared.args)-2])
	require.Equal(t, &t0, prepared.args[len(prepared.args)-1])
	require.Nil(t, loaded.BodyEOFMs)
	historical, err := scanUsageLog(budgetTimingScanner{})
	require.NoError(t, err)
	require.Nil(t, historical.BodyBytes)
	require.Nil(t, historical.AnchorEligible)
	json := `{"body_bytes":0}`
	args := opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{ResilienceTiming: &json})
	require.Equal(t, sql.NullString{String: json, Valid: true}, args[42])
	require.Contains(t, insertOpsErrorLogSQL, "resilience_timing")
}
