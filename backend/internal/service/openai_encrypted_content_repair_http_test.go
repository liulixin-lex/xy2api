//go:build unit

package service

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPEncryptedRepairUniqueItemReplay(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusBadGateway} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			body := []byte(`{"model":"gpt-5.5","session_id":"m10-repair","stream":true,"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad-A","content":null,"summary":[{"type":"summary_text","text":"keep summary"}]},{"type":"reasoning","id":"rs_good","encrypted_content":"good-B","summary":[]},{"type":"compaction","id":"cmp_good","encrypted_content":"good-C"},{"role":"user","content":"question"}]}`)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIUpstreamErrorResponse(status, `{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content","message":"Encrypted content could not be verified for item rs_bad"}}`),
				webSearchSuccessStream("resp_repaired"),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), body)
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 2)
			require.Equal(t, http.StatusOK, req.rec.Code)
			next := upstream.bodies[1]
			require.False(t, gjson.GetBytes(next, "input.0.encrypted_content").Exists())
			require.False(t, gjson.GetBytes(next, "input.0.content").Exists())
			require.Equal(t, "rs_bad", gjson.GetBytes(next, "input.0.id").String())
			require.Equal(t, "keep summary", gjson.GetBytes(next, "input.0.summary.0.text").String())
			for _, index := range []int{1, 2, 3} {
				path := fmt.Sprintf("input.%d", index)
				require.JSONEq(t, gjson.GetBytes(upstream.bodies[0], path).Raw, gjson.GetBytes(next, path).Raw)
			}
		})
	}
}

// Red review inputs: one encrypted item whose upstream locator does not map
// to the request is still the only possible culprit.
func TestHTTPEncryptedRepairSoleItemUnmappedLocator(t *testing.T) {
	const named = `{"error":{"message":"The encrypted content for item rs_0db2ab9550c3ef08 could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`
	for _, tc := range []struct {
		name, body, rejection string
		repaired              bool
	}{
		{"single_no_id", `{"model":"gpt-5.5","stream":true,"store":false,"input":[{"type":"reasoning","summary":[],"encrypted_content":"cipher-A"},{"role":"user","content":"q"}]}`, named, true},
		{"idless_compaction_named", `{"model":"gpt-5.5","stream":true,"input":[{"type":"compaction","encrypted_content":"gAAA"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","param":null,"message":"The encrypted content for item cmp_ag_compact_1 could not be verified. Reason: Encrypted content could not be decrypted or parsed."}}`, true},
		{"sole_compaction_unlocated", `{"model":"gpt-5.5","stream":true,"input":[{"id":"cmp_stale","type":"compaction","encrypted_content":"gAAA"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"bad encrypted content"}}`, true},
		// Several items without a usable location remain the accepted 400.
		{"two_no_id", `{"model":"gpt-5.5","stream":true,"store":false,"input":[{"type":"reasoning","summary":[],"encrypted_content":"cipher-A"},{"role":"assistant","content":"a"},{"type":"reasoning","summary":[],"encrypted_content":"cipher-B"},{"role":"user","content":"q"}]}`, named, false},
	} {
		for _, status := range []int{http.StatusBadRequest, http.StatusBadGateway} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				withResilienceSettings(t, nil)
				req := newWebSearchReplayRequest(t)
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					newOpenAIUpstreamErrorResponse(status, tc.rejection),
					webSearchSuccessStream("resp_ok"),
				}}
				svc := newOpenAIImageGenerationControlTestService(upstream)
				_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(tc.body))
				if !tc.repaired {
					require.Error(t, err)
					require.Len(t, upstream.bodies, 1)
					require.Equal(t, http.StatusBadRequest, req.rec.Code)
					require.Equal(t, "encrypted_content_rejected", gjson.GetBytes(req.rec.Body.Bytes(), "error.code").String())
					return
				}
				require.NoError(t, err)
				require.Len(t, upstream.bodies, 2)
				require.Equal(t, http.StatusOK, req.rec.Code)
				require.NotContains(t, string(upstream.bodies[1]), "encrypted_content")
				last := func(body []byte) string {
					items := gjson.GetBytes(body, "input").Array()
					return items[len(items)-1].Raw
				}
				require.Equal(t, last(upstream.bodies[0]), last(upstream.bodies[1]))
			})
		}
	}
}
