package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// nativeResponsesPreAnswerBoundary reports the first event that either gives
// the client an answer or makes replay unsafe. Metadata and reasoning remain
// local until this boundary so an account that only thinks and then stalls can
// be replaced without exposing its response identity.
func nativeResponsesPreAnswerBoundary(payload []byte) bool {
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) || !gjson.ValidBytes(payload) {
		return true
	}
	typ := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	_, answer, terminal, tool := classifySemanticEvent(payload)
	if answer || terminal || tool {
		return true
	}
	switch typ {
	case "response.created", "response.in_progress", "keepalive",
		"response.reasoning_text.delta", "response.reasoning_text.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return false
	case "response.output_item.added", "response.output_item.done":
		return strings.TrimSpace(gjson.GetBytes(payload, "item.type").String()) != "reasoning"
	case "response.content_part.added", "response.content_part.done":
		part := gjson.GetBytes(payload, "part")
		kind := strings.TrimSpace(part.Get("type").String())
		if kind == "summary_text" {
			return false
		}
		if kind == "output_text" || kind == "refusal" {
			return part.Get("text").String() != "" || part.Get("refusal").String() != ""
		}
		return true
	case "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		return true
	}
	// New protocol shapes must establish an explicit safety classification before
	// they become replayable. Conservatively committing them preserves tools and
	// future output types exactly once.
	return true
}

func nativeResponsesFirstAnswerOutput(payload []byte) bool {
	if !gjson.ValidBytes(payload) {
		return false
	}
	_, answer, _, _ := classifySemanticEvent(payload)
	if answer {
		return true
	}
	typ := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	switch typ {
	case "response.reasoning_text.delta", "response.reasoning_text.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return false
	}
	return openAIStreamDataStartsVisibleOutput(string(payload), typ)
}

type nativeFirstAnswerStage struct {
	spool   *openAIFirstOutputStage
	updates []nativeResponsesIdentityUpdate
}

func newNativeFirstAnswerStage() *nativeFirstAnswerStage {
	return &nativeFirstAnswerStage{spool: newDefaultOpenAIFirstOutputStage()}
}

func (s *nativeFirstAnswerStage) Stage(frame, payload []byte) error {
	if s == nil || s.spool == nil {
		return io.ErrClosedPipe
	}
	if _, err := s.spool.Write(frame); err != nil {
		return err
	}
	s.updates = append(s.updates, nativeResponsesIdentityUpdateFromFrame(payload))
	return nil
}

type nativeFirstAnswerStageWriter struct {
	ctx     context.Context
	dst     http.ResponseWriter
	written int
}

func (w *nativeFirstAnswerStageWriter) Write(p []byte) (int, error) {
	n, err := WriteNativeStreamFrame(w.ctx, w.dst, p)
	w.written += n
	return n, err
}

// Commit writes the staged wire bytes only after the caller has committed the
// selected attempt. Identity updates occur after the full spool was accepted by
// the downstream writer, so a failed pre-answer attempt never leaks identity.
func (s *nativeFirstAnswerStage) Commit(ctx context.Context, dst http.ResponseWriter, c *gin.Context) (int, error) {
	if s == nil || s.spool == nil || s.spool.Buffered() == 0 {
		return 0, nil
	}
	w := &nativeFirstAnswerStageWriter{ctx: ctx, dst: dst}
	if err := s.spool.CommitTo(w); err != nil {
		return w.written, err
	}
	for _, update := range s.updates {
		recordNativeResponsesDeliveredIdentityUpdate(c, update)
	}
	s.updates = nil
	return w.written, nil
}

func (s *nativeFirstAnswerStage) Close() error {
	if s == nil || s.spool == nil {
		return nil
	}
	return s.spool.Close()
}
