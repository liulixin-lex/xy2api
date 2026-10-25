package service

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cacheTraceBillingForUsage(t *testing.T, usage OpenAIUsage) any {
	t.Helper()
	rows := &openAIRecordUsageLogRepoStub{inserted: true}
	billing := &openAIRecordUsageBillingRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(rows, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	input := &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "isolation-corpus", Model: "gpt-5.4", Usage: usage, Duration: time.Second},
		APIKey: &APIKey{ID: 1000, Group: &Group{RateMultiplier: 1}}, User: &User{ID: 2000}, Account: &Account{ID: 3000, Type: AccountTypeAPIKey},
		PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	require.Equal(t, 1, billing.calls)
	require.Equal(t, 1, rows.calls)
	if rows.lastLog != nil {
		rows.lastLog.CreatedAt = time.Time{}
	}
	if billing.lastCmd != nil && billing.lastCmd.Usage != nil {
		billing.lastCmd.Usage.CreatedAt = time.Time{}
	}
	return cacheBillingJSON(t, map[string]any{"command": billing.lastCmd, "usage_log": rows.lastLog, "billing_calls": billing.calls, "row_calls": rows.calls})
}

func TestCacheTraceBufferedObservationFailureIsolation(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	svc := &OpenAIGatewayService{}
	for _, tc := range cacheBillingCorpus(false) {
		t.Run(tc.name, func(t *testing.T) {
			baseline := svc.parseSSEUsageFromBody(tc.wire)
			expectedBill := cacheTraceBillingForUsage(t, *baseline)
			frames := 0
			forEachOpenAISSEFrame(tc.wire, func(string, []byte) { frames++ })
			require.Positive(t, frames)
			for _, at := range []int{1, max(1, frames/2), frames} {
				for _, failure := range []string{"panic", "error"} {
					calls := 0
					got, complete := svc.parseSSEUsageObserving(tc.wire, func([]byte) error {
						calls++
						if calls == at {
							if failure == "panic" {
								panic("private-observer-panic-marker")
							}
							return errors.New("private-observer-error-marker")
						}
						return nil
					})
					require.False(t, complete)
					require.Equal(t, at, calls, "no callbacks after failure, including fallback reparse")
					require.Equal(t, baseline, got, "all usage fields must survive observer failure")
					require.Equal(t, expectedBill, cacheTraceBillingForUsage(t, *got), "billing commits once with identical values")
				}
			}
		})
	}
	for _, marker := range []string{"private-observer-panic-marker", "private-observer-error-marker", "private-output-marker"} {
		require.NotContains(t, logs.String(), marker)
	}
}

func TestCacheTraceBufferedRecoveryIsPerRequest(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "cache_trace.go", nil, 0)
	require.NoError(t, err)
	found := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "parseSSEUsageObserving" {
			continue
		}
		found = true
		defers := 0
		ast.Inspect(fn, func(n ast.Node) bool {
			if _, ok := n.(*ast.DeferStmt); ok {
				defers++
			}
			return true
		})
		require.Equal(t, 1, defers, "one request-level recover, no per-frame defer")
	}
	require.True(t, found)
}

func TestCacheTraceIncompleteUsageNeverLooksLikeMissingUpstreamUsage(t *testing.T) {
	s, c, _, _ := cacheTraceWireFixture(t, "responses", "apikey", "private-pck", "session_id", traceTestConfig)
	tr := cacheTraceFromGin(c)
	tr.usageObservationFailed = true
	result := &OpenAIForwardResult{}
	s.finishCacheTrace(c, result)
	require.Nil(t, result.CacheTrace)
	resetCacheTraceAttempt(c)
	require.False(t, tr.usageObservationFailed)
	// The error reason is never part of any public response field.
	require.False(t, strings.Contains(c.Writer.Header().Get("Cache-Trace"), "private"))
}
