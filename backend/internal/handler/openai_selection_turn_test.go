//go:build unit

package handler

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type selectionTurnLeases struct {
	mu                          sync.Mutex
	acquired, touched, released []string
}

func (l *selectionTurnLeases) Room(context.Context, int64, string, int64, string, time.Time) (bool, error) {
	return true, nil
}
func (l *selectionTurnLeases) PeekRoom(context.Context, int64, string) (bool, error) {
	return true, nil
}
func (l *selectionTurnLeases) LastFull(context.Context, int64, string, int64) (time.Time, error) {
	return time.Time{}, nil
}
func (l *selectionTurnLeases) Acquire(_ context.Context, _ int64, m string, _ int64, s string, _ time.Time) (bool, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquired = append(l.acquired, m+"/"+s)
	return true, "test-generation", nil
}
func (l *selectionTurnLeases) Touch(_ context.Context, _ int64, m string, _ int64, s string, _ time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.touched = append(l.touched, m+"/"+s)
	return nil
}
func (l *selectionTurnLeases) Release(_ context.Context, _ int64, m string, _ int64, s string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, m+"/"+s)
	return nil
}
func (l *selectionTurnLeases) Promote(context.Context, int64, string, int64, string, string, string, time.Time) error {
	return nil
}
func (l *selectionTurnLeases) ReleaseGroup(context.Context, int64, string, int64) error { return nil }

type selectionTurnTrialView struct{ wsPinnedQualityView }

func (selectionTurnTrialView) StateOf(_ int64, _ int64, m string) q.StateView {
	if m == "upstream" {
		return q.StateView{TTFO: q.LineView{Trial: true}}
	}
	return q.StateView{}
}

// A retained unavailable owner makes both connection turns borrow account9901.
type selectionTurnOwnerCache struct{ *wsQualityPinnedGatewayCache }

func (c *selectionTurnOwnerCache) GetSessionAccountID(ctx context.Context, g int64, key string) (int64, error) {
	if strings.HasPrefix(key, "openai:") {
		return 9902, nil
	}
	return c.wsQualityPinnedGatewayCache.GetSessionAccountID(ctx, g, key)
}

func TestSelectionWebSocketTurnsLeaseResolvedModel(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		for _, borrow := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/owner", true: "/borrow"}[borrow], func(t *testing.T) {
				leases := &selectionTurnLeases{}
				var cache service.GatewayCache = &wsQualityPinnedGatewayCache{bindings: map[string]int64{}}
				if borrow {
					cache = &selectionTurnOwnerCache{&wsQualityPinnedGatewayCache{bindings: map[string]int64{}}}
				}
				got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
					firstPayload:  `{"type":"response.create","model":"public-alias","input":"one"}`,
					secondPayload: `{"type":"response.create","model":"public-alias","input":"two"}`,
					ingressMode:   mode, channelMapping: map[string]string{"public-alias": "channel-alias"}, accountModelMapping: map[string]any{"channel-alias": "upstream"},
					qualityView: selectionTurnTrialView{}, trialLeases: leases, gatewayCache: cache, sessionID: "synthetic-turn-session",
				})
				require.Len(t, got.upstreamPayloads, 2)
				leases.mu.Lock()
				defer leases.mu.Unlock()
				if borrow {
					require.Len(t, leases.acquired, 2)
					require.NotEqual(t, leases.acquired[0], leases.acquired[1])
					require.ElementsMatch(t, leases.acquired, leases.released)
					for _, member := range leases.acquired {
						require.True(t, strings.HasPrefix(member, "upstream/r:"))
					}
				} else {
					require.NotEmpty(t, leases.touched)
					for _, member := range leases.touched {
						require.True(t, strings.HasPrefix(member, "upstream/s:"))
					}
				}
			})
		}
	}
}
