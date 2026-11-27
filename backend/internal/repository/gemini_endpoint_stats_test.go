package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"github.com/stretchr/testify/require"
)

func TestGeminiEndpointStatsAndExportKeepEveryBucket(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	endpoints := []string{"/v1beta/models", "/v1beta/models/{model}", "/v1beta/models:generateContent", "/v1beta/models:streamGenerateContent", "/v1beta/models:countTokens"}
	rows := sqlmock.NewRows([]string{"inbound_grouped", "upstream_grouped", "inbound_endpoint", "upstream_endpoint", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "cost", "actual_cost", "account_cost", "avg_duration_ms", "success", "rescued", "failed", "p50", "samples", "cache_ratio"})
	rows.AddRow(1, 1, nil, nil, 5, 10, 5, 0, 0, 1.25, .625, .5, 10, 5, 0, 0, nil, 0, nil)
	for _, endpoint := range endpoints {
		rows.AddRow(0, 1, endpoint, nil, 1, 2, 1, 0, 0, .25, .125, .1, 10, 1, 0, 0, nil, 0, nil)
		when, err := json.Marshal(start.Format(time.RFC3339))
		require.NoError(t, err)
		label, err := json.Marshal(endpoint)
		require.NoError(t, err)
		for _, format := range []string{"csv", "xlsx"} {
			row, err := usageexport.Project(usageexport.Record{"created_at": when, "inbound_endpoint": label}, usageexport.Options{Format: format, Timezone: "UTC", Version: 1})
			require.NoError(t, err)
			index := 4
			if format == "xlsx" {
				index = 11
			}
			require.Equal(t, endpoint, row[index])
		}
	}
	mock.ExpectQuery("GROUP BY GROUPING SETS").WithArgs(start, end).WillReturnRows(rows)
	stats, err := repo.AdminRequestStats(context.Background(), usagestats.UsageLogFilters{StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	var names []string
	var requests, tokens int64
	var cost float64
	for _, bucket := range stats.Endpoints {
		names = append(names, bucket.Endpoint)
		requests += bucket.Requests
		tokens += bucket.TotalTokens
		cost += bucket.ActualCost
	}
	require.ElementsMatch(t, endpoints, names)
	require.Equal(t, stats.TotalRequests, requests)
	require.Equal(t, stats.TotalTokens, tokens)
	require.Equal(t, stats.TotalActualCost, cost)
	require.NoError(t, mock.ExpectationsWereMet())
}
