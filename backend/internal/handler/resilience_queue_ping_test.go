//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Pings written while a streaming request waits for a slot or the user
// message queue are no output. A client that leaves after them, before the
// upstream answered, received nothing: the request is undelivered (design §8),
// not partly delivered and billed.

func trackedQueueContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *resilience.RequestState, context.CancelFunc) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	client, cancel := context.WithCancel(context.Background())
	st := resilience.NewRequestState()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(resilience.WithRequestState(client, st))
	service.InstallDeliveryTracking(c, time.Now())
	return c, rec, st, cancel
}

func TestAccountSlotPingsAreNotDelivery(t *testing.T) {
	c, rec, st, disconnect := trackedQueueContext(t)
	cache := &helperConcurrencyCacheStub{accountSeq: []bool{false, true}}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatComment, 5*time.Millisecond)

	streamStarted := false
	release, err := helper.waitForSlotWithPingTimeout(c, "account", 101, 1, time.Second, true, &streamStarted, true)
	require.NoError(t, err)
	defer release()
	require.True(t, streamStarted)
	require.Contains(t, rec.Body.String(), ":\n\n", "pings were written while waiting")

	disconnect()
	gone, delivered, _ := st.ResolveDisconnect()
	require.True(t, gone)
	require.False(t, delivered, "only queue pings reached the client")
	require.True(t, gatewayStreamHasOnlyHeartbeats(c))
}

type queuePingLockStub struct {
	mu  sync.Mutex
	seq []bool
}

func (s *queuePingLockStub) AcquireLock(context.Context, int64, string, int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seq) == 0 {
		return true, nil
	}
	v := s.seq[0]
	s.seq = s.seq[1:]
	return v, nil
}
func (s *queuePingLockStub) ReleaseLock(context.Context, int64, string) (bool, error) {
	return true, nil
}
func (s *queuePingLockStub) GetLastCompletedMs(context.Context, int64) (int64, error) { return 0, nil }
func (s *queuePingLockStub) GetCurrentTimeMs(context.Context) (int64, error) {
	return time.Now().UnixMilli(), nil
}
func (s *queuePingLockStub) ReconcileExpiredLockCandidates(context.Context, int) (int, error) {
	return 0, nil
}

func TestUserMessageQueuePingsAreNotDelivery(t *testing.T) {
	c, rec, st, disconnect := trackedQueueContext(t)
	queue := service.NewUserMessageQueueService(&queuePingLockStub{seq: []bool{false, false, true}}, nil, &config.UserMessageQueueConfig{})
	helper := NewUserMsgQueueHelper(queue, SSEPingFormatClaude, 5*time.Millisecond)

	streamStarted := false
	release, err := helper.AcquireWithWait(c, 7, 0, true, &streamStarted, time.Second, zap.NewNop())
	require.NoError(t, err)
	defer release()
	require.True(t, streamStarted)
	require.Contains(t, rec.Body.String(), "ping")

	disconnect()
	gone, delivered, _ := st.ResolveDisconnect()
	require.True(t, gone)
	require.False(t, delivered, "only queue pings reached the client")
}

func TestUserMessageQueueUsesInboundPingProtocol(t *testing.T) {
	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions} {
		t.Run(endpoint, func(t *testing.T) {
			c, rec, _, cancel := trackedQueueContext(t)
			defer cancel()
			c.Request.URL.Path = endpoint
			queue := service.NewUserMessageQueueService(&queuePingLockStub{seq: []bool{false, true}}, nil, &config.UserMessageQueueConfig{})
			helper := NewUserMsgQueueHelper(queue, SSEPingFormatClaude, 5*time.Millisecond)
			started := false
			release, err := helper.AcquireWithWait(c, 7, 0, true, &started, time.Second, zap.NewNop())
			require.NoError(t, err)
			defer release()
			require.True(t, started)
			require.Empty(t, parseSSEEvents(rec.Body.String()))
			require.Contains(t, rec.Body.String(), string(SSEPingFormatComment))
		})
	}
}
