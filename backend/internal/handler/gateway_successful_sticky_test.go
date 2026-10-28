//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type successfulStickyValueKey struct{}

type cancelOnSuccessWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *cancelOnSuccessWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(body)
	w.cancel()
	return n, err
}

type successfulStickyCache struct {
	service.GatewayCache
	t              *testing.T
	detachedWrites int
}

func (*successfulStickyCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, service.ErrStickySessionNotFound
}
func (c *successfulStickyCache) SetSessionAccountID(ctx context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		c.detachedWrites++
		require.Equal(c.t, "retained", ctx.Value(successfulStickyValueKey{}))
		require.Equal(c.t, "claude-sonnet-4-5", resilience.StateFrom(ctx).Model())
	}
	return nil
}

func TestGatewaySuccessfulStickyBindingSurvivesClientCancellation(t *testing.T) {
	withHandlerResilience(t, nil)
	const groupID = int64(9200)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	account := &service.Account{ID: 9201, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"anthropic_passthrough": true}, AccountGroups: []service.AccountGroup{{GroupID: groupID, AccountID: 9201}}}
	cache := &successfulStickyCache{t: t}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	gw := service.NewGatewayService(nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, cache, cfg, snapshot, nil, nil, nil, nil, nil, sessionAnchorHandlerUpstream{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := &GatewayHandler{gatewayService: gw, billingCacheService: billing, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0), cfg: cfg}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), successfulStickyValueKey{}, "retained"))
	defer cancel()
	writer := &cancelOnSuccessWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c := newGatewayConvertUsageContext(writer, "/v1/messages", `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":"stable-session"}}`)
	c.Request = c.Request.WithContext(resilience.WithRequestState(parent, resilience.NewRequestState()))
	h.Messages(c)
	require.Equal(t, 200, writer.Code)
	require.ErrorIs(t, parent.Err(), context.Canceled)
	require.Equal(t, 1, cache.detachedWrites, "the successful refresh must finish synchronously with values and a deadline")
}
