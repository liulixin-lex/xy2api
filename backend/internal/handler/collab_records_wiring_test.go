//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabWiringRecords(t *testing.T) {
	quietReplayLogs(t)
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "release_default"}[enabled], func(t *testing.T) {
			settings := resilience.Defaults()
			settings.Collab.Records = enabled
			const output = `{"id":"resp_sample","status":"completed","output":[{"id":"item_sample","call_id":"call_sample","type":"function_call","namespace":"collaboration","name":"send_message","arguments":"{\"message\":\"cipher-task\"}","encrypted_function_args":["message"]}],"usage":{"input_tokens":2,"output_tokens":1}}`
			fixture := newCollabWiringFixture(t, settings, func(*http.Request, int64) (*http.Response, error) {
				return replayResponse(200, "application/json", output), nil
			})
			rec, state := fixture.request([]byte(`{"model":"gpt-5.5","input":[` + replayTask + `,` + replayQuestion + `]}`))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, fixture.upstream.sends)
			require.Contains(t, rec.Body.String(), `"encrypted_function_args":["message"]`)
			collab := state.CollabState()
			require.NotEmpty(t, collab.Items, "ingress delivery identities must reach records")
			key := collab.Items[0].Key
			query := resilience.CollabRecordQuery{Key: key, Call: fixture.gateway.CollabDigest(resilience.CollabDigestDomainCall, "call_sample")}
			result := fixture.gateway.CollabRecords().LookupBatch(context.Background(), resilience.CollabRecordBatch{Scope: key.Scope, Queries: []resilience.CollabRecordQuery{query}, Records: true})
			if !enabled {
				require.Empty(t, result.Records[query].Issued)
				require.Empty(t, result.Records[query].Consumed)
				return
			}
			require.Len(t, result.Records[query].Issued, 1)
			require.Equal(t, resilience.CollabIssuedStatusConfirmed, result.Records[query].Issued[0].Status)
			require.Len(t, result.Records[query].Consumed, 1)
		})
	}
}
