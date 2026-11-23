//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const localEndingNotice = "Gateway notice [cle_example]. The task content could not be read. This turn ended before work began. A readable copy is needed to retry."

func localEndingState(created bool) CollabLocalEndingResponseState {
	state := CollabLocalEndingResponseState{CreatedDelivered: created, ResponseID: "resp_example", CreatedAt: 42}
	if created {
		state.NextSequence = 5
	}
	return state
}

func TestCollabLocalEndingEventNumber(t *testing.T) {
	id, err := newCollabLocalID("cle_", bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)))
	require.NoError(t, err)
	require.Equal(t, "cle_abababababababababababababababab", id)
	id, err = newCollabLocalID("cle_", bytes.NewReader([]byte{1}))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Empty(t, id)
	seen := map[string]bool{}
	for range 32 {
		id, err = NewCollabLocalEndingEvent()
		require.NoError(t, err)
		require.Regexp(t, `^cle_[0-9a-f]{32}$`, id)
		require.False(t, seen[id])
		seen[id] = true
	}
}

func TestCollabLocalEndingGolden(t *testing.T) {
	for _, created := range []bool{false, true} {
		t.Run(fmt.Sprint(created), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(&localEndingFaultWriter{ResponseRecorder: recorder})
			name := "testdata/collab_local_ending.sse"
			if created {
				name = "testdata/collab_local_ending_existing.sse"
			}
			golden, err := os.ReadFile(name)
			require.NoError(t, err)
			claims := 0
			complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, localEndingState(created), "example-model", localEndingNotice, true, func() bool {
				claims++
				require.NotContains(t, recorder.Body.String(), "response.completed")
				require.True(t, recorder.Flushed)
				return ClaimProtocolTerminal(c)
			})
			require.NoError(t, err)
			require.True(t, complete)
			require.Equal(t, 1, claims)
			require.True(t, ProtocolTerminalClaimed(c))
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
			frames := strings.Split(strings.TrimSuffix(recorder.Body.String(), "\n\n"), "\n\n")
			wantFrames := strings.Split(strings.TrimSuffix(string(golden), "\n\n"), "\n\n")
			require.Len(t, frames, len(wantFrames))
			for i := range frames {
				require.Equal(t, wantFrames[i], frames[i], "frame %d", i)
			}
			require.Equal(t, string(golden), recorder.Body.String())
			require.NotContains(t, recorder.Body.String(), "usage")
			require.NotContains(t, recorder.Body.String(), "[DONE]")
		})
	}
}

func TestCollabLocalEndingParsersAcceptCompletedWithoutUsage(t *testing.T) {
	for _, created := range []bool{false, true} {
		t.Run(fmt.Sprint(created), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer := &localEndingFaultWriter{ResponseRecorder: recorder}
			c, _ := gin.CreateTestContext(writer)
			if created {
				c.Writer.Header().Set("Content-Type", "text/event-stream")
				_, err := c.Writer.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":4,\"response\":{\"id\":\"resp_example\",\"object\":\"response\",\"created_at\":42,\"model\":\"example-model\",\"status\":\"in_progress\",\"output\":[]}}\n\n"))
				require.NoError(t, err)
				c.Writer.Flush()
			}
			complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, localEndingState(created), "example-model", localEndingNotice, true, func() bool { return ClaimProtocolTerminal(c) })
			require.NoError(t, err)
			require.True(t, complete)
			body := recorder.Body.String()
			acc := apicompat.NewBufferedResponseAccumulator()
			chat := apicompat.NewResponsesEventToChatState()
			var text string
			var terminalCount, finishCount int
			lastSequence := int64(-1)
			forEachOpenAIResponsesFrame(body, func(event string, data []byte) {
				var parsed apicompat.ResponsesStreamEvent
				require.NoError(t, json.Unmarshal(data, &parsed))
				require.Equal(t, event, parsed.Type)
				require.Greater(t, int64(parsed.SequenceNumber), lastSequence)
				lastSequence = int64(parsed.SequenceNumber)
				require.Nil(t, parsed.Usage)
				if parsed.Response != nil {
					require.Nil(t, parsed.Response.Usage)
				}
				acc.ProcessEvent(&parsed)
				for _, chunk := range apicompat.ResponsesEventToChatChunks(&parsed, chat) {
					for _, choice := range chunk.Choices {
						if choice.Delta.Content != nil {
							text += *choice.Delta.Content
						}
						if choice.FinishReason != nil {
							require.Equal(t, "stop", *choice.FinishReason)
							finishCount++
						}
					}
				}
				terminal := ClassifyOpenAIResponsesTerminal(event, data)
				if terminal.Seen {
					terminalCount++
					require.True(t, terminal.Valid && terminal.Success && !terminal.Failure)
					require.False(t, terminal.QualityFailure())
					require.Equal(t, "completed", terminal.Status)
				}
				_, present := openAIResponsesUsageEvidence(data, "upstream_sse_terminal")
				require.False(t, present)
			})
			require.NoError(t, acc.Err())
			require.NoError(t, chat.Err())
			require.Equal(t, localEndingNotice, text)
			require.Equal(t, 1, finishCount)
			require.Equal(t, 1, terminalCount)
			output := acc.BuildOutput()
			require.Len(t, output, 1)
			require.Len(t, output[0].Content, 1)
			require.Equal(t, localEndingNotice, output[0].Content[0].Text)
			terminal, event, data := extractOpenAISSETerminalEvent(body)
			require.Equal(t, "response.completed", event)
			response, ok := openAIResponsesTerminalResponse(terminal, data)
			require.True(t, ok)
			require.Equal(t, localEndingNotice, gjson.GetBytes(response, "output.0.content.0.text").String())
			require.False(t, gjson.GetBytes(response, "usage").Exists())
		})
	}
}

var errLocalEndingWrite = errors.New("synthetic delivery failure")

// It sits beneath gin. A failed flush cancels the request, as the server does.
type localEndingFaultWriter struct {
	*httptest.ResponseRecorder
	writes, flushes int
	failAt          int
	kind            string
	cancel          context.CancelCauseFunc
}

func (w *localEndingFaultWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		switch w.kind {
		case "write":
			return 0, errLocalEndingWrite
		case "partial":
			return len(p) / 2, errLocalEndingWrite
		case "short":
			return len(p) - 1, nil
		}
	}
	return w.ResponseRecorder.Write(p)
}

func (w *localEndingFaultWriter) Flush() {
	w.flushes++
	if w.kind == "flush" && w.flushes == w.failAt {
		w.cancel(errLocalEndingWrite)
		return
	}
	w.ResponseRecorder.Flush()
}

func TestCollabLocalEndingStopsAtEveryDeliveryFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, created := range []bool{false, true} {
			if !stream && created {
				continue
			}
			frames := 1
			if stream {
				frames = 8
				if created {
					frames--
				}
			}
			for _, kind := range []string{"write", "partial", "short", "flush"} {
				for failAt := 1; failAt <= frames; failAt++ {
					t.Run(fmt.Sprintf("stream_%v_created_%v_%s_%d", stream, created, kind, failAt), func(t *testing.T) {
						ctx, cancel := context.WithCancelCause(t.Context())
						defer cancel(nil)
						w := &localEndingFaultWriter{ResponseRecorder: httptest.NewRecorder(), failAt: failAt, kind: kind, cancel: cancel}
						c, _ := gin.CreateTestContext(w)
						complete, err := WriteCollabLocalEnding(ctx, c.Writer, localEndingState(created), "example-model", localEndingNotice, stream, func() bool { return ClaimProtocolTerminal(c) })
						require.False(t, complete, "termination records require complete delivery")
						want := errLocalEndingWrite
						if kind == "short" {
							want = io.ErrShortWrite
						}
						require.ErrorIs(t, err, want)
						require.Equal(t, failAt, w.writes)
						flushes := failAt - 1
						if kind == "flush" {
							flushes++
						}
						require.Equal(t, flushes, w.flushes)
						require.Equal(t, failAt == frames, ProtocolTerminalClaimed(c), "final failure retains the terminal claim")
					})
				}
			}
		}
	}
}

func TestCollabLocalEndingJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Length", "1")
	c, _ := gin.CreateTestContext(&localEndingFaultWriter{ResponseRecorder: recorder})
	complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, localEndingState(false), "example-model", localEndingNotice, false, func() bool { return ClaimProtocolTerminal(c) })
	require.NoError(t, err)
	require.True(t, complete)
	require.True(t, recorder.Flushed)
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Get("Content-Length"))
	golden, err := os.ReadFile("testdata/collab_local_ending.json")
	require.NoError(t, err)
	require.Equal(t, strings.TrimSuffix(string(golden), "\n"), recorder.Body.String())
	var response apicompat.ResponsesResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "completed", response.Status)
	require.Nil(t, response.Usage)
	_, present := openAIResponsesUsageEvidence(recorder.Body.Bytes(), "upstream_json")
	require.False(t, present)
	require.True(t, openAIM3JSONAccepted(recorder.Body.Bytes()))
}

// A production wrapper may have neither Unwrap nor FlushError.
type localEndingOpaqueWriter struct{ gin.ResponseWriter }

func TestCollabLocalEndingWorksThroughOpaqueWrappers(t *testing.T) {
	for _, stream := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Writer = localEndingOpaqueWriter{c.Writer}
		complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, localEndingState(false), "example-model", localEndingNotice, stream, func() bool { return true })
		require.NoError(t, err)
		require.True(t, complete)
		require.True(t, recorder.Flushed)
	}
}

func TestCollabLocalEndingCanceledClientWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	complete, err := WriteCollabLocalEnding(ctx, c.Writer, localEndingState(false), "example-model", localEndingNotice, true, func() bool {
		t.Fatal("a canceled client must not claim the terminal")
		return false
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, complete)
	require.Empty(t, recorder.Body.String())
	require.False(t, c.Writer.Written())
}

// The notice becomes permanent client history and is replayed upstream.
func TestCollabLocalEndingMessageIDIsReplayable(t *testing.T) {
	for _, responseID := range []string{"", "resp_example", "chatcmpl-x", strings.Repeat("r", 300)} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		state := CollabLocalEndingResponseState{CreatedDelivered: responseID != "", ResponseID: responseID}
		complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, state, "example-model", localEndingNotice, true, func() bool { return true })
		require.NoError(t, err)
		require.True(t, complete)
		_, _, payload := extractOpenAISSETerminalEvent(recorder.Body.String())
		require.Regexp(t, `^msg_[0-9a-f]{32}$`, gjson.GetBytes(payload, "response.output.0.id").String())
	}
}

func TestCollabLocalEndingTerminalClaimAndState(t *testing.T) {
	for _, stream := range []bool{false, true} {
		w := &localEndingFaultWriter{ResponseRecorder: httptest.NewRecorder()}
		c, _ := gin.CreateTestContext(w)
		var claimed atomic.Bool
		claimed.Store(true)
		complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, localEndingState(false), "example-model", localEndingNotice, stream, func() bool {
			if stream {
				require.Equal(t, 7, w.flushes)
			}
			return claimed.CompareAndSwap(false, true)
		})
		require.Error(t, err)
		require.False(t, complete)
		require.NotContains(t, w.Body.String(), "response.completed")
		if !stream {
			require.Zero(t, w.writes)
		}
	}
	for _, state := range []CollabLocalEndingResponseState{
		{CreatedDelivered: true}, {NextSequence: -1}, {NextSequence: math.MaxInt64},
	} {
		w := &localEndingFaultWriter{ResponseRecorder: httptest.NewRecorder()}
		c, _ := gin.CreateTestContext(w)
		complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, state, "example-model", localEndingNotice, true, func() bool { t.Fatal("unexpected terminal claim"); return false })
		require.Error(t, err)
		require.False(t, complete)
		require.Empty(t, w.Body.String())
	}
	// A fresh response gets an ID and timestamp; an existing one preserves even 0.
	for _, state := range []CollabLocalEndingResponseState{{}, {CreatedDelivered: true, ResponseID: "resp_existing", NextSequence: 1}} {
		w := &localEndingFaultWriter{ResponseRecorder: httptest.NewRecorder()}
		c, _ := gin.CreateTestContext(w)
		complete, err := WriteCollabLocalEnding(t.Context(), c.Writer, state, "example-model", localEndingNotice, true, func() bool { return ClaimProtocolTerminal(c) })
		require.NoError(t, err)
		require.True(t, complete)
		_, _, payload := extractOpenAISSETerminalEvent(w.Body.String())
		if state.CreatedDelivered {
			require.Equal(t, state.ResponseID, gjson.GetBytes(payload, "response.id").String())
			require.Equal(t, int64(0), gjson.GetBytes(payload, "response.created_at").Int())
		} else {
			require.Regexp(t, `^resp_[0-9a-f]{32}$`, gjson.GetBytes(payload, "response.id").String())
			require.Positive(t, gjson.GetBytes(payload, "response.created_at").Int())
		}
	}
	// The notice selector and writer agree on the exact payload used by goldens.
	require.Equal(t, localEndingNotice, resilience.CollabLocalEndingText("cle_example", resilience.CollabRoleChild, resilience.CollabExecutionNotStarted, false, false))
}
