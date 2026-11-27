//go:build unit

package service

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
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
		// Several items without a usable location: all are cleared once (E24).
		{"two_no_id", `{"model":"gpt-5.5","stream":true,"store":false,"input":[{"type":"reasoning","summary":[],"encrypted_content":"cipher-A"},{"role":"assistant","content":"a"},{"type":"reasoning","summary":[],"encrypted_content":"cipher-B"},{"role":"user","content":"q"}]}`, named, true},
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

// encryptedForeignUpstream rejects the first undecryptable item and names it,
// as the provider does when a whole history came from another credential.
type encryptedForeignUpstream struct {
	bad    map[string]string // ciphertext -> item id named by the provider
	bodies [][]byte
}

func (u *encryptedForeignUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	RecordOpsUpstreamSendBoundary(req.Context(), accountID)
	body, _ := io.ReadAll(req.Body)
	u.bodies = append(u.bodies, body)
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if id, ok := u.bad[item.Get("encrypted_content").String()]; ok {
			return newOpenAIUpstreamErrorResponse(http.StatusBadRequest, `{"error":{"message":"The encrypted content for item `+id+` could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`), nil
		}
	}
	return webSearchSuccessStream("resp_ok"), nil
}

func (u *encryptedForeignUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

// Red review inputs: when more than one item fails, the request still ends
// in 200 within the existing budget and the next turn sends once.
func TestHTTPEncryptedRepairMultipleRejectedItems(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bad        map[string]string
	}{
		{"three_reasoning", `{"model":"gpt-5.5","stream":true,"store":false,"prompt_cache_key":"red-sess","input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"c1"},{"role":"assistant","content":"a1"},{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"c2"},{"role":"assistant","content":"a2"},{"type":"reasoning","id":"rs_3","summary":[],"encrypted_content":"c3"},{"role":"user","content":"q"}]}`, map[string]string{"c1": "rs_1", "c2": "rs_2", "c3": "rs_3"}},
		{"compaction_plus_reasoning", `{"model":"gpt-5.5","stream":true,"store":false,"prompt_cache_key":"red-sess2","input":[{"type":"compaction","id":"cmp_1","encrypted_content":"c1"},{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"c2"},{"role":"user","content":"q"}]}`, map[string]string{"c1": "cmp_1", "c2": "rs_2"}},
	} {
		for _, pass := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/passthrough=%t", tc.name, pass), func(t *testing.T) {
				withResilienceSettings(t, nil)
				upstream := &encryptedForeignUpstream{bad: tc.bad}
				cfg := &config.Config{}
				cfg.JWT.Secret = "fixture-jwt-secret"
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
				for turn, sends := range []int{3, 1} {
					req := newWebSearchReplayRequest(t)
					before := len(upstream.bodies)
					_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(pass), []byte(tc.body))
					require.NoError(t, err, "turn %d", turn+1)
					require.Equal(t, http.StatusOK, req.rec.Code, "turn %d", turn+1)
					require.Len(t, upstream.bodies[before:], sends, "turn %d", turn+1)
					last := upstream.bodies[len(upstream.bodies)-1]
					require.NotContains(t, string(last), "encrypted_content")
					require.Equal(t, `"q"`, gjson.GetBytes(last, "input.#(role==\"user\").content").Raw)
					require.Empty(t, req.session.failures)
				}
			})
		}
	}
}

// E24: several id-less encrypted reasoning items and an upstream 400 without
// any location. Like the baseline, all top-level ciphertext is cleared once
// within the same budget, the request succeeds and a degradation is recorded.
func TestHTTPEncryptedRepairUnlocatedMultipleItems(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"store":false,"input":[{"type":"reasoning","summary":[],"encrypted_content":"cipher-A"},{"role":"assistant","content":"a"},{"type":"reasoning","summary":[],"encrypted_content":"cipher-B"},{"role":"user","content":"q"}]}`
	rejection := `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","param":null,"message":"Encrypted content could not be verified."}}`
	for _, pass := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", pass), func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIUpstreamErrorResponse(http.StatusBadRequest, rejection),
				webSearchSuccessStream("resp_ok"),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(pass), []byte(body))
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 2)
			require.Equal(t, http.StatusOK, req.rec.Code)
			require.NotContains(t, string(upstream.bodies[1]), "encrypted_content")
			require.Len(t, gjson.GetBytes(upstream.bodies[1], "input").Array(), 4)
			require.Equal(t, `"a"`, gjson.GetBytes(upstream.bodies[1], "input.1.content").Raw)
			require.Equal(t, `"q"`, gjson.GetBytes(upstream.bodies[1], "input.3.content").Raw)
			require.Contains(t, resilience.SnapshotConversionNotices(req.ctx), apicompat.ConversionNotice{Field: "input[].encrypted_content", Action: "omit"})
			require.Empty(t, req.session.failures)
		})
	}
}

func TestHTTPEncryptedRepairM7RespectsAttemptCap(t *testing.T) {
	for _, maxAttempts := range []int{1, 2} {
		t.Run(fmt.Sprint(maxAttempts), func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			settings := resilience.Defaults()
			settings.Budget.MaxAttempts = maxAttempts
			budget := resilience.NewBudget(req.ctx, &settings, resilience.KindText, time.Now())
			state := resilience.NewRequestState()
			state.SetBudget(budget)
			ctx := resilience.WithRequestState(req.ctx, state)
			account := webSearchReplayAccount(false)
			body := []byte(`{"input":[{"type":"reasoning","id":"rs_one","encrypted_content":"synthetic-cipher"}]}`)
			payload := []byte(`{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content","message":"Encrypted content could not be verified for item rs_one"}}`)
			repair := armOpenAIRequestRepair(req.c, account, body)
			_, allowed := planHTTPEncryptedContentRepair(ctx, req.c, account, repair, body, payload)
			require.Equal(t, maxAttempts > 1, allowed)
		})
	}
}
