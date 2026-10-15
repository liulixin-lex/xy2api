//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	_, _ = c.Writer.WriteString("event: response.created\ndata: {}\n\n")
	stop()
	require.Equal(t, int64(len("event: response.created\ndata: {}\n\n")), st.DeliveredBytes())

	// In-stream keepalive bytes are excluded too.
	_, _ = c.Writer.WriteString(": ping\n\n")
	recordOpenAIStreamKeepaliveBytes(c, len(": ping\n\n"))
	require.Equal(t, int64(len("event: response.created\ndata: {}\n\n")), st.DeliveredBytes())
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
