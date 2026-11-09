//go:build unit

package handler

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Same event and same available queue bytes on both trees. Unknown observation
// fields are ignored by the baseline, just as when reading newer JSON.
func TestOpsObservationDoesNotChangeQueueAdmission(t *testing.T) {
	var event service.OpsUpstreamErrorEvent
	require.NoError(t, json.Unmarshal([]byte(`{"kind":"first_output_timeout","account_id":7,"upstream_status_code":504,"reason":"planned_first_output_timeout","message":"Upstream produced no output before the deadline","timeout_ms":5000,"timeout_source":"remaining_cap","tier_ms":30000,"planned_timeout_ms":235000,"effective_timeout_ms":5000,"budget_remaining_ms_at_start":5000,"would_budget_cut":true,"attempt_start_offset_ms":235000,"committed_at_start":true,"shadow_eof_remaining_ms":55000,"ttfo_lower_bound_ms":5000}`), &event))
	entry := &service.OpsInsertErrorLogInput{UpstreamErrors: []*service.OpsUpstreamErrorEvent{&event}}
	require.NoError(t, service.SanitizeOpsUpstreamErrorsForQueue(entry))
	stored := *entry.UpstreamErrorsJSON
	size := estimateOpsErrorLogJobBytes(entry)
	beforeBytes, beforeLen := opsErrorLogQueueBytes.Load(), opsErrorLogQueueLen.Load()
	t.Cleanup(func() { opsErrorLogQueueBytes.Store(beforeBytes); opsErrorLogQueueLen.Store(beforeLen) })
	const headroom = 900
	opsErrorLogQueueBytes.Store(opsErrorLogMaxQueueBytes - headroom)
	accepted := reserveOpsErrorLogQueueBytes(size)
	t.Logf("headroom=%d queued_bytes=%d accepted=%t upstream_json_bytes=%d", headroom, size, accepted, len(*entry.UpstreamErrorsJSON))
	require.EqualValues(t, 744, size, "admission must use the legacy event representation")
	require.True(t, accepted, "observe diagnostics must not change admission of the same legacy ops event")
	require.Equal(t, stored, *entry.UpstreamErrorsJSON, "sizing must preserve persisted diagnostics")
	var saved service.OpsUpstreamErrorEvent
	var events []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(stored), &events))
	require.NoError(t, json.Unmarshal(events[0], &saved))
	require.Equal(t, "remaining_cap", saved.TimeoutSource)
	opsErrorLogQueueBytes.Store(opsErrorLogMaxQueueBytes - 744)
	require.True(t, reserveOpsErrorLogQueueBytes(size), "exact legacy boundary fits")
	opsErrorLogQueueBytes.Store(opsErrorLogMaxQueueBytes - 743)
	require.False(t, reserveOpsErrorLogQueueBytes(size), "existing global byte limit is still enforced")
}
