//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptedContentAmbiguousRejectionContract(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"cipher-bad"},{"type":"reasoning","id":"rs_good","encrypted_content":"cipher-good"}]}`)
	account := &Account{Credentials: map[string]any{"api_key": "fixture-credential-secret"}}
	for _, tc := range []struct {
		name, payload, param string
		unique               bool
	}{
		{"missing", `{"error":{"code":"invalid_encrypted_content","message":"Could not verify encrypted content"}}`, "", false},
		{"conflicting", `{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content","message":"Could not verify for item rs_good"}}`, "input[0].encrypted_content", false},
		{"located", `{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content","message":"Could not verify for item rs_bad"}}`, "input[0].encrypted_content", true},
		{"item_only", `{"error":{"code":"invalid_encrypted_content","message":"Could not verify for item rs_bad"}}`, "", true},
		{"redaction", `{"error":{"code":"invalid_encrypted_content","message":"Could not verify cipher-bad with fixture-credential-secret"},"debug":"arbitrary response body"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newOpenAIEncryptedContentRejection(context.Background(), account, body, []byte(tc.payload))
			require.Equal(t, tc.param, err.Param)
			require.Contains(t, err.Message, "Upstream detail:")
			if tc.unique {
				require.Contains(t, err.Message, "Remove only the identified encrypted item")
			} else {
				require.Contains(t, err.Message, "Rebuild the request from complete readable history")
				require.NotContains(t, err.Message, "Remove only")
			}
			require.NotContains(t, err.Message, "cipher-bad")
			require.NotContains(t, err.Message, "fixture-credential-secret")
			require.NotContains(t, err.Message, "arbitrary response body")
			if tc.name == "conflicting" {
				require.Contains(t, err.Message, "rs_good")
				require.Contains(t, err.Message, "input[0].encrypted_content")
			}
		})
	}
	for _, status := range []int{400, 502} {
		for _, mode := range []string{"ambiguous", "conflicting", "budget", "twice"} {
			t.Run(fmt.Sprintf("native_%d_%s", status, mode), func(t *testing.T) {
				withResilienceSettings(t, func(settings *resilience.Settings) {
					if mode == "budget" {
						settings.Budget.MaxAttempts = 1
					}
				})
				req := newWebSearchReplayRequest(t)
				req.c.Header("Retry-After", "9")
				input := []byte(`{"model":"gpt-5.5","session_id":"encrypted-terminal","stream":true,"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"cipher-bad","summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":"cipher-good","summary":[]}]}`)
				detail := `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified"}}`
				param := ""
				if mode != "ambiguous" {
					param = "input[0].encrypted_content"
					id := "rs_bad"
					if mode == "conflicting" {
						id = "rs_good"
					}
					detail = fmt.Sprintf(`{"error":{"code":"invalid_encrypted_content","param":%q,"message":"Encrypted content could not be verified for item %s"}}`, param, id)
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIUpstreamErrorResponse(status, detail)}}
				sends := 1
				if mode == "twice" {
					upstream.responses = append(upstream.responses, newOpenAIUpstreamErrorResponse(status, detail))
					sends = 2
				}
				svc := newOpenAIImageGenerationControlTestService(upstream)
				repo := &openAIModelAvailabilityRepo{}
				svc.accountRepo = repo
				_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), input)
				var rejection *OpenAIRequestRejectionError
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &rejection)
				require.NotErrorAs(t, err, &failover)
				require.Len(t, upstream.bodies, sends)
				require.Equal(t, 400, req.rec.Code)
				require.Equal(t, "encrypted_content_rejected", gjson.GetBytes(req.rec.Body.Bytes(), "error.code").String())
				require.Equal(t, param, gjson.GetBytes(req.rec.Body.Bytes(), "error.param").String())
				require.Empty(t, req.rec.Header().Get("Retry-After"))
				require.Equal(t, "false", req.rec.Header().Get("x-should-retry"))
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.tempCalls)
				require.Empty(t, repo.modelRateLimitCalls)
				require.Empty(t, req.session.failures)
				if mode == "ambiguous" || mode == "conflicting" {
					require.Contains(t, req.rec.Body.String(), "Rebuild the request from complete readable history")
				} else {
					require.Contains(t, req.rec.Body.String(), "Remove only the identified encrypted item")
				}
			})
		}
	}
	for _, protocol := range []string{PreOutputProtocolResponses, PreOutputProtocolChat, PreOutputProtocolAnthropic} {
		t.Run("committed_"+protocol, func(t *testing.T) {
			req := newWebSearchReplayRequest(t)
			req.c.Header("Content-Type", "text/event-stream")
			req.c.Header("Retry-After", "9")
			req.c.Writer.WriteHeader(http.StatusOK)
			req.c.Writer.WriteHeaderNow()
			rejection := newOpenAIEncryptedContentRejection(req.ctx, account, body, []byte(`{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content","message":"for item rs_bad"}}`))
			require.True(t, writeOpenAIEncryptedContentRejection(req.c, protocol, rejection))
			before := req.rec.Body.String()
			require.False(t, writeOpenAIEncryptedContentRejection(req.c, protocol, rejection))
			require.Equal(t, before, req.rec.Body.String())
			require.Equal(t, http.StatusOK, req.rec.Code)
			require.Equal(t, "9", req.rec.Result().Header.Get("Retry-After"))
			require.Contains(t, before, "Remove only the identified encrypted item")
			if protocol == PreOutputProtocolResponses {
				require.Equal(t, 1, strings.Count(before, `"type":"response.failed"`))
			}
			if protocol != PreOutputProtocolAnthropic {
				require.Contains(t, before, `"code":"encrypted_content_rejected"`)
				require.Contains(t, before, `"param":"input[0].encrypted_content"`)
			}
		})
	}

	t.Run("nested_M3_wrapped_rejection_preserves_replay", func(t *testing.T) {
		withResilienceSettings(t, nil)
		req := newWebSearchReplayRequest(t)
		account := webSearchReplayAccount(false)
		svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
		repair := armOpenAIRequestRepair(req.c, account, nil)
		repair.setBody([]byte(m3TwoItemRequest))
		repair.m3 = svc.armOpenAIM3(req.c, account, repair.body)
		payload := []byte(`{"type":"response.failed","response":{"error":{"code":"400","status_code":400,"message":"Encrypted content could not be verified"}}}`)
		err := newOpenAIRequestRepairSignal(req.ctx, req.c, account, payload, "Encrypted content could not be verified", 400)
		var signal *openAIRequestRepairSignal
		require.ErrorAs(t, err, &signal)
		require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(signal.next, "input.0.content.0.text").String())
		require.False(t, req.c.Writer.Written())
	})

}
