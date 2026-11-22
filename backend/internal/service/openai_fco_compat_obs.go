package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"go.uber.org/zap"
)

const fcoSuccessSampleEvery uint64 = 16

var fcoSuccessSampleCounter atomic.Uint64

// FCORefusal is a bounded classification of the upstream standalone-output
// rejection. Message is intentionally omitted so callers cannot accidentally
// persist provider text.
type FCORefusal struct {
	Matched bool
	Variant string
}

const (
	fcoRefusalPrefix         = "function_call_output requires call_id on HTTP requests"
	fcoRefusalUnpairedPrefix = "function_call_output requires call_id on HTTP requests; unpaired tool outputs are only accepted on Responses WebSocket v2"
)

// classifyOpenAIFCORefusal recognizes only the documented complete/truncated
// signatures. It never treats an arbitrary suffix as a prefix match.
func classifyOpenAIFCORefusal(status int, payload []byte) FCORefusal {
	if status != 400 {
		return FCORefusal{}
	}
	var envelope struct {
		Error struct {
			Message string          `json:"message"`
			Param   json.RawMessage `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return FCORefusal{}
	}
	var param string
	if json.Unmarshal(envelope.Error.Param, &param) == nil && param != "" && !strings.HasPrefix(param, "input") {
		return FCORefusal{}
	}
	m := strings.TrimSpace(envelope.Error.Message)
	if m == "function_call_output requires call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2" {
		return FCORefusal{Matched: true, Variant: "complete"}
	}
	if m == fcoRefusalPrefix {
		return FCORefusal{Matched: true, Variant: "truncated"}
	}
	if suffix, ok := strings.CutPrefix(m, fcoRefusalUnpairedPrefix); ok {
		if suffix == "" {
			return FCORefusal{Matched: true, Variant: "unpaired"}
		}
		first, _ := utf8.DecodeRuneInString(suffix)
		if strings.ContainsRune(".;,:", first) || unicode.IsSpace(first) {
			return FCORefusal{Matched: true, Variant: "unpaired"}
		}
	}
	return FCORefusal{}
}

// observeOpenAIFCOWire summarizes an actual wire body. The fast path is kept
// at the call site: callers should check for function_call_output before this
// parser on successful responses.
func observeOpenAIFCOWire(body []byte, rules FCOCompatRules) FCOShapeSummary {
	return summarizeOpenAIFCOBody(body, rules)
}

// recordOpenAIFCOShape is shared by the four HTTP/WS paths. Labels are finite.
func recordOpenAIFCOShape(ctxLogger *zap.Logger, summary FCOShapeSummary, outcome string) {
	resilience.Count(resilience.MetricFCOCompat, 0, 0, outcome)
	if ctxLogger != nil {
		ctxLogger.Info("resilience.fco_shape", zap.Any("shape", summary), zap.String("outcome", outcome))
	}
}

// observeOpenAIFCOHTTPError records a refusal using only the sent body shape.
// It is safe to call from any HTTP Responses path; the caller supplies the
// exact body used for that attempt.
func observeOpenAIFCOHTTPError(ctx context.Context, c *gin.Context, body, response []byte, status int, rules FCOCompatRules) {
	refusal := classifyOpenAIFCORefusal(status, response)
	if !refusal.Matched || !bytes.Contains(body, []byte("function_call_output")) {
		return
	}
	summary := observeOpenAIFCOWire(body, rules)
	recordOpenAIFCOShape(logger.FromContext(ctx), summary, "refusal_"+refusal.Variant)
	logger.FromContext(ctx).Info("resilience.fco_shape_refusal", zap.Int("status", status), zap.String("variant", refusal.Variant))
}

// observeOpenAIFCOSuccess performs the deliberately sparse success sample.
// The caller must do the cheap byte prefilter before invoking this function.
func observeOpenAIFCOSuccess(ctx context.Context, body []byte, rules FCOCompatRules) {
	if !bytes.Contains(body, []byte("function_call_output")) {
		return
	}
	if fcoSuccessSampleCounter.Add(1)%fcoSuccessSampleEvery != 0 {
		return
	}
	summary := summarizeOpenAIFCOBodyMode(body, rules, false)
	if summary.IndependentFCOCount == "0" {
		return
	}
	recordOpenAIFCOShape(logger.FromContext(ctx), summary, "success_sample")
}
