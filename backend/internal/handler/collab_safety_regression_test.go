//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var collabResponseFault func(*http.Request, *http.Response)
var collabUserLoadFault func(context.Context) error
var collabIngressTimeout time.Duration

func collabSafetyReplay(t *testing.T) {
	t.Helper()
	quietReplayLogs(t)
	replayGroupLookup = true
	t.Cleanup(func() {
		replayGroupLookup = false
		collabResponseFault = nil
		collabUserLoadFault = nil
		collabIngressTimeout = 0
	})
	setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
		h.collabRescuable = collabCanRescue
		h.collabRescue = h.rescueCollab
	})
}
func collabSafetyCase(t *testing.T, name string) replayCase {
	t.Helper()
	for _, c := range collaborationReplayCorpus() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing fixture %s", name)
	return replayCase{}
}

type collabPanicCloser struct {
	io.ReadCloser
	triggered *bool
}

func (b *collabPanicCloser) Close() error {
	_ = b.ReadCloser.Close()
	*b.triggered = true
	panic("synthetic cleanup failure")
}
func TestCollabRegressionDeliveredCleanupSettlement(t *testing.T) {
	collabSafetyReplay(t)
	for _, lossless := range []bool{false, true} {
		cfg := resilience.Defaults()
		cfg.Collab.Ladder = true
		cfg.Collab.LosslessFirst = lossless
		c := collabSafetyCase(t, "unnamed_task/pass_false/stream_true/attempts_1")
		collabResponseFault = nil
		control := runCollaborationReplay(t, c, cfg).Turns[0]
		require.True(t, control.Success)
		require.Len(t, control.UsageAccounts, 1)
		triggered := false
		collabResponseFault = func(req *http.Request, resp *http.Response) {
			if resilience.CollabAttemptFrom(req.Context()).Optional && resp != nil && resp.StatusCode == 200 {
				resp.Body = &collabPanicCloser{resp.Body, &triggered}
			}
		}
		got := runCollaborationReplay(t, c, cfg).Turns[0]
		require.True(t, triggered)
		require.True(t, got.Success)
		require.Equal(t, 1, got.TerminalCount)
		require.Equal(t, control.UsageAccounts, got.UsageAccounts, "lossless=%t", lossless)
		require.Zero(t, got.UnbilledCount)
	}
}
func TestCollabRegressionLosslessEligibilityBound(t *testing.T) {
	collabSafetyReplay(t)
	c := collabSafetyCase(t, "task_then_reasoning_elsewhere/pass_false/stream_true/attempts_4")
	cfg := resilience.Defaults()
	require.True(t, runCollaborationReplay(t, c, cfg).Turns[0].Success)
	cfg.Collab.Ladder = true
	cfg.Collab.LosslessFirst = true
	collabIngressTimeout = 120 * time.Second
	rejected, queried := false, false
	collabResponseFault = func(req *http.Request, resp *http.Response) {
		if resp != nil && resp.StatusCode >= 400 && !resilience.CollabAttemptFrom(req.Context()).Optional {
			rejected = true
		}
	}
	collabUserLoadFault = func(ctx context.Context) error {
		if !rejected || queried {
			return nil
		}
		queried = true
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(70 * time.Second):
			return nil
		}
	}
	got := runCollaborationReplay(t, c, cfg).Turns[0]
	require.True(t, queried)
	require.True(t, got.Success)
	require.LessOrEqual(t, got.AcceptedMillis, int64(60000))
	require.LessOrEqual(t, got.TerminalMillis, int64(60000))
}
func TestCollabRegressionContinuationEOFIdentity(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := newCollabSeamWriter(c.Writer, seamTestAlways)
	c.Writer = w
	c.Header("Content-Type", "text/event-stream")
	_, _ = w.WriteString(strings.Replace(seamTestCreated, `"response":`, `"sequence_number":4,"response":`, 1))
	_, _ = w.WriteString(seamTestFailed)
	w.suspend()
	_, _ = w.WriteString(strings.Replace(seamTestDelta, `"delta":`, `"sequence_number":0,"delta":`, 1))
	raw := "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"id\":\"resp_wrong\",\"status\":\"completed\"}}\n"
	_, err := w.WriteString(raw)
	require.NoError(t, err)
	w.release()
	events, err := sse.Decode(strings.NewReader(rec.Body.String()))
	require.NoError(t, err)
	require.Len(t, events, 3)
	for i, event := range events {
		payload, ok := event.Data.(string)
		require.True(t, ok)
		require.True(t, json.Valid([]byte(payload)))
		require.Equal(t, int64(i+4), gjson.Get(payload, "sequence_number").Int())
		if i == 2 {
			require.Equal(t, "resp_a", gjson.Get(payload, "response.id").String())
		}
	}
	if strings.Contains(rec.Body.String(), "resp_wrong") {
		t.Error("unfinished continuation flushed raw with replacement ID/sequence")
	}
}

func TestCollabRegressionJSONWhitespaceMustStayValid(t *testing.T) {
	for _, tail := range []string{" ", "\t", "\r\n", "\v", "\u00a0"} {
		for _, asString := range []bool{false, true} {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/", nil)
			valid := tail == " " || tail == "\t" || tail == "\r\n"
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Header("Content-Type", "application/json")
				_, err := c.Writer.WriteString(seamTestJSON)
				require.NoError(t, err)
				if asString {
					_, err = c.Writer.WriteString(tail)
				} else {
					_, err = c.Writer.Write([]byte(tail))
				}
				if valid {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, http.ErrBodyNotAllowed)
				}
			}, seamTestExistingError(c))
			require.True(t, json.Valid(rec.Body.Bytes()), "tail=%q string=%t", tail, asString)
		}
	}
}
