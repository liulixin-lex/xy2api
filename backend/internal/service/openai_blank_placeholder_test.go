//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func d10BlankPlaceholderStream(count int, failed bool) string {
	var b strings.Builder
	var completedItems []string
	emit := func(typ string, sequence int, fields string) {
		_, _ = fmt.Fprintf(&b, "event: %s\ndata: {\"type\":%q,\"sequence_number\":%d%s}\n\n", typ, typ, sequence, fields)
	}
	emit("response.created", 0, `,"response":{"id":"resp_ft_test","status":"in_progress"}`)
	emit("response.in_progress", 1, `,"response":{"id":"resp_ft_test","status":"in_progress"}`)
	sequence := 2
	for i := 0; i < count; i++ {
		item := fmt.Sprintf(`{"id":"msg_ft_%d","type":"message","role":"assistant","status":"in_progress","phase":"final_answer","content":[{"type":"output_text","text":" ","annotations":[],"logprobs":[]}]}`, i)
		part := `{"type":"output_text","text":" ","annotations":[],"logprobs":[]}`
		emit("response.output_item.added", sequence, fmt.Sprintf(`,"output_index":%d,"item":%s`, i, item))
		sequence++
		emit("response.content_part.added", sequence, fmt.Sprintf(`,"item_id":"msg_ft_%d","output_index":%d,"content_index":0,"part":%s`, i, i, part))
		sequence++
		emit("response.output_text.delta", sequence, fmt.Sprintf(`,"item_id":"msg_ft_%d","output_index":%d,"content_index":0,"delta":" ","logprobs":[],"obfuscation":"test"`, i, i))
		sequence++
		emit("response.output_text.done", sequence, fmt.Sprintf(`,"item_id":"msg_ft_%d","output_index":%d,"content_index":0,"text":" ","logprobs":[]`, i, i))
		sequence++
		emit("response.content_part.done", sequence, fmt.Sprintf(`,"item_id":"msg_ft_%d","output_index":%d,"content_index":0,"part":%s`, i, i, part))
		sequence++
		completedItem := strings.Replace(item, `"status":"in_progress"`, `"status":"completed"`, 1)
		emit("response.output_item.done", sequence, fmt.Sprintf(`,"output_index":%d,"item":%s`, i, completedItem))
		sequence++
		completedItems = append(completedItems, completedItem)
	}
	if failed {
		emit("response.failed", sequence, `,"response":{"id":"resp_ft_test","status":"failed","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"busy"}}`)
		return b.String()
	}
	emit("response.completed", sequence, `,"response":{"id":"resp_ft_test","status":"completed","output":[`+strings.Join(completedItems, ",")+`]}`)
	return b.String()
}

func TestResponsesBlankPlaceholderFailoverDropsWholeD10Prelude(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		for _, count := range []int{1, 2} {
			t.Run(fmt.Sprintf("passthrough=%t/placeholders=%d", passthrough, count), func(t *testing.T) {
				c, recorder := compatStreamContext("/v1/responses", `{}`)
				body := d10BlankPlaceholderStream(count, true)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				account := &Account{ID: 9021, Name: "account_test", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				} else {
					_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				}
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Empty(t, recorder.Body.String(), "created/in_progress and every whitespace placeholder must be discarded")
				wantFailureSequence := 8
				if count == 2 {
					wantFailureSequence = 14
				}
				require.Contains(t, body, fmt.Sprintf(`"sequence_number":%d`, wantFailureSequence))
			})
		}
	}
}

func TestResponsesBlankPlaceholderCompletedFlushesInOrderWithoutFirstToken(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", passthrough), func(t *testing.T) {
			c, recorder := compatStreamContext("/v1/responses", `{}`)
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			account := &Account{ID: 9022, Name: "account_test", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(d10BlankPlaceholderStream(1, false)))}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			var firstToken *int
			var err error
			if passthrough {
				result, handleErr := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				err = handleErr
				if result != nil {
					firstToken = result.firstTokenMs
				}
			} else {
				result, handleErr := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				err = handleErr
				if result != nil {
					firstToken = result.firstTokenMs
				}
			}
			require.NoError(t, err)
			require.Nil(t, firstToken, "an empty successful terminal does not count as a token")
			require.Zero(t, state.FirstOutput())
			require.True(t, state.SemanticOutputAt().IsZero(), "whitespace-only response.output is not semantic output")
			body := recorder.Body.String()
			require.Less(t, strings.Index(body, "response.created"), strings.Index(body, "response.output_item.added"))
			require.Less(t, strings.Index(body, "response.output_item.done"), strings.Index(body, "response.completed"))
		})
	}
}

func TestResponsesBlankPlaceholderPredicatesAndSemanticDeliveryAgree(t *testing.T) {
	withResilienceSettings(t, nil)
	tests := []struct {
		name, event, payload string
	}{
		{"delta", "response.output_text.delta", `{"type":"response.output_text.delta","delta":" \t"}`},
		{"text done", "response.output_text.done", `{"type":"response.output_text.done","text":"　 "}`},
		{"content part", "response.content_part.added", `{"type":"response.content_part.added","part":{"type":"output_text","text":" "}}`},
		{"message item", "response.output_item.added", `{"type":"response.output_item.added","item":{"type":"message","role":"assistant","status":"in_progress","content":[{"type":"output_text","text":" "}]}}`},
	}
	for _, block := range strings.Split(d10BlankPlaceholderStream(2, true), "\n\n") {
		event, data := sseBlockFields([]byte(block))
		if len(data) > 0 && event != "response.failed" {
			tests = append(tests, struct{ name, event, payload string }{event + " wire", event, string(data)})
		}
	}
	tests = append(tests, struct{ name, event, payload string }{"metadata wire", "response.metadata", `{"type":"response.metadata","response_id":"resp_test","sequence_number":2,"metadata":{}}`})
	// Marshal the repository's own wire shape so logprobs cannot disappear
	// from this fixture when the apicompat encoder changes.
	part, err := json.Marshal(apicompat.ResponsesStreamEvent{Type: "response.content_part.added", Part: &apicompat.ResponsesContentPart{Type: "output_text", Text: " "}})
	require.NoError(t, err)
	tests = append(tests, struct{ name, event, payload string }{"apicompat wire", "response.content_part.added", string(part)})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			disposition := resilience.ResponsesEventOutputDisposition(tc.event, payload)
			require.Equal(t, resilience.Prelude, disposition.Kind)
			var compatEvent apicompat.ResponsesStreamEvent
			require.NoError(t, json.Unmarshal(payload, &compatEvent))
			require.True(t, openAICompatPreludeEvent(&compatEvent, payload))
			require.Equal(t, resilience.Prelude, resilience.ClassifyStreamEvent("responses", tc.event, payload).Kind)
			require.False(t, openAIStreamDataStartsClientOutput(tc.payload, tc.event))
			require.False(t, openAIStreamDataStartsVisibleOutput(tc.payload, tc.event))
			require.False(t, openAIStreamDataStartsSemanticTTFT(tc.payload, tc.event))
			require.False(t, openAIStreamDataStartsTTFT(tc.payload, tc.event, false, OpenAITTFTModeVisible))
			require.False(t, semanticSSEBlock([]byte("event: "+tc.event+"\ndata: "+tc.payload)))

			c, state := newSSEDeliveryContext(t, "text/event-stream")
			start := time.Now().Add(-time.Second)
			state.SetAttemptProfile(resilience.AttemptProfile{AccountID: 9023, AttemptStart: start})
			_, err := deliveryWriterFrom(c).WriteString("event: " + tc.event + "\ndata: " + tc.payload + "\n\n")
			require.NoError(t, err)
			require.True(t, state.SemanticOutputAt().IsZero())
			require.Nil(t, state.AttemptFirstOutputMs())
			require.True(t, state.FirstOutput().IsZero())
		})
	}
}

func TestResponsesMalformedEventFailsClosedAtAllOutputChecks(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, payload := range []string{
		`{"type":"response.output_text.delta","delta":`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m","type":"message","role":"assistant","content":[{"type":"output_text","text":" ","text":"hi"}]}}`,
		`{"type":"response.output_text.delta","item_id":[],"output_index":"bad","delta":" "}`,
	} {
		t.Run(payload, func(t *testing.T) {
			event := gjson.Get(payload, "type").String()
			got := resilience.ClassifyStreamEvent("responses", event, []byte(payload))
			require.Equal(t, resilience.Content, got.Kind)
			require.True(t, openAIStreamDataStartsClientOutput(payload, event))
			require.True(t, openAIStreamDataStartsVisibleOutput(payload, event))
			require.True(t, openAIStreamDataStartsSemanticTTFT(payload, event))
			require.True(t, semanticSSEBlock([]byte("event: "+event+"\ndata: "+payload)))
			require.False(t, openAICompatPreludeEvent(&apicompat.ResponsesStreamEvent{Type: event}, []byte(payload)))
			c, state := newSSEDeliveryContext(t, "text/event-stream")
			start := time.Now().Add(-time.Second)
			state.SetAttemptProfile(resilience.AttemptProfile{AccountID: 9024, AttemptStart: start})
			_, err := deliveryWriterFrom(c).WriteString("event: " + event + "\ndata: " + payload + "\n\n")
			require.NoError(t, err)
			require.False(t, state.SemanticOutputAt().IsZero())
			require.NotNil(t, state.AttemptFirstOutputMs())
		})
	}
}

func TestCompatStreamStagesD10BlankPlaceholderBeforeFailure(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, count := range []int{1, 2} {
		c, recorder := compatStreamContext("/v1/chat/completions", compatChatStreamBody)
		_, err := runPreludeCompat(t, c, false, strings.NewReader(d10BlankPlaceholderStream(count, true)), 0, 0)
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.Contains(t, string(failure.ResponseBody), "server_is_overloaded")
		require.Empty(t, recorder.Body.String())
	}
}

func runBlankPlaceholderAttempt(svc *OpenAIGatewayService, c *gin.Context, account *Account, passthrough bool, body io.ReadCloser, start time.Time) (*int, error) {
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}
	if passthrough {
		result, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, start, "model", "model")
		if result != nil {
			return result.firstTokenMs, err
		}
		return nil, err
	}
	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, start, "model", "model")
	if result != nil {
		return result.firstTokenMs, err
	}
	return nil, err
}

func TestResponsesBlankPlaceholderVisibleItemFlushesAllBeforeTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", passthrough), func(t *testing.T) {
			// The two empty items interleave, and item 1 has no item.added.
			// Its .done carries all text and must release both items immediately.
			prelude := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"a\",\"output_index\":0,\"delta\":\" \"}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"b\",\"output_index\":1,\"delta\":\"\\t\"}\n\n"
			prelude = strings.Split(d10BlankPlaceholderStream(2, true), "event: response.failed")[0] + prelude
			metadata := "event: response.metadata\ndata: {\"type\":\"response.metadata\",\"response_id\":\"resp_test\",\"sequence_number\":2,\"metadata\":{}}\n\n"
			prelude = strings.Replace(prelude, "event: response.output_item.added", metadata+"event: response.output_item.added", 1)
			visible := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"b\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\" hi\"}]}}\n\n"
			completed := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"
			c, _ := compatStreamContext("/v1/responses", `{}`)
			recorder := newOpenAIResponseFlushRecorder()
			recordedContext, _ := gin.CreateTestContext(recorder)
			c.Writer = recordedContext.Writer
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			result := make(chan error, 1)
			go func() {
				_, err := runBlankPlaceholderAttempt(&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}, c,
					&Account{ID: 9025, Platform: PlatformOpenAI}, passthrough, reader, time.Now())
				result <- err
			}()
			_, err := io.WriteString(writer, prelude+visible)
			require.NoError(t, err)
			select {
			case <-recorder.flushEvents:
			case <-time.After(3 * time.Second):
				t.Fatal("visible item did not flush until terminal")
			}
			body, _ := recorder.snapshot()
			require.Equal(t, prelude+visible, body, "staging preserves output-path bytes and item order")
			_, err = io.WriteString(writer, completed)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			require.NoError(t, <-result)
			require.False(t, state.FirstOutput().IsZero())
			require.False(t, state.SemanticOutputAt().IsZero())
		})
	}
}

func TestResponsesBlankPlaceholderNonRetryableFailureKeepsOnlyFailureFrame(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		c, recorder := compatStreamContext("/v1/responses", `{}`)
		body := strings.ReplaceAll(d10BlankPlaceholderStream(2, true), "server_is_overloaded", "content_policy_violation")
		body = strings.ReplaceAll(body, "service_unavailable_error", "invalid_request_error")
		_, err := runBlankPlaceholderAttempt(&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}, c,
			&Account{ID: 9026, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, passthrough, io.NopCloser(strings.NewReader(body)), time.Now())
		require.Error(t, err)
		out := recorder.Body.String()
		require.True(t, strings.HasPrefix(out, "event: response.failed\ndata: "), out)
		require.True(t, strings.HasSuffix(out, "\n\n"), out)
		require.NotContains(t, out, "response.created")
		require.NotContains(t, out, "response.in_progress")
		require.NotContains(t, out, "response.output_item")
	}
}

func TestResponsesBlankPlaceholderTimeoutCountsOnlyStagedAttempts(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", passthrough), func(t *testing.T) {
			c, recorder := compatStreamContext("/v1/responses", `{}`)
			core, logs := observer.New(zap.InfoLevel)
			c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
			account := &Account{ID: 9027, Platform: PlatformOpenAI}
			for _, blank := range []bool{false, true} {
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				body := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
				if blank {
					body = strings.Split(d10BlankPlaceholderStream(2, true), "event: response.failed")[0]
				}
				written := make(chan error, 1)
				go func() { _, err := io.WriteString(writer, body); written <- err }()
				_, err := runBlankPlaceholderAttempt(svc, c, account, passthrough, reader, time.Now())
				requireFirstOutputFailover(t, err)
				require.NoError(t, <-written)
				require.Empty(t, recorder.Body.String())
				staged, timeouts, marked := OpenAIBlankPlaceholderOutcomeCounts(c)
				require.Equal(t, blank, marked)
				if blank {
					require.Equal(t, 1, staged)
					require.Equal(t, 1, timeouts)
				}
			}
			account.ID++
			_, err := runBlankPlaceholderAttempt(svc, c, account, passthrough, io.NopCloser(strings.NewReader(d10BlankPlaceholderStream(1, false))), time.Now())
			require.NoError(t, err)
			staged, timeouts, marked := OpenAIBlankPlaceholderOutcomeCounts(c)
			require.True(t, marked)
			require.Equal(t, 2, staged)
			require.Equal(t, 1, timeouts, "next-hop success does not add a placeholder timeout")
			events := logs.FilterMessage("resilience.blank_placeholder_staged").All()
			require.Len(t, events, 2, "one event per attempt, not per placeholder frame")
			require.EqualValues(t, 9027, events[0].ContextMap()["account_id"])
			require.EqualValues(t, 9028, events[1].ContextMap()["account_id"])
		})
	}
}

func TestResponsesBlankPlaceholderStageResetDropsMemoryAndSpool(t *testing.T) {
	for _, size := range []int{1024, 70 << 10} {
		stage := newDefaultOpenAIFirstOutputStage()
		t.Cleanup(func() { _ = stage.Close() })
		_, err := stage.WriteString(strings.Repeat(" ", size))
		require.NoError(t, err)
		require.NoError(t, stage.Reset())
		require.Zero(t, stage.Buffered())
		_, err = stage.WriteString("failure only")
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, stage.CommitTo(&out))
		require.Equal(t, "failure only", out.String())
	}
}

func TestResponsesBlankPlaceholderSuccessLatencyUsesRealOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, visible := range []bool{false, true} {
		c, state := newSSEDeliveryContext(t, "text/event-stream")
		account := &Account{ID: 9029, Platform: PlatformOpenAI}
		start := time.Now().Add(-11 * time.Second)
		state.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: start, Warm: true, Stream: true})
		_, err := c.Writer.WriteString("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" \"}\n\n")
		require.NoError(t, err)
		require.True(t, state.FirstOutput().IsZero())
		if visible {
			_, err = c.Writer.WriteString("event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"text\":\" result\"}\n\n")
			require.NoError(t, err)
		}
		_, err = c.Writer.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
		require.NoError(t, err)
		svc := &OpenAIGatewayService{}
		svc.ReportOpenAIAttemptSuccess(c.Request.Context(), account, "model")
		latency := svc.getOpenAIAccountLatencyHoldState()
		latency.mu.Lock()
		entry, sampled := latency.entries[openAIAccountModelKey{AccountID: account.ID, Model: "model"}]
		latency.mu.Unlock()
		require.Equal(t, visible, sampled)
		if visible {
			require.Equal(t, float64(1), entry.weightedSamples)
			require.Equal(t, float64(1), entry.weightedSlow, "sample is real output minus this attempt's start")
			require.GreaterOrEqual(t, *state.AttemptFirstOutputMs(), int64(11000))
		} else {
			require.Nil(t, state.AttemptFirstOutputMs())
		}
	}
}
