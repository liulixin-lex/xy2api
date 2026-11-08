package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSlotWaitPreservesRequestHeartbeatAcrossAttempts(t *testing.T) {
	for _, tc := range []struct {
		name, path, protocol string
		format               SSEPingFormat
		frame, queueFrame    string
	}{
		{"responses", "/responses", service.PreOutputProtocolResponses, SSEPingFormatComment, ":\n\n", ":\n\n"},
		{"chat", "/chat/completions", service.PreOutputProtocolChat, SSEPingFormatComment, ":\n\n", ":\n\n"},
		{"messages", "/messages", service.PreOutputProtocolAnthropic, SSEPingFormatClaude, "event: ping\ndata: {\"type\":\"ping\"}\n\n", string(SSEPingFormatClaude)},
	} {
		for _, suspended := range []bool{false, true} {
			name := tc.name + "/active"
			if suspended {
				name = tc.name + "/suspended"
			}
			t.Run(name, func(t *testing.T) {
				c, rec := newHelperTestContext("POST", tc.path)
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
				service.InstallDeliveryTracking(c, time.Now())
				stop := service.StartPreOutputKeepalive(c, tc.protocol, 0, 20*time.Millisecond)
				defer stop()
				require.Eventually(t, func() bool { return service.PreOutputKeepaliveCommitted(c) }, time.Second, time.Millisecond)
				if suspended {
					c.Header("X-Attempt", "first")
				}
				beforeWait := c.Writer.Size()
				cache := &helperConcurrencyCacheStub{accountSeq: []bool{false, true}}
				helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), tc.format, 10*time.Millisecond)
				started := true
				var slotID int64
				release, err := helper.AcquireAccountSlotWithWaitTimeout(c, slotID, 1, time.Second, true, &started)
				require.NoError(t, err)
				require.NotNil(t, release)
				release()
				before := c.Writer.Size()
				require.Greater(t, before, beforeWait)
				service.StartPreOutputKeepalive(c, tc.protocol, 0, 20*time.Millisecond)
				require.Eventually(t, func() bool { return c.Writer.Size() >= before+2*len(tc.frame) }, time.Second, 5*time.Millisecond)
				stop()
				body := rec.Body.String()
				for len(body) > 0 {
					switch {
					case strings.HasPrefix(body, tc.frame):
						body = strings.TrimPrefix(body, tc.frame)
					case strings.HasPrefix(body, tc.queueFrame):
						body = strings.TrimPrefix(body, tc.queueFrame)
					default:
						t.Fatalf("invalid or interleaved heartbeat frame: %q", body)
					}
				}
				require.Equal(t, -1, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
				only, tracked := service.DeliveryOnlyHeartbeats(c)
				require.True(t, tracked)
				require.True(t, only)
			})
		}
	}
}

func TestSlotWaitHeartbeatWithoutDeliveryTracking(t *testing.T) {
	c, _ := newHelperTestContext("POST", "/responses")
	stop := service.StartPreOutputKeepalive(c, service.PreOutputProtocolResponses, 0, 20*time.Millisecond)
	defer stop()
	require.Eventually(t, func() bool { return service.PreOutputKeepaliveCommitted(c) }, time.Second, time.Millisecond)
	c.Header("X-Attempt", "first")
	cache := &helperConcurrencyCacheStub{accountSeq: []bool{false, true}}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatComment, 10*time.Millisecond)
	started := true
	var slotID int64
	release, err := helper.AcquireAccountSlotWithWaitTimeout(c, slotID, 1, time.Second, true, &started)
	require.NoError(t, err)
	release()
	require.True(t, gatewayStreamHasOnlyHeartbeats(c))
	_, err = c.Writer.Write([]byte("data: content\n\n"))
	require.NoError(t, err)
	require.False(t, gatewayStreamHasOnlyHeartbeats(c))
}
