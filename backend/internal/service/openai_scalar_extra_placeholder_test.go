//go:build unit

package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Same wire shapes as a complete blank message emitted before the real answer.
// The extension key is deliberately generic: its name never affects the rule.
func scalarExtraPlaceholderStream() (prelude, visible, terminal string) {
	var b strings.Builder
	sequence := 0
	emit := func(event, fields string) {
		_, _ = fmt.Fprintf(&b, "event: %s\ndata: {\"type\":%q,\"sequence_number\":%d%s}\n\n", event, event, sequence, fields)
		sequence++
	}
	emit("response.created", `,"response":{"id":"resp_scalar","status":"in_progress"}`)
	emit("response.in_progress", `,"response":{"id":"resp_scalar","status":"in_progress"}`)
	emit("response.output_item.added", `,"output_index":0,"item":{"id":"msg_blank","type":"message","role":"assistant","status":"in_progress","content":[]},"x":true`)
	emit("response.content_part.added", `,"item_id":"msg_blank","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[],"logprobs":[]},"x":true`)
	emit("response.output_text.delta", `,"item_id":"msg_blank","output_index":0,"content_index":0,"delta":" ","x":true`)
	emit("response.output_text.done", `,"item_id":"msg_blank","output_index":0,"content_index":0,"text":" ","x":true`)
	emit("response.content_part.done", `,"item_id":"msg_blank","output_index":0,"content_index":0,"part":{"type":"output_text","text":" ","annotations":[],"logprobs":[]},"x":true`)
	emit("response.output_item.done", `,"output_index":0,"item":{"id":"msg_blank","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":" ","annotations":[],"logprobs":[]}]},"x":true`)
	emit("response.output_item.added", `,"output_index":1,"item":{"id":"msg_answer","type":"message","role":"assistant","status":"in_progress","phase":"final_answer","content":[]}`)
	prelude = b.String()
	b.Reset()
	emit("response.output_text.delta", `,"item_id":"msg_answer","output_index":1,"content_index":0,"delta":"ok"`)
	emit("response.metadata", `,"metadata":{}`)
	emit("response.output_text.delta", `,"item_id":"msg_answer","output_index":1,"content_index":0,"delta":"!"`)
	visible = b.String()
	b.Reset()
	emit("response.completed", `,"response":{"id":"resp_scalar","status":"completed","usage":{"input_tokens":1,"output_tokens":2}}`)
	return prelude, visible, b.String()
}

func TestResponsesScalarExtraPlaceholderDisposition(t *testing.T) {
	withResilienceSettings(t, nil)
	prelude, _, _ := scalarExtraPlaceholderStream()
	for _, block := range strings.Split(strings.TrimSpace(prelude), "\n\n") {
		event, payload := sseBlockFields([]byte(block))
		t.Run(event, func(t *testing.T) {
			got := resilience.ResponsesEventOutputDisposition(event, payload)
			require.Equal(t, resilience.Prelude, got.Kind)
			require.False(t, got.Visible)
			require.False(t, got.SemanticOutput)
			require.Equal(t, event != "response.created" && event != "response.in_progress", got.BlankPlaceholder)
		})
	}
}

func runScalarExtraPlaceholderAttempt(svc *OpenAIGatewayService, c *gin.Context, account *Account, path string, body io.ReadCloser) (*int, error) {
	if path == "native" || path == "passthrough" {
		return runBlankPlaceholderAttempt(svc, c, account, path == "passthrough", body, time.Now())
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}
	var result *OpenAIForwardResult
	var err error
	if path == "messages" {
		result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "model", "model", "model", time.Now())
	} else {
		result, err = svc.handleChatStreamingResponse(resp, c, account, "model", "model", "model", time.Now(), 0)
	}
	if result != nil {
		return result.FirstTokenMs, err
	}
	return nil, err
}

func TestResponsesScalarExtraPlaceholderReplayAndFlush(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough", "chat", "messages"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				prelude, visible, terminal := scalarExtraPlaceholderStream()
				endpoint := "/v1/responses"
				if path == "chat" {
					endpoint = "/v1/chat/completions"
				} else if path == "messages" {
					endpoint = "/v1/messages"
				}
				c, recorder := compatStreamContext(endpoint, `{}`)
				state := resilience.NewRequestState()
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
				InstallDeliveryTracking(c, time.Now())
				svc := compatStreamService(nil, 0, 0, 0)
				account := rawChatCompletionsTestAccount()
				failed := prelude + "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"busy\"}}}\n\n"
				firstToken, err := runScalarExtraPlaceholderAttempt(svc, c, account, path, io.NopCloser(strings.NewReader(failed)))
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Contains(t, string(failover.ResponseBody), "server_is_overloaded")
				require.Nil(t, firstToken)
				require.Empty(t, recorder.Body.String(), "the entire failed attempt is discarded before retry")
				require.True(t, state.FirstOutput().IsZero())
				require.True(t, state.SemanticOutputAt().IsZero())

				// Replay on a second account into the same downstream response.
				account = rawChatCompletionsTestAccount()
				account.ID++
				state.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: time.Now(), Stream: true})
				reader, writer := io.Pipe()
				defer func() { _ = reader.Close(); _ = writer.Close() }()
				done := make(chan error, 1)
				go func() {
					_, attemptErr := runScalarExtraPlaceholderAttempt(svc, c, account, path, reader)
					done <- attemptErr
				}()
				_, err = io.WriteString(writer, prelude)
				require.NoError(t, err)
				synctest.Wait()
				require.Empty(t, recorder.Body.String(), "all blank frames stay staged until real output")
				require.True(t, state.FirstOutput().IsZero())
				_, err = io.WriteString(writer, visible)
				require.NoError(t, err)
				synctest.Wait()
				require.Contains(t, recorder.Body.String(), "ok", "real delta releases the stage before any terminal")
				require.False(t, state.FirstOutput().IsZero())
				if path == "native" || path == "passthrough" {
					require.Equal(t, prelude+visible, recorder.Body.String(), "raw frames retain their bytes and order")
				}
				_, err = io.WriteString(writer, terminal)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
				require.NoError(t, <-done)

				// Compare every downstream frame with the already supported blank
				// sequence. Raw paths keep extensions; converters discard them.
				control := strings.ReplaceAll(prelude+visible+terminal, `,"x":true`, "")
				controlContext, controlRecorder := compatStreamContext(endpoint, `{}`)
				_, err = runScalarExtraPlaceholderAttempt(svc, controlContext, account, path, io.NopCloser(strings.NewReader(control)))
				require.NoError(t, err)
				actual := recorder.Body.String()
				if path == "native" || path == "passthrough" {
					if path == "passthrough" {
						require.Equal(t, prelude+visible+terminal, actual)
					}
					actual = strings.ReplaceAll(actual, `,"x":true`, "")
				} else {
					var text strings.Builder
					for _, block := range strings.Split(actual, "\n\n") {
						_, data := sseBlockFields([]byte(block))
						if path == "messages" && gjson.GetBytes(data, "delta.type").String() == "text_delta" {
							text.WriteString(gjson.GetBytes(data, "delta.text").String())
						} else if path == "chat" {
							for _, choice := range gjson.GetBytes(data, "choices").Array() {
								text.WriteString(choice.Get("delta.content").String())
							}
						}
					}
					require.Equal(t, " ok!", text.String(), "one retained space, then both real deltas; no duplicate or missing text")
					if path == "messages" {
						require.Equal(t, 1, strings.Count(actual, "event: message_start"))
					} else {
						require.Equal(t, 1, strings.Count(actual, `"role":"assistant"`))
					}
				}
				require.Equal(t, controlRecorder.Body.String(), actual, "the entire client frame sequence matches ordinary blank-placeholder replay")
			})
		})
	}
}

func TestResponsesScalarExtraPlaceholderKeepsFirstOutputDeadline(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough", "chat", "messages"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				prelude, _, _ := scalarExtraPlaceholderStream()
				c, recorder := compatStreamContext("/v1/responses", `{}`)
				svc := compatStreamService(&silentUpstream{headers: true, before: prelude}, 1, 30, 0)
				account := rawChatCompletionsTestAccount()
				var err error
				if path == "native" || path == "passthrough" {
					reader, writer := io.Pipe()
					defer func() { _ = reader.Close(); _ = writer.Close() }()
					go func() { _, _ = io.WriteString(writer, prelude) }()
					_, err = runScalarExtraPlaceholderAttempt(svc, c, account, path, reader)
				} else if path == "chat" {
					_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, []byte(compatChatStreamBody), "", "")
				} else {
					_, err = svc.ForwardAsAnthropic(c.Request.Context(), c, account, []byte(compatMessagesStreamBody), "", "")
				}
				requireFirstOutputFailover(t, err)
				require.Empty(t, recorder.Body.String(), "blank messages never disarm the first-output deadline")
			})
		})
	}
}
