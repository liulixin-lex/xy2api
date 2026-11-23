//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabWiringAffinityAdmissionAndSuccess(t *testing.T) {
	quietReplayLogs(t)
	settings := resilience.Defaults()
	settings.Collab.TreeAffinity = true
	var fixture *collabWiringFixture
	var scope resilience.CollabRecordScope
	fixture = newCollabWiringFixture(t, settings, func(req *http.Request, account int64) (*http.Response, error) {
		state := resilience.StateFrom(req.Context()).CollabState()
		require.NotEmpty(t, state.Items)
		scope = state.Items[0].Key.Scope
		hint, ok := fixture.gateway.CollabTreeAffinity().LookupTreeHint(req.Context(), scope)
		require.True(t, ok, "root admission must publish the hint before upstream can return an executable call")
		require.Equal(t, account, hint.AccountID)
		require.Equal(t, resilience.CollabTreeHintStatusProvisional, hint.Status)
		return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":1}}`), nil
	})
	rec, _ := fixture.request([]byte(`{"model":"gpt-5.5","input":[` + replayTask + `,` + replayQuestion + `]}`))
	require.Equal(t, http.StatusOK, rec.Code)
	hint, ok := fixture.gateway.CollabTreeAffinity().LookupTreeHint(context.Background(), scope)
	require.True(t, ok)
	require.Equal(t, resilience.CollabTreeHintStatusConfirmed, hint.Status)
}

func TestCollabWiringLeaseRequiresLosslessFirst(t *testing.T) {
	quietReplayLogs(t)
	for _, lossless := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy_first", true: "lossless_first"}[lossless], func(t *testing.T) {
			settings := resilience.Defaults()
			settings.Collab.TreeAffinity, settings.Collab.Ladder, settings.Collab.LosslessFirst = true, true, lossless
			const call = `{"type":"function_call","id":"item_sample","call_id":"call_sample","namespace":"collaboration","name":"send_message","arguments":"{\"message\":\"cipher-task\"}","encrypted_function_args":["message"]}`
			first := true
			fixture := newCollabWiringFixture(t, settings, func(*http.Request, int64) (*http.Response, error) {
				if first {
					first = false
					return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[`+call+`],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
				}
				return replayResponse(200, "application/json", `{"id":"resp_next","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
			})
			_, state := fixture.request([]byte(`{"model":"gpt-5.5","input":[` + replayTask + `,` + replayQuestion + `]}`))
			scope := state.CollabState().Items[0].Key.Scope
			require.Equal(t, lossless, fixture.gateway.CollabTreeAffinity().HasDeliveryLeases(context.Background(), scope))
			fixture.request([]byte(`{"model":"gpt-5.5","input":[` + call + `,` + replayTask + `,` + replayQuestion + `]}`))
			require.False(t, fixture.gateway.CollabTreeAffinity().HasDeliveryLeases(context.Background(), scope))
		})
	}
}
