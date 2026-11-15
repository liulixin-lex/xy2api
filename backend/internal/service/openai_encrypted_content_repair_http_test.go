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
