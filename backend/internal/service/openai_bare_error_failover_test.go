//go:build unit

package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func bareErrorPayload(shape, fields string) string {
	switch shape {
	case "top":
		return `{"type":"error",` + fields + `}`
	case "null":
		return `{"type":"error","error":null,` + fields + `}`
	case "scalar":
		return `{"type":"error","error":"rejected",` + fields + `}`
	default:
		return `{"type":"error","error":{` + fields + `}}`
	}
}

func bareErrorResponse(frames string) *http.Response {
	resp := newOpenAIUpstreamErrorResponse(http.StatusOK, frames)
	resp.Header.Set("Content-Type", "text/event-stream")
	return resp
}

func bareErrorFrame(payload string) string { return "event: error\ndata: " + payload + "\n\n" }

func forwardBareErrorPath(svc *OpenAIGatewayService, c *gin.Context, account *Account, path string, streaming bool, writes *strings.Builder) error {
	body := []byte(fmt.Sprintf(`{"model":"public","stream":%t,"input":"hello","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`, streaming))
	switch path {
	case "chat":
		_, err := svc.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
		return err
	case "messages":
		_, err := svc.ForwardAsAnthropic(c.Request.Context(), c, account, body, "", "")
		return err
	case "bridge":
		_, err := svc.proxyOpenAIWSHTTPBridgeTurn(c.Request.Context(), c, account, "test-key", body, len(body), "public", "", "", "", "", 1,
			func(message []byte) error { writes.Write(message); writes.WriteString("\n"); return nil })
		return err
	default:
		_, err := svc.Forward(c.Request.Context(), c, account, body)
		return err
	}
}

func TestOpenAIBareErrorTransportRecovery(t *testing.T) {
	withResilienceSettings(t, nil)
	codes := []string{OpenAIUpstreamStreamReadErrorCode, OpenAIUpstreamHTTP2StreamErrorCode, OpenAIUpstreamStreamTruncatedCode, OpenAIStreamTimeoutCode}
	for _, path := range []string{"native", "passthrough", "chat", "messages", "bridge"} {
		for _, code := range codes {
			for _, shape := range []string{"top", "object"} {
				t.Run(path+"/"+code+"/"+shape, func(t *testing.T) {
					c, rec := newOpenAIUpstreamErrorTestContext(t)
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
					InstallDeliveryTracking(c, time.Now())
					payload := bareErrorPayload(shape, fmt.Sprintf(`"code":%q,"message":"Upstream response stream was interrupted","param":null,"sequence_number":0`, code))
					upstream := &httpUpstreamRecorder{responses: []*http.Response{bareErrorResponse(bareErrorFrame(payload)), bareErrorResponse(openAIChatFieldRetrySuccessStream)}}
					svc := compatStreamService(upstream, 0, 0, 0)
					var writes strings.Builder
					first := streamModelAccount(1, path == "passthrough", "https://relay.example/v1")
					err := forwardBareErrorPath(svc, c, first, path, true, &writes)
					var failure *UpstreamFailoverError
					require.ErrorAs(t, err, &failure)
					require.Empty(t, rec.Body.String())
					require.Empty(t, writes.String())
					raw, ok := c.Get(OpsUpstreamErrorsKey)
					require.True(t, ok)
					attempts := raw.([]*OpsUpstreamErrorEvent)
					require.Len(t, attempts, 1)
					require.Equal(t, int64(1), attempts[0].AccountID)
					require.Equal(t, "failover", attempts[0].Kind)
					require.Equal(t, string(resilience.CategoryTransient), attempts[0].ResilienceCategory)
					second := streamModelAccount(2, path == "passthrough", "https://relay.example/v1")
					require.NoError(t, forwardBareErrorPath(svc, c, second, path, true, &writes))
					require.Len(t, upstream.bodies, 2)
					output := rec.Body.String() + writes.String()
					require.Contains(t, output, "ok")
					require.NotContains(t, output, "response.failed")
					require.NotContains(t, output, code)
					raw, _ = c.Get(OpsUpstreamErrorsKey)
					require.Len(t, raw.([]*OpsUpstreamErrorEvent), 1)
				})
			}
		}
	}
}

func TestOpenAIBareErrorHeartbeatCommittedThenTransportFailsOver(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			for _, shape := range []string{"top", "object"} {
				t.Run(fmt.Sprintf("%s/%s/%s", path, accountType, shape), func(t *testing.T) {
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					upstream := &httpUpstreamRecorder{resp: bareErrorResponse("")}
					upstream.resp.Body = reader
					svc := compatStreamService(upstream, 30, 0, 1)
					c, rec := newOpenAIUpstreamErrorTestContext(t)
					InstallDeliveryTracking(c, time.Now())
					account := streamModelAccount(1, path == "passthrough", "https://relay.example/v1")
					account.Type = accountType
					if accountType == AccountTypeOAuth {
						account.Credentials["access_token"] = "test-token"
					}
					resultCh := make(chan error, 1)
					go func() {
						resultCh <- forwardBareErrorPath(svc, c, account, path, true, &strings.Builder{})
					}()
					opening := `data: {"type":"response.created","response":{"id":"resp_heartbeat","model":"public"}}` + "\n\n"
					_, err := io.WriteString(writer, opening)
					require.NoError(t, err)
					time.Sleep(1500 * time.Millisecond)
					payload := bareErrorPayload(shape, fmt.Sprintf(`"code":%q,"message":"Upstream response stream was interrupted"`, OpenAIUpstreamStreamReadErrorCode))
					_, err = io.WriteString(writer, bareErrorFrame(payload))
					require.NoError(t, err)
					_ = writer.Close()
					select {
					case err = <-resultCh:
					case <-time.After(5 * time.Second):
						t.Fatal("stream did not decide the pre-output transport failure")
					}
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					output := rec.Body.String()
					require.Contains(t, output, ":\n\n")
					require.NotContains(t, output, "response.created")
					require.NotContains(t, output, "response.failed")
					require.NotContains(t, output, OpenAIUpstreamStreamReadErrorCode)
					raw, ok := c.Get(OpsUpstreamErrorsKey)
					require.True(t, ok)
					require.Len(t, raw.([]*OpsUpstreamErrorEvent), 1)
				})
			}
		}
	}
}

func TestOpenAIBareErrorAuthoritativePairFailsOverOnce(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			payload := bareErrorPayload("top", `"code":"`+OpenAIUpstreamStreamReadErrorCode+`","message":"Upstream response stream was interrupted"`)
			failed := `{"type":"response.failed","response":{"error":{"code":"` + OpenAIUpstreamStreamReadErrorCode + `","message":"Upstream response stream was interrupted"}}}`
			first := bareErrorResponse(bareErrorFrame(payload) + "data: " + failed + "\n\n")
			second := bareErrorResponse(openAIChatFieldRetrySuccessStream)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{first, second}}
			svc := compatStreamService(upstream, 0, 0, 0)
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			InstallDeliveryTracking(c, time.Now())
			account := streamModelAccount(1, path == "passthrough", "https://relay.example/v1")
			account.Type = AccountTypeOAuth
			account.Credentials["access_token"] = "test-token"
			err := forwardBareErrorPath(svc, c, account, path, true, &strings.Builder{})
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Len(t, upstream.bodies, 1)
			secondAccount := streamModelAccount(2, path == "passthrough", "https://relay.example/v1")
			secondAccount.Type = AccountTypeOAuth
			secondAccount.Credentials["access_token"] = "test-token"
			require.NoError(t, forwardBareErrorPath(svc, c, secondAccount, path, true, &strings.Builder{}))
			require.Len(t, upstream.bodies, 2)
			require.Contains(t, rec.Body.String(), "ok")
			require.NotContains(t, rec.Body.String(), "response.failed")
			raw, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events := raw.([]*OpsUpstreamErrorEvent)
			require.Len(t, events, 1)
			require.Equal(t, int64(1), events[0].AccountID)
			require.Equal(t, "failover", events[0].Kind)
		})
	}
}

func TestOpenAIBareErrorResponsesBoundary(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		for _, streaming := range []bool{false, true} {
			for _, tail := range []string{"eof", "failed"} {
				for _, output := range []bool{false, true} {
					if output && !streaming {
						continue
					}
					t.Run(fmt.Sprintf("%s/stream=%t/%s/output=%t", path, streaming, tail, output), func(t *testing.T) {
						payload := bareErrorPayload("top", `"code":"`+OpenAIUpstreamStreamReadErrorCode+`","message":"Upstream response stream was interrupted"`)
						frames := keepaliveStallCreated
						if output {
							frames += keepaliveStallDelta1
						}
						frames += bareErrorFrame(payload)
						if tail == "failed" {
							frames += `data: {"type":"response.failed","response":{"id":"resp_authoritative","error":{"code":"` + OpenAIUpstreamStreamReadErrorCode + `","message":"authoritative failure"},"usage":{"input_tokens":7,"output_tokens":2}}}` + "\n\n"
						}
						upstream := &httpUpstreamRecorder{resp: bareErrorResponse(frames)}
						svc := compatStreamService(upstream, 0, 0, 0)
						c, rec := newOpenAIUpstreamErrorTestContext(t)
						InstallDeliveryTracking(c, time.Now())
						if output {
							_, _ = rec.WriteString("already delivered")
						}
						var writes strings.Builder
						err := forwardBareErrorPath(svc, c, streamModelAccount(1, path == "passthrough", "https://relay.example/v1"), path, streaming, &writes)
						var failure *UpstreamFailoverError
						require.Equal(t, !output, errors.As(err, &failure))
						require.Len(t, upstream.bodies, 1)
						if !output {
							raw, ok := c.Get(OpsUpstreamErrorsKey)
							require.True(t, ok)
							require.Len(t, raw.([]*OpsUpstreamErrorEvent), 1)
						}
						if output {
							require.Contains(t, rec.Body.String(), "already delivered")
						} else {
							require.Empty(t, rec.Body.String())
						}
					})
				}
			}
		}
	}
}

func TestOpenAIBareErrorCompatAfterOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"chat", "messages", "bridge"} {
		t.Run(path, func(t *testing.T) {
			payload := bareErrorPayload("top", `"code":"`+OpenAIUpstreamStreamReadErrorCode+`","message":"Upstream response stream was interrupted"`)
			frames := keepaliveStallCreated + keepaliveStallDelta1 + bareErrorFrame(payload) + `data: {"type":"response.failed","response":{"id":"resp_authoritative","error":{"message":"authoritative failure"}}}` + "\n\n"
			upstream := &httpUpstreamRecorder{resp: bareErrorResponse(frames)}
			svc := compatStreamService(upstream, 0, 0, 0)
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			InstallDeliveryTracking(c, time.Now())
			var writes strings.Builder
			err := forwardBareErrorPath(svc, c, streamModelAccount(1, false, "https://relay.example/v1"), path, true, &writes)
			var failure *UpstreamFailoverError
			require.False(t, errors.As(err, &failure))
			require.Len(t, upstream.bodies, 1)
			require.NotEmpty(t, rec.Body.String()+writes.String())
			if path == "bridge" && err != nil {
				require.Contains(t, writes.String(), "resp_authoritative")
			}
		})
	}
}

func TestOpenAIBareErrorCodeRouting(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		code  string
		retry bool
	}{
		{"upstream_error", false}, {"moderation_blocked", false}, {"misalignment_policy_violation", false},
		{"invalid_request_error", false}, {"context_length_exceeded", false}, {"invalid_prompt", false},
		{"cyber_policy", false}, {"previous_response_not_found", false}, {"invalid_encrypted_content", false},
		{"server_is_overloaded", true}, {"slow_down", true}, {"rate_limit_exceeded", true}, {"insufficient_quota", true},
	} {
		for _, shape := range []string{"top", "object", "null", "scalar"} {
			t.Run(tc.code+"/"+shape, func(t *testing.T) {
				payload := []byte(bareErrorPayload(shape, fmt.Sprintf(`"code":%q,"message":"request rejected"`, tc.code)))
				message := extractOpenAISSEErrorMessage(payload)
				require.Equal(t, tc.retry, openAIStreamErrorEventShouldFailover(payload, message))
				require.Equal(t, tc.code, openAIStreamFailedEventErrorCode(payload))
				canonical := []byte(bareErrorPayload("object", fmt.Sprintf(`"code":%q,"message":"request rejected"`, tc.code)))
				require.Equal(t, openAIStreamFailedEventSemanticStatus(canonical, "request rejected"), openAIStreamFailedEventSemanticStatus(payload, message))
				require.Equal(t, openAIStreamFailedEventShouldFailover(canonical, "request rejected"), openAIStreamFailedEventShouldFailover(payload, message))
				canonicalStreamClass := resilience.ClassifyStreamEvent("responses", "error", canonical)
				actualStreamClass := resilience.ClassifyStreamEvent("responses", "error", payload)
				require.Equal(t, canonicalStreamClass, actualStreamClass)
				_, canonicalCategory := openAIStreamFailureStatusAndCategory("stream_failed", canonical, "request rejected")
				_, actualCategory := openAIStreamFailureStatusAndCategory("stream_failed", payload, message)
				require.Equal(t, canonicalCategory, actualCategory)
				if tc.code == "previous_response_not_found" {
					family, trigger := openAIM3Trigger(payload, "request rejected", 0)
					require.False(t, trigger, "M3 does not own this code in the selected baseline")
					canonicalFamily, canonicalTrigger := openAIM3Trigger(canonical, "request rejected", 0)
					require.Equal(t, canonicalFamily, family)
					require.Equal(t, canonicalTrigger, trigger)
					for _, candidate := range []string{
						`{"type":"error","error":{"code":"previous_response_not_found"}}`,
						`{"type":"error","code":"previous_response_not_found"}`,
					} {
						code, _, _ := parseOpenAIWSErrorEventFields([]byte(candidate))
						reason, recoverable := classifyOpenAIWSErrorEventFromRaw(code, "", "")
						require.Equal(t, "previous_response_not_found", reason)
						require.True(t, recoverable)
					}
				}
				if tc.code == "invalid_encrypted_content" {
					require.True(t, isOpenAIEncryptedContentRejectionPayload(payload))
				}
				if tc.code == "cyber_policy" {
					c, _ := newOpenAIUpstreamErrorTestContext(t)
					require.True(t, markOpenAICyberPolicyEvent(c, payload, http.StatusOK, nil))
					require.Equal(t, "request rejected", GetOpsCyberPolicy(c).Message)
				}
				if tc.code == "misalignment_policy_violation" {
					require.False(t, openAIStreamFailedEventShouldFailover(payload, "request rejected"))
					_, category := openAIStreamFailureStatusAndCategory("stream_failed", payload, "request rejected")
					require.Equal(t, resilience.CategoryClientError, category)
					require.False(t, openAIPostOutputEventCounts(payload, "request rejected"))
					require.True(t, classifyUpstreamHTTP(http.StatusBadGateway, nil, payload, "request rejected").ContentPolicy())
					require.False(t, resilience.ClassifyStreamEvent("responses", "error", payload).Retryable)
					canonicalCategory := classifyUpstreamHTTP(http.StatusBadGateway, nil, canonical, "request rejected").Category
					require.Equal(t, canonicalCategory, classifyUpstreamHTTP(http.StatusBadGateway, nil, payload, "request rejected").Category)
				}
			})
		}
	}
}

func TestOpenAIBareErrorRetryMessageMatchesObject(t *testing.T) {
	for _, shape := range []string{"top", "object"} {
		t.Run(shape, func(t *testing.T) {
			payload := []byte(bareErrorPayload(shape, `"code":"upstream_error","message":"Temporary failure; please retry"`))
			require.True(t, openAIStreamErrorEventShouldFailover(payload, ""))
		})
	}
}

func TestOpenAIBareErrorFlatFieldsArePreserved(t *testing.T) {
	for _, shape := range []string{"top", "object"} {
		t.Run(shape, func(t *testing.T) {
			payload := []byte(bareErrorPayload(shape, `"code":"unknown_parameter","message":"Unknown field","param":"cache_control"`))
			passthrough := openAIStreamFailedEventPassthroughBody(payload, "")
			require.Equal(t, "unknown_parameter", gjson.GetBytes(passthrough, "error.code").String())
			require.Equal(t, "Unknown field", gjson.GetBytes(passthrough, "error.message").String())
			require.Equal(t, "cache_control", gjson.GetBytes(passthrough, "error.param").String())

			var failedFrame []byte
			for _, line := range strings.Split(buildOpenAIResponseFailedSSE(nil, "resp_test", "model", payload, ""), "\n") {
				if data, ok := extractOpenAISSEDataLine(line); ok {
					failedFrame = append([]byte(nil), data...)
					break
				}
			}
			require.Equal(t, "unknown_parameter", gjson.GetBytes(failedFrame, "response.error.code").String())
			require.Equal(t, "Unknown field", gjson.GetBytes(failedFrame, "response.error.message").String())
			require.Equal(t, "cache_control", gjson.GetBytes(failedFrame, "response.error.param").String())

			bridgeFrame := buildOpenAIWSHTTPBridgeFailedEvent("resp_test", "model", payload, "")
			require.Equal(t, "unknown_parameter", gjson.GetBytes(bridgeFrame, "response.error.code").String())
			require.Equal(t, "Unknown field", gjson.GetBytes(bridgeFrame, "response.error.message").String())
			require.Equal(t, "cache_control", gjson.GetBytes(bridgeFrame, "response.error.param").String())
		})
	}
}

func TestOpenAIBareErrorTopLevelFallbackScope(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.completed","code":"upstream_stream_read_error"}`,
		`{"code":"upstream_stream_read_error"}`,
		`{"type":"error","code":"upstream_stream_read_error","error":{}}`,
		`{"type":"error","code":"upstream_stream_read_error","response":{"error":{}}}`,
		`{"type":"error","message":"must not mix","error":{}}`,
	} {
		require.False(t, openAIStreamErrorEventShouldFailover([]byte(payload), "request rejected"), payload)
		if strings.Contains(payload, `"message":"must not mix"`) {
			require.Empty(t, extractOpenAISSEErrorMessage([]byte(payload)))
		}
	}
}

func TestOpenAIBareErrorRepairRouting(t *testing.T) {
	for _, shape := range []string{"top", "object"} {
		for _, code := range []string{"invalid_encrypted_content", "unknown_parameter"} {
			t.Run(fmt.Sprintf("%s/%s", shape, code), func(t *testing.T) {
				withResilienceSettings(t, nil)
				req := newWebSearchReplayRequest(t)
				body := m3TwoItemRequest
				fields := fmt.Sprintf(`"code":%q,"message":"request rejected"`, code)
				switch code {
				case "invalid_encrypted_content":
					body = encryptedRepairRequestBody
				case "unknown_parameter":
					body = `{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`
					fields += `,"param":"input[0].content[0].cache_control"`
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{bareErrorResponse(bareErrorFrame(bareErrorPayload(shape, fields))), webSearchSuccessStream("resp_repaired")}}
				svc := newOpenAIImageGenerationControlTestService(upstream)
				svc.openaiWSStateStore = NewOpenAIWSStateStore(nil)
				_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(body))
				require.NoError(t, err)
				require.Len(t, upstream.bodies, 2)
				require.Equal(t, upstream.requests[0].Header.Get("Authorization"), upstream.requests[1].Header.Get("Authorization"))
				require.Contains(t, req.rec.Body.String(), "resp_repaired")
				require.NotContains(t, req.rec.Body.String(), "response.failed")
				require.Empty(t, req.session.failures)
				switch code {
				case "invalid_encrypted_content":
					require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
				case "unknown_parameter":
					require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.content.0.cache_control").Exists())
				}
			})
		}
	}
}

func TestOpenAIBareErrorM3RepairRouting(t *testing.T) {
	for _, shape := range []string{"top", "object"} {
		for _, passthrough := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/passthrough=%t", shape, passthrough), func(t *testing.T) {
				withResilienceSettings(t, nil)
				req := newWebSearchReplayRequest(t)
				payload := bareErrorPayload(shape, `"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_one"`)
				upstream := &httpUpstreamRecorder{responses: []*http.Response{bareErrorResponse(bareErrorFrame(payload)), webSearchSuccessStream("resp_repaired")}}
				svc := newOpenAIImageGenerationControlTestService(upstream)
				_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(passthrough), []byte(m3TwoItemRequest))
				require.NoError(t, err)
				require.Len(t, upstream.bodies, 2)
				require.Equal(t, upstream.requests[0].Header.Get("Authorization"), upstream.requests[1].Header.Get("Authorization"))
				require.Equal(t, "[encrypted content omitted]", gjson.GetBytes(upstream.bodies[1], "input.0.content.0.text").String())
				require.Equal(t, "same-cipher", gjson.GetBytes(upstream.bodies[1], "input.1.content.0.encrypted_content").String())
				require.Contains(t, req.rec.Body.String(), "resp_repaired")
				require.NotContains(t, req.rec.Body.String(), "response.failed")
			})
		}
	}
}

func TestOpenAIBareErrorAfterOutputUsesOneFailedTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		for _, shape := range []string{"top", "object"} {
			for _, authoritative := range []bool{false, true} {
				t.Run(fmt.Sprintf("passthrough=%t/%s/authoritative=%t", passthrough, shape, authoritative), func(t *testing.T) {
					payload := bareErrorPayload(shape, `"code":"`+OpenAIUpstreamStreamReadErrorCode+`","message":"Upstream response stream was interrupted"`)
					frames := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" + bareErrorFrame(payload)
					if authoritative {
						frames += "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_authoritative\",\"error\":{\"message\":\"authoritative failure\"}}}\n\n"
					}
					body := io.NopCloser(strings.NewReader(frames))
					var output string
					var err error
					if passthrough {
						_, rec, _, streamErr := runPassthroughFlushTest(t, body, -1)
						err, output = streamErr, rec.Body.String()
					} else {
						rec := newOpenAIResponseFlushRecorder()
						_, err = runOpenAIResponseFlushTest(rec, body, config.GatewayConfig{})
						output, _ = rec.snapshot()
					}
					require.Error(t, err)
					var failover *UpstreamFailoverError
					require.NotErrorAs(t, err, &failover)
					require.Contains(t, output, "partial")
					require.Equal(t, 1, strings.Count(output, `"type":"response.failed"`))
					require.NotContains(t, output, `"type":"error"`)
					if authoritative {
						require.Contains(t, output, "resp_authoritative")
						require.NotContains(t, output, OpenAIUpstreamStreamReadErrorCode)
					} else {
						require.Contains(t, output, OpenAIUpstreamStreamReadErrorCode)
					}
				})
			}
		}
	}
}

func TestOpenAIBareErrorCapacityShedAfterOutputSanitizesSynthesizedTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		for _, shape := range []string{"top", "object"} {
			for _, code := range []string{"server_is_overloaded", "slow_down"} {
				t.Run(fmt.Sprintf("%s/%s/%s", path, shape, code), func(t *testing.T) {
					rl, repo, _, resilienceService := newPenaltyFixtureKeepSettings(t, PlatformOpenAI)
					account := streamModelAccount(254, path == "passthrough", "https://relay.example/v1")
					repo.account = account
					account.Type = AccountTypeOAuth
					account.Credentials["access_token"] = "test-token"
					const message = "Upstream request deferred"
					payload := bareErrorPayload(shape, fmt.Sprintf(`"code":%q,"message":%q`, code, message))
					frames := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n" + bareErrorFrame(payload)
					upstream := &httpUpstreamRecorder{resp: bareErrorResponse(frames)}
					svc := compatStreamService(upstream, 0, 0, 0)
					svc.rateLimitService = rl
					c, rec := newOpenAIUpstreamErrorTestContext(t)
					state := resilience.NewRequestState()
					requestContext := resilience.WithRequestState(c.Request.Context(), state)
					c.Request = c.Request.WithContext(requestContext)
					InstallDeliveryTracking(c, time.Now())
					beforeMetric := postOutputMetric(account.ID, postOutputEvent)

					err := forwardBareErrorPath(svc, c, account, path, true, &strings.Builder{})
					require.Error(t, err)
					var failover *UpstreamFailoverError
					require.NotErrorAs(t, err, &failover)
					require.Len(t, upstream.bodies, 1)
					// Account effects retain the original capacity classification; a
					// client-side server_error must not replace the upstream evidence.
					require.True(t, state.StreamFailureReported(account.ID))
					require.True(t, state.AccountFailed(account.ID))
					require.Equal(t, beforeMetric+1, postOutputMetric(account.ID, postOutputEvent))
					facts := state.TakePostOutputFailureFacts()
					require.Len(t, facts, 1)
					require.Equal(t, resilience.CategoryCapacityShed, facts[0].Category)
					require.Len(t, resilienceService.postOutput.failures[account.ID], 1)
					require.Zero(t, repo.tempCalls, "one failure remains below the post-output cooldown threshold")
					require.Zero(t, repo.errorCalls)
					output := rec.Body.String()
					require.Contains(t, output, "partial")
					require.Equal(t, 1, strings.Count(output, `"type":"response.failed"`))
					var failurePayload []byte
					for _, line := range strings.Split(output, "\n") {
						data, ok := extractOpenAISSEDataLine(line)
						if ok && gjson.Get(data, "type").String() == "response.failed" {
							failurePayload = []byte(data)
							break
						}
					}
					require.NotEmpty(t, failurePayload)
					require.Equal(t, openAICapacityShedRetryableClientCode, gjson.GetBytes(failurePayload, "response.error.code").String())
					require.Equal(t, message, gjson.GetBytes(failurePayload, "response.error.message").String())
					require.NotContains(t, output, `"type":"error"`)
					require.NotContains(t, output, code)
				})
			}
		}
	}
}

func TestOpenAIBareErrorRateLimitAfterOutputPreservesSynthesizedCode(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		for _, shape := range []string{"top", "object"} {
			t.Run(path+"/"+shape, func(t *testing.T) {
				payload := bareErrorPayload(shape, `"code":"rate_limit_exceeded","message":"retry later"`)
				frames := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n" + bareErrorFrame(payload)
				upstream := &httpUpstreamRecorder{resp: bareErrorResponse(frames)}
				svc := compatStreamService(upstream, 0, 0, 0)
				account := streamModelAccount(1, path == "passthrough", "https://relay.example/v1")
				account.Type = AccountTypeOAuth
				account.Credentials["access_token"] = "test-token"
				c, rec := newOpenAIUpstreamErrorTestContext(t)
				err := forwardBareErrorPath(svc, c, account, path, true, &strings.Builder{})
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.NotErrorAs(t, err, &failover)
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`))
				require.Contains(t, rec.Body.String(), `"code":"rate_limit_exceeded"`)
				require.NotContains(t, rec.Body.String(), `"code":"server_error"`)
			})
		}
	}
}

func TestOpenAIBareErrorModelRouting(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		for _, shape := range []string{"top", "object"} {
			for _, status := range []int{0, 404, 503} {
				t.Run(fmt.Sprintf("%s/%s/status=%d", path, shape, status), func(t *testing.T) {
					fields := `"code":"model_not_found","message":"Requested resource absent","param":"model"`
					if status != 0 {
						fields += fmt.Sprintf(`,"status_code":%d`, status)
					}
					payload := bareErrorPayload(shape, fields)
					upstream := &httpUpstreamRecorder{resp: bareErrorResponse(bareErrorFrame(payload))}
					svc := compatStreamService(upstream, 0, 0, 0)
					repo := &openAIModelAvailabilityRepo{}
					svc.accountRepo = repo
					svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
					c, rec := newOpenAIUpstreamErrorTestContext(t)
					var writes strings.Builder
					err := forwardBareErrorPath(svc, c, streamModelAccount(1, path == "passthrough", "https://relay.example/v1"), path, true, &writes)
					var failure *UpstreamFailoverError
					require.ErrorAs(t, err, &failure)
					require.True(t, failure.IsOpenAIModelUnavailable())
					if status == 0 {
						status = 404
					}
					require.Equal(t, status, failure.StatusCode)
					require.False(t, failure.ShouldReportAccountScheduleFailure())
					require.Empty(t, rec.Body.String())
					require.Zero(t, repo.setErrorCalls)
					require.Zero(t, repo.tempCalls)
					require.Empty(t, repo.modelRateLimitCalls)
				})
			}
		}
	}
	for _, shape := range []string{"top", "object"} {
		payload := []byte(bareErrorPayload(shape, `"code":"model_not_found","message":"Requested resource absent","param":"input"`))
		_, matched := openAIStreamModelAvailabilityStatus(payload, "Requested resource absent", 0)
		require.False(t, matched)
	}
}
