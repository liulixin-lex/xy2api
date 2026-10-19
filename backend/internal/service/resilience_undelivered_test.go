//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// disconnectedState returns a request context whose client already left
// with the given delivered byte count.
func disconnectedState(t *testing.T, delivered int64, apiKeyID int64) context.Context {
	t.Helper()
	client, cancel := context.WithCancel(context.Background())
	st := resilience.NewRequestState()
	st.InitDelivery(client, time.Now().Add(-2*time.Second), func() int64 { return delivered })
	st.SetAPIKey(apiKeyID)
	st.SetStage(resilience.StageHeaderWait)
	cancel()
	st.ResolveDisconnect()
	return resilience.WithRequestState(context.Background(), st)
}

func TestUndeliveredRequestIsNotBilled(t *testing.T) {
	withResilienceSettings(t, nil)
	ctx := disconnectedState(t, 0, 7)
	usageLog := &UsageLog{Model: "m", InputTokens: 1000, OutputTokens: 10, TotalCost: 0.02, ActualCost: 0.03}
	cost := &CostBreakdown{TotalCost: 0.02, ActualCost: 0.03}
	require.True(t, resilienceUndeliveredUsage(ctx, usageLog, cost, 0.5), "no usage_logs row, no charge")
	usageJSON, upstreamCost, billed := resilience.StateFrom(ctx).UndeliveredUsage()
	require.False(t, billed)
	require.InDelta(t, 0.01, upstreamCost, 1e-12)
	require.Equal(t, int64(1000), gjson.GetBytes(usageJSON, "input_tokens").Int())

	// Partial delivery and connected requests bill normally.
	require.False(t, resilienceUndeliveredUsage(disconnectedState(t, 5, 7), &UsageLog{}, &CostBreakdown{}, 1))
	require.False(t, resilienceUndeliveredUsage(context.Background(), &UsageLog{}, &CostBreakdown{}, 1))

	// Switch off: billed as before.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Disconnect.Enabled = false })
	require.False(t, resilienceUndeliveredUsage(disconnectedState(t, 0, 7), &UsageLog{}, &CostBreakdown{}, 1))
}

func TestUndeliveredAbuseBillsUpstreamCost(t *testing.T) {
	// Spec: the 31st undelivered disconnect of a key in an hour is billed at
	// upstream cost and visible to the user.
	withResilienceSettings(t, nil)
	r := NewResilienceService(nil, newMemResilienceStore())
	r.ConfigureUndelivered(nil)
	t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
	var last context.Context
	for i := 0; i < 31; i++ {
		last = disconnectedState(t, 0, 99)
	}
	_, _, abuse := resilience.StateFrom(last).Disconnect()
	require.True(t, abuse)

	usageLog := &UsageLog{Model: "m", TotalCost: 0.02, ActualCost: 0.03}
	cost := &CostBreakdown{TotalCost: 0.02, ActualCost: 0.03}
	stats := 0.012
	usageLog.AccountStatsCost = &stats
	require.False(t, resilienceUndeliveredUsage(last, usageLog, cost, 0.5), "billed")
	require.InDelta(t, 0.012, usageLog.ActualCost, 1e-12, "account stats cost is the upstream cost")
	require.InDelta(t, 0.012, cost.ActualCost, 1e-12)
	require.Equal(t, UsageBillingModeUndeliveredAbuse, *usageLog.BillingMode)
	_, _, billed := resilience.StateFrom(last).UndeliveredUsage()
	require.True(t, billed)

	// Another key is unaffected.
	_, _, abuse = resilience.StateFrom(disconnectedState(t, 0, 100)).Disconnect()
	require.False(t, abuse)
}

func TestDeliveryWriterExcludesHeartbeats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	st := resilience.NewRequestState()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
	InstallDeliveryTracking(c, time.Now())

	stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 0, 5*time.Millisecond)
	require.Eventually(t, func() bool { return PreOutputKeepaliveCommitted(c) }, time.Second, 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	require.Zero(t, st.DeliveredBytes(), "heartbeats are not delivery")
	recordOpenAIStreamKeepaliveBytes(c, 0)
	created := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	_, _ = c.Writer.WriteString(created)
	stop()
	require.Zero(t, st.DeliveredBytes(), "an opening event alone is no delivery (review R03)")
	delta := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
	_, _ = c.Writer.WriteString(delta)
	require.Equal(t, int64(len(created)+len(delta)), st.DeliveredBytes())

	// In-stream keepalive bytes are excluded too.
	_, _ = c.Writer.WriteString(": ping\n\n")
	recordOpenAIStreamKeepaliveBytes(c, len(": ping\n\n"))
	require.Equal(t, int64(len(created)+len(delta)), st.DeliveredBytes())
}

type memUndeliveredRepo struct {
	mu   sync.Mutex
	rows []UndeliveredRequest
}

func (m *memUndeliveredRepo) InsertUndelivered(_ context.Context, rec *UndeliveredRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, *rec)
	return nil
}

func (m *memUndeliveredRepo) ListUndelivered(context.Context, UndeliveredFilter, pagination.PaginationParams) ([]UndeliveredRequest, *pagination.PaginationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]UndeliveredRequest(nil), m.rows...), &pagination.PaginationResult{Total: int64(len(m.rows))}, nil
}

func (m *memUndeliveredRepo) DeleteUndeliveredBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (m *memUndeliveredRepo) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func TestRecordUndeliveredRequest(t *testing.T) {
	withResilienceSettings(t, nil)
	repo := &memUndeliveredRepo{}
	r := NewResilienceService(nil, newMemResilienceStore())
	r.ConfigureUndelivered(repo)
	t.Cleanup(func() {
		resilience.SetUndeliveredAbuseCheck(nil)
		undeliveredRecorder.Store(nil)
	})
	RecordUndeliveredRequest(&UndeliveredRequest{ClientRequestID: "c1", DisconnectStage: "header_wait"})
	require.Eventually(t, func() bool { return repo.count() == 1 }, time.Second, 5*time.Millisecond)
	items, page, err := r.ListUndelivered(context.Background(), UndeliveredFilter{}, pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "c1", items[0].ClientRequestID)

	// Without a configured service nothing happens.
	undeliveredRecorder.Store(nil)
	RecordUndeliveredRequest(&UndeliveredRequest{})
}

func TestEdgeRequestsGet503InsteadOf502(t *testing.T) {
	// Staging CF-3: Cloudflare replaces an origin 502/504 with its own plain
	// page; a 503 keeps the JSON error and Retry-After.
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		edge   string
		status int
		want   int
	}{
		{"cf", http.StatusBadGateway, http.StatusServiceUnavailable},
		{"esa", http.StatusGatewayTimeout, http.StatusServiceUnavailable},
		{"", http.StatusBadGateway, http.StatusBadGateway},
		{"cf", http.StatusInternalServerError, http.StatusInternalServerError},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		st := resilience.NewRequestState()
		st.SetEdge(tc.edge)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), st))
		InstallDeliveryTracking(c, time.Now())
		c.JSON(tc.status, gin.H{"error": gin.H{"type": "upstream_error", "message": "Upstream service temporarily unavailable"}})
		require.Equal(t, tc.want, rec.Code, "edge=%q status=%d", tc.edge, tc.status)
		if tc.want != tc.status {
			require.Equal(t, strconv.Itoa(tc.status), rec.Header().Get("X-XY-Upstream-Status"))
			require.Contains(t, rec.Body.String(), "upstream_error")
		}
	}
}

type flakyUndeliveredRepo struct {
	memUndeliveredRepo
	fails   int
	block   chan struct{}
	entered chan struct{}
}

func (r *flakyUndeliveredRepo) InsertUndelivered(ctx context.Context, rec *UndeliveredRequest) error {
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	if r.fails > 0 {
		r.fails--
		r.mu.Unlock()
		return context.DeadlineExceeded
	}
	r.mu.Unlock()
	return r.memUndeliveredRepo.InsertUndelivered(ctx, rec)
}

func TestUndeliveredRecordRetriesOnce(t *testing.T) {
	repo := &flakyUndeliveredRepo{fails: 1}
	r := NewResilienceService(nil, nil)
	r.undelivered = repo
	r.RecordUndelivered(&UndeliveredRequest{RequestID: "req-retry"})
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.rows) == 1
	}, 3*time.Second, 20*time.Millisecond, "a short database failure is retried")
}

func TestUndeliveredRecordWritersAreBounded(t *testing.T) {
	repo := &flakyUndeliveredRepo{block: make(chan struct{}), entered: make(chan struct{}, undeliveredWriters)}
	r := NewResilienceService(nil, nil)
	r.undelivered = repo
	for i := 0; i < undeliveredWriters; i++ {
		r.RecordUndelivered(&UndeliveredRequest{RequestID: "req-busy"})
	}
	for i := 0; i < undeliveredWriters; i++ {
		<-repo.entered
	}
	r.RecordUndelivered(&UndeliveredRequest{RequestID: "req-dropped"})
	require.Len(t, undeliveredSlots, undeliveredWriters, "no writer beyond the bound")
	close(repo.block)
	require.Eventually(t, func() bool { return len(undeliveredSlots) == 0 }, 3*time.Second, 10*time.Millisecond)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.rows, undeliveredWriters, "the record that found every writer busy was dropped")
}
