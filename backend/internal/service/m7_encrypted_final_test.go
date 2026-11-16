//go:build unit

package service

import (
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const m7FinalCipherA = "provider_m7_first_cipher_0123456789"
const m7FinalCipherB = "provider_m7_second_cipher_0123456789"

func m7FinalEncryptedBody(target string, stream bool) []byte {
	if target == "messages" {
		return []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"max_tokens":32,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"one","signature":%q},{"type":"thinking","thinking":"two","signature":%q}]},{"role":"user","content":"hi"}]}`, stream, m7FinalCipherA, m7FinalCipherB))
	}
	return []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"max_output_tokens":32,"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":%q,"summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":%q,"summary":[]},{"role":"user","content":"hi"}]}`, stream, m7FinalCipherA, m7FinalCipherB))
}
func m7FinalEncryptedError(param, message string) string {
	location := ""
	if param != "" {
		location = fmt.Sprintf(`,"param":%q`, param)
	}
	return `{"error":{"code":"invalid_encrypted_content"` + location + fmt.Sprintf(`,"message":%q}}`, message)
}
func m7FinalEncryptedFailure(wire, payload string, afterOutput bool) *http.Response {
	if wire == "http" {
		return newOpenAIUpstreamErrorResponse(400, payload)
	}
	frames := m7ReviewWire(m7ReviewCreated)
	if afterOutput {
		frames += m7ReviewWire(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`)
	}
	frames += m7ReviewWire(`{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":` + gjson.Get(payload, "error").Raw + `,"usage":{"input_tokens":7,"output_tokens":2}}}`)
	if wire == "eof" {
		frames = strings.TrimRight(frames, "\n")
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(frames))}
}
func TestM7FinalEncryptedRepairPlan(t *testing.T) {
	for _, target := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, wire := range []string{"http", "sse", "eof"} {
				for _, mode := range []string{"unlocated", "sequential", "rejected_cleared", "budget", "after_output", "shadow"} {
					if mode == "after_output" && (!stream || wire == "http") {
						continue
					}
					t.Run(fmt.Sprintf("%s/%t/%s/%s", target, stream, wire, mode), func(t *testing.T) {
						withResilienceSettings(t, func(s *resilience.Settings) {
							if mode == "budget" || mode == "shadow" {
								s.Budget.MaxAttempts = 1
							}
						})
						req := newWebSearchReplayRequest(t)
						req.c.Request.URL.Path = "/v1/chat/completions"
						if target == "messages" {
							req.c.Request.URL.Path = "/v1/messages"
						}
						account := webSearchReplayAccount(false)
						index := 0
						if target == "messages" {
							index = 1
						}
						param := fmt.Sprintf("input[%d].encrypted_content", index)
						message := "Encrypted content could not be verified: " + m7FinalCipherA
						if mode == "unlocated" {
							param = ""
						}
						if mode == "shadow" {
							message += " parent-fixture-access-token"
						}
						first := m7FinalEncryptedFailure(wire, m7FinalEncryptedError(param, message), mode == "after_output")
						responses := []*http.Response{first}
						if mode == "sequential" {
							responses = append(responses, m7FinalEncryptedFailure(wire, m7FinalEncryptedError(fmt.Sprintf("input[%d].encrypted_content", index+1), "Encrypted content could not be verified"), false))
						}
						if mode == "rejected_cleared" {
							responses = append(responses, m7FinalEncryptedFailure(wire, m7FinalEncryptedError(param, message), false))
							// Id-less Messages reasoning disappears when repaired. The same
							// index then addresses the remaining item, so M10 permits bulk repair.
							if target == "messages" {
								responses = append(responses, m7FinalEncryptedFailure(wire, m7FinalEncryptedError(param, message), false))
							}
						}
						responses = append(responses, webSearchSuccessStream("resp_repaired"))
						up := &httpUpstreamRecorder{responses: responses}
						svc := newOpenAIImageGenerationControlTestService(up)
						svc.cfg.JWT.Secret = "fixture-secret"
						if mode == "shadow" {
							parentID := int64(9007)
							account.ParentAccountID = &parentID
							svc.accountRepo = encryptedShadowParentRepo{parent: &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "parent-fixture-access-token"}}}
						}
						body := m7FinalEncryptedBody(target, stream)
						var result *OpenAIForwardResult
						var err error
						if target == "chat" {
							result, err = svc.ForwardAsChatCompletions(req.ctx, req.c, account, body, "", "")
						} else {
							result, err = svc.ForwardAsAnthropic(req.ctx, req.c, account, body, "", "")
						}
						success := mode == "unlocated" || mode == "sequential"
						if success {
							require.NoError(t, err)
							require.NotNil(t, result)
							require.Equal(t, 200, req.rec.Code)
							sends := 2
							if mode == "sequential" {
								sends = 3
								require.Contains(t, string(up.bodies[1]), m7FinalCipherB)
							}
							require.Len(t, up.bodies, sends)
							last := up.bodies[len(up.bodies)-1]
							require.NotContains(t, string(last), m7FinalCipherA)
							require.NotContains(t, string(last), m7FinalCipherB)
							notices := resilience.SnapshotConversionNotices(req.ctx)
							require.NotEmpty(t, notices)
							found := false
							for _, n := range notices {
								found = found || n.Field == "input[].encrypted_content" && n.Action == "omit"
							}
							require.True(t, found)
						} else {
							var rejection *OpenAIRequestRejectionError
							var failover *UpstreamFailoverError
							require.ErrorAs(t, err, &rejection)
							require.NotErrorAs(t, err, &failover)
							sends := 1
							if mode == "rejected_cleared" {
								sends = 2
								if target == "messages" {
									sends = 3
								}
							}
							require.Len(t, up.bodies, sends)
							require.Contains(t, req.rec.Body.String(), "encrypted_content_rejected")
							require.NotContains(t, req.rec.Body.String(), m7FinalCipherA)
							require.NotContains(t, rejection.Error(), m7FinalCipherA)
							require.NotContains(t, req.rec.Body.String(), "parent-fixture-access-token")
							if mode == "shadow" {
								require.Contains(t, req.rec.Body.String(), "input[")
							}
							if mode == "after_output" {
								require.NotNil(t, result)
								require.True(t, result.PartialFailure)
								require.Equal(t, 7, result.Usage.InputTokens)
								require.Equal(t, 2, result.Usage.OutputTokens)
								require.Contains(t, req.rec.Body.String(), "hello")
								require.Equal(t, 1, strings.Count(req.rec.Body.String(), `"code":"encrypted_content_rejected"`))
							} else {
								require.Equal(t, 400, req.rec.Code)
								require.Empty(t, req.rec.Header().Get("Retry-After"))
								require.Equal(t, "false", req.rec.Header().Get("x-should-retry"))
							}
						}
						for _, sent := range up.bodies {
							require.True(t, gjson.GetBytes(sent, "input").Exists())
							require.False(t, gjson.GetBytes(sent, "messages").Exists())
							require.Contains(t, string(sent), "hi")
						}
						require.Empty(t, req.session.failures)
					})
				}
			}
		}
	}
}

func TestM7FinalEncryptedLocatorThroughForwarder(t *testing.T) {
	id := "rs_" + strings.Repeat("a", 65)
	for _, stream := range []bool{false, true} {
		for _, wire := range []string{"http", "sse", "eof"} {
			t.Run(fmt.Sprintf("%t/%s", stream, wire), func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.MaxAttempts = 1 })
				req := newWebSearchReplayRequest(t)
				req.c.Request.URL.Path = "/v1/chat/completions"
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"input":[{"type":"reasoning","id":%q,"encrypted_content":%q,"summary":[]},{"type":"reasoning","id":"rs_other","encrypted_content":%q,"summary":[]},{"role":"user","content":"hi"}]}`, stream, id, m7FinalCipherA, m7FinalCipherB))
				payload := m7FinalEncryptedError("", "Encrypted content could not be verified for item "+id)
				up := &httpUpstreamRecorder{responses: []*http.Response{m7FinalEncryptedFailure(wire, payload, false)}}
				svc := newOpenAIImageGenerationControlTestService(up)
				_, err := svc.ForwardAsChatCompletions(req.ctx, req.c, webSearchReplayAccount(false), body, "", "")
				var rejection *OpenAIRequestRejectionError
				require.ErrorAs(t, err, &rejection)
				require.Len(t, up.bodies, 1)
				require.Equal(t, id, gjson.GetBytes(up.bodies[0], "input.0.id").String(), "this converted API-key builder preserves the original id")
				require.Contains(t, req.rec.Body.String(), "at item "+id+".")
				require.Contains(t, req.rec.Body.String(), "Remove only the identified encrypted item")
				require.False(t, gjson.GetBytes(req.rec.Body.Bytes(), "error.param").Exists())
			})
		}
	}
}

func TestM7FinalEncryptedMappedReaderLocator(t *testing.T) {
	id := "rs_" + strings.Repeat("a", 65)
	for _, target := range []string{"chat", "messages"} {
		for _, buffered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", target, buffered), func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.MaxAttempts = 1 })
				req := newWebSearchReplayRequest(t)
				req.c.Request.URL.Path = "/v1/chat/completions"
				if target == "messages" {
					req.c.Request.URL.Path = "/v1/messages"
				}
				original := fmt.Sprintf(`{"input":[{"type":"reasoning","id":%q,"encrypted_content":%q,"summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":%q,"summary":[]}]}`, id, m7FinalCipherA, m7FinalCipherB)
				normalized := strings.Replace(original, fmt.Sprintf(`"id":%q,`, id), "", 1)
				account := webSearchReplayAccount(false)
				svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
				repair := armOpenAIRequestRepair(req.c, account, nil)
				svc.prepareM7EncryptedRepair(req.c, account, repair, []byte(original), "fixture-token")
				bindM7ResponsesWireBody(req.c, account, []byte(normalized))
				req.budget.RecordAttemptAt(time.Now())
				response := m7FinalEncryptedFailure("eof", m7FinalEncryptedError("", "Encrypted content could not be verified for item "+id), !buffered)
				var result *OpenAIForwardResult
				var err error
				if target == "chat" {
					if buffered {
						result, err = svc.handleChatBufferedStreamingResponse(response, req.c, account, "fixture", "fixture", "fixture", time.Now())
					} else {
						result, err = svc.handleChatStreamingResponse(response, req.c, account, "fixture", "fixture", "fixture", time.Now(), 1)
					}
				} else {
					if buffered {
						result, err = svc.handleAnthropicBufferedStreamingResponse(response, req.c, account, "fixture", "fixture", "fixture", time.Now())
					} else {
						result, err = svc.handleAnthropicStreamingResponse(response, req.c, account, "fixture", "fixture", "fixture", time.Now())
					}
				}
				var rejection *OpenAIRequestRejectionError
				require.ErrorAs(t, err, &rejection)
				require.Contains(t, req.rec.Body.String(), "at item "+id+".")
				require.NotContains(t, req.rec.Body.String(), `"param":`)
				require.NotNil(t, result)
				require.Equal(t, 7, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.Equal(t, 1, req.budget.Attempts())
				require.Equal(t, 1, strings.Count(req.rec.Body.String(), `"code":"encrypted_content_rejected"`))
			})
		}
	}
}
