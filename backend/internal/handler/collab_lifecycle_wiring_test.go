//go:build unit

package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type collabWiringFaultCache struct {
	service.GatewayCache
	calls  int
	panics bool
}

func (c *collabWiringFaultCache) CollabRecords() resilience.CollabSharedRecordsStore {
	c.calls++
	if c.panics {
		panic("synthetic construction failure")
	}
	return nil
}

func TestCollabWiringConstructionKeepsResponse(t *testing.T) {
	quietReplayLogs(t)
	for _, enabled := range []bool{false, true} {
		for _, panics := range []bool{false, true} {
			t.Run(map[bool]string{false: "off", true: "on"}[enabled]+"/"+map[bool]string{false: "nil", true: "panic"}[panics], func(t *testing.T) {
				memory := &replayMemory{bindings: map[string]int64{}, avoids: map[string]map[int64]time.Time{}, avoidUntil: map[string]map[int64]time.Time{}, failures: map[string]map[int64]int64{}}
				cache := &collabWiringFaultCache{GatewayCache: memory, panics: panics}
				settings := resilience.Defaults()
				settings.Collab.Records = enabled
				settings.Collab.Ladder = enabled
				const output = `{"id":"resp_sample","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`
				f := newCollabWiringFixture(t, settings, func(*http.Request, int64) (*http.Response, error) {
					return replayResponse(200, "application/json", output), nil
				}, cache)
				for i := 0; i < 3; i++ {
					rec, state := f.request([]byte(`{"model":"gpt-5.5","input":[` + replayTask + `]}`))
					require.Equal(t, 200, rec.Code)
					require.JSONEq(t, output, rec.Body.String())
					metadata, _ := state.ClaimGatewayRequest(false)
					require.Equal(t, resilience.RequestSuccess, metadata.Result)
				}
				if enabled {
					require.Equal(t, 1, cache.calls)
				} else {
					require.Zero(t, cache.calls)
				}
				require.Len(t, f.usage.created, 3)
				require.Empty(t, f.usage.unbilled)
			})
		}
	}
}
