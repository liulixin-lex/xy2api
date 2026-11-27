//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGeminiEndpointStatsPreserveLegacyBucket(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	u := mustCreateUser(t, client, &service.User{Email: "records@example.test"})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, Key: uuid.NewString(), Name: "records"})
	a := mustCreateAccount(t, client, &service.Account{Name: "records"})
	at := time.Now().UTC()
	start, end := at.Add(-time.Minute), at.Add(time.Minute)
	endpoints := []string{"/v1beta/models", "/v1beta/models/{model}", "/v1beta/models:generateContent", "/v1beta/models:streamGenerateContent", "/v1beta/models:countTokens"}
	for _, endpoint := range endpoints {
		_, err := repo.Create(ctx, &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID,
			RequestID: uuid.NewString(), Model: "gemini-fixture", CreatedAt: at, InputTokens: 2, OutputTokens: 1,
			TotalCost: .25, ActualCost: .125, InboundEndpoint: &endpoint, UpstreamEndpoint: new("/v1beta/models")})
		require.NoError(t, err)
	}
	filters := usagestats.UsageLogFilters{UserID: u.ID, StartTime: &start, EndTime: &end}
	stats, err := repo.AdminRequestStats(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, len(endpoints), stats.TotalRequests)
	require.InDelta(t, 1.25, stats.TotalCost, 1e-12)
	require.InDelta(t, .625, stats.TotalActualCost, 1e-12)
	var names []string
	var requests int64
	var cost float64
	for _, bucket := range stats.Endpoints {
		names = append(names, bucket.Endpoint)
		requests += bucket.Requests
		cost += bucket.ActualCost
	}
	require.ElementsMatch(t, endpoints, names)
	require.Equal(t, stats.TotalRequests, requests)
	require.InDelta(t, stats.TotalActualCost, cost, 1e-12)
	rows, page, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.EqualValues(t, len(endpoints), page.Total)
	names = nil
	for _, row := range rows {
		require.NotNil(t, row.Log.InboundEndpoint)
		names = append(names, *row.Log.InboundEndpoint)
	}
	require.ElementsMatch(t, endpoints, names)
}
