//go:build unit

package service

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func conversionDiagnosticSink(t *testing.T) *OpsSystemLogSink {
	t.Helper()
	previous := slog.Default()
	logger.InitBootstrap()
	t.Cleanup(func() { slog.SetDefault(previous) })
	sink := NewOpsSystemLogSink(nil)
	t.Cleanup(sink.Stop)
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	return sink
}

func conversionDiagnosticEvents(sink *OpsSystemLogSink) []map[string]any {
	var events []map[string]any
	for len(sink.queue) > 0 {
		event := <-sink.queue
		if event.Message == "upstream_conversion_failed" {
			events = append(events, event.Fields)
		}
	}
	return events
}

func TestConversionDiagnosticResponsesTerminals(t *testing.T) {
	withResilienceSettings(t, nil)
	sink := conversionDiagnosticSink(t)
	for _, target := range []string{"chat", "messages"} {
		for _, mode := range []string{"buffered", "stream", "heartbeat", "after_output"} {
			t.Run(target+"/"+mode, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+target, nil)
				body := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"private_item_a","type":"message","role":"assistant","content":[]}}` + "\n\n"
				if mode == "after_output" {
					body += `data: {"type":"response.output_text.delta","item_id":"private_item_a","output_index":0,"content_index":0,"delta":"private_content"}` + "\n\n"
				}
				wantField, wantReason, wantIdentity := "output", "conflicting_output_identity", "output_index_item_id"
				if target == "messages" && mode != "buffered" {
					body += `data: {"type":"response.output_text.delta","item_id":"private_item_a","output_index":0,"content_index":0,"delta":123}` + "\n\n"
					wantField, wantReason, wantIdentity = "delta", "invalid_event", ""
				} else {
					body += `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"private_item_b","type":"message","role":"assistant","content":[]}}` + "\n\n"
				}
				body += `data: {"type":"response.completed","response":{"id":"private_response","status":"completed","output":[]}}` + "\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				if mode == "heartbeat" {
					c.Header("Content-Type", "text/event-stream")
					_, err := c.Writer.WriteString(": heartbeat\n\n")
					require.NoError(t, err)
				}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				account := rawChatCompletionsTestAccount()
				var err error
				if target == "chat" {
					if mode == "buffered" {
						_, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
					} else {
						_, err = svc.handleChatStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now(), 1)
					}
				} else if mode == "buffered" {
					_, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
				} else {
					_, err = svc.handleAnthropicStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
				}
				require.Error(t, err)
				events := conversionDiagnosticEvents(sink)
				require.Len(t, events, 1)
				fields := events[0]
				require.Equal(t, "responses", fields["source_protocol"])
				require.Equal(t, wantField, fields["conversion_field"])
				require.Equal(t, wantReason, fields["conversion_reason"])
				if wantIdentity != "" {
					require.Equal(t, wantIdentity, fields["identity_conflict"])
				} else {
					require.NotContains(t, fields, "identity_conflict")
				}
				require.EqualValues(t, 200, fields["upstream_status"])
				require.EqualValues(t, 502, fields["client_error_status"])
				wantStatus := http.StatusBadGateway
				if mode == "heartbeat" || mode == "after_output" {
					wantStatus = http.StatusOK
				}
				require.Equal(t, wantStatus, c.Writer.Status())
				require.EqualValues(t, c.Writer.Status(), fields["client_status"])
				require.NotContains(t, fmt.Sprint(fields), "private_")
				t.Logf("source=responses mode=%s upstream=200 client=%d reason=%s subtype=%v", mode, c.Writer.Status(), fields["conversion_reason"], fields["identity_conflict"])
			})
		}
	}
}

func TestConversionDiagnosticOtherProtocolsAndDeduplication(t *testing.T) {
	sink := conversionDiagnosticSink(t)
	for _, source := range []string{"chat", "anthropic", "gemini"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", source, stream), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				state := resilience.NewRequestState()
				state.NoteUpstreamHTTPStatus(1, http.StatusCreated)
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
				if stream {
					c.Header("Content-Type", "text/event-stream")
					_, err := c.Writer.WriteString(": heartbeat\n\n")
					require.NoError(t, err)
				}
				err := fmt.Errorf("private_wrapper: %w", &apicompat.UpstreamConversionError{SourceProtocol: source, Field: "finish_reason", Reason: "terminal reason has no equivalent", SourceReason: "private_reason"})
				for range 2 {
					writeCCStreamReadError(c, PreOutputProtocolChat, err)
				}
				events := conversionDiagnosticEvents(sink)
				require.Len(t, events, 1)
				require.Equal(t, source, events[0]["source_protocol"])
				require.Equal(t, "finish_reason", events[0]["conversion_field"])
				require.Equal(t, "unsupported_terminal", events[0]["conversion_reason"])
				require.EqualValues(t, 201, events[0]["upstream_status"])
				require.NotContains(t, fmt.Sprint(events), "private_")
			})
		}
	}
}

func TestConversionDiagnosticGeminiTerminals(t *testing.T) {
	sink := conversionDiagnosticSink(t)
	for _, target := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", target, stream), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+target, nil)
				body := `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":5}}`
				contentType := "application/json"
				if stream {
					body = "data: " + body + "\n\n"
					contentType = "text/event-stream"
				}
				resp := &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
				svc := &GeminiMessagesCompatService{cfg: rawChatCompletionsTestConfig()}
				var err error
				if target == "chat" && stream {
					_, err = svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "fixture", false, true)
				} else if target == "chat" {
					_, err = svc.handleChatCompletionsNonStreamingResponseFromGemini(c, resp, "fixture", false)
				} else if stream {
					_, err = svc.handleStreamingResponse(c, resp, time.Now(), "fixture")
				} else {
					_, err = svc.handleNonStreamingResponse(c, resp, "fixture")
				}
				require.Error(t, err)
				events := conversionDiagnosticEvents(sink)
				require.Len(t, events, 1)
				require.Equal(t, "gemini", events[0]["source_protocol"])
				require.Equal(t, "finish_reason", events[0]["conversion_field"])
				require.Equal(t, "unsupported_terminal", events[0]["conversion_reason"])
				require.EqualValues(t, 201, events[0]["upstream_status"])
			})
		}
	}
}
