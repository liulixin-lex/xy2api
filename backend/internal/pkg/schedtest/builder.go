// Package schedtest builds synthetic Responses conversations from public example text.
package schedtest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
)

//go:embed corpus/examples.txt
var corpus string

// Session supplies the previous input verbatim; Build never changes it.
// ID must identify one test, and Model is the admitted request model.
type Session struct {
	ID, Model string
	Input     json.RawMessage
	Answer    string
}

var tools = json.RawMessage(`[
 {"type":"function","name":"exec_command","description":"Read example files or run a shell command.","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"],"additionalProperties":false}},
 {"type":"function","name":"apply_patch","description":"Apply a patch to example files.","parameters":{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"],"additionalProperties":false}},
 {"type":"function","name":"update_plan","description":"Update the task plan.","parameters":{"type":"object","properties":{"plan":{"type":"array","items":{"type":"object","properties":{"step":{"type":"string"},"status":{"type":"string"}},"required":["step","status"],"additionalProperties":false}}},"required":["plan"],"additionalProperties":false}}
]`)

type request struct {
	Model           string          `json:"model"`
	Stream          bool            `json:"stream"`
	Store           bool            `json:"store"`
	Reasoning       reasoning       `json:"reasoning"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	PromptCacheKey  string          `json:"prompt_cache_key"`
	Instructions    string          `json:"instructions"`
	Tools           json.RawMessage `json:"tools"`
	Input           json.RawMessage `json:"input"`
}
type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

// Build returns a standard inbound Responses body. count estimates the complete
// request's input tokens using the caller's tokenizer, including tools/instructions.
// Invalid session, tier, turn, or counter returns nil.
func Build(tier schedquality.TestTier, turn int, session Session, count func([]byte) int) []byte {
	if tier < schedquality.TierSmall || tier > schedquality.TierLarge || turn < 1 || turn > schedquality.TestTurnsPerTier || session.ID == "" || session.Model == "" || count == nil {
		return nil
	}
	spec := schedquality.TestTiers[tier]
	key := session.ID + "/" + tier.String()
	req := request{Model: session.Model, Stream: true, Store: false, Reasoning: reasoning{spec.Effort, "auto"}, MaxOutputTokens: schedquality.TestOutputCap, PromptCacheKey: key, Instructions: openai.CodexBaseInstructionsForModel(session.Model), Tools: tools}
	var prefix []json.RawMessage
	offset := 0
	if turn == 1 {
		prefix = append(prefix, marshal(map[string]any{"role": "user", "content": key + "\nReview this generated example project. Read the supplied files and answer the final question briefly; do not invoke tools."}))
	} else {
		if json.Unmarshal(session.Input, &prefix) != nil || len(prefix) == 0 {
			return nil
		}
		var outputs []struct {
			Output string `json:"output"`
		}
		_ = json.Unmarshal(session.Input, &outputs)
		for _, item := range outputs {
			offset += len(item.Output)
		}
		answer := session.Answer
		if answer == "" {
			answer = "The example has been reviewed."
		}
		prefix = append(prefix, marshal(map[string]any{"role": "assistant", "content": answer}))
	}
	target := spec.Tokens
	if turn > 1 {
		req.Input = session.Input
		previous := count(marshal(req))
		if previous <= 0 {
			return nil
		}
		target = previous + schedquality.TestTurnGrowthTokens
	}
	render := func(size int) []byte {
		input := append([]json.RawMessage(nil), prefix...)
		for start, piece := 0, 0; start < size; piece++ {
			end := min(start+16*1024, size)
			callID := fmt.Sprintf("example_%d_%d", turn, piece)
			input = append(input,
				marshal(map[string]any{"type": "function_call", "call_id": callID, "name": "exec_command", "arguments": `{"cmd":"cat examples/parcel_*.go notes/parcel_*.md"}`}),
				marshal(map[string]any{"type": "function_call_output", "call_id": callID, "output": corpusPiece(offset+start, end-start)}))
			start = end
		}
		input = append(input, marshal(map[string]any{"role": "user", "content": "In the latest example, does a disabled entry change the total? Answer in one sentence."}))
		req.Input = marshal(input)
		return marshal(req)
	}
	low, high := 1, len(corpus)
	best := render(low)
	bestN := count(best)
	if bestN <= 0 {
		return nil
	}
	for low <= high {
		mid := low + (high-low)/2
		candidate := render(mid)
		n := count(candidate)
		if n <= 0 {
			return nil
		}
		if abs(n-target) < abs(bestN-target) {
			best, bestN = candidate, n
		}
		if abs(n-target) <= max(1, target/100) {
			return candidate
		}
		if n < target {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best
}

func corpusPiece(offset, size int) string {
	offset %= len(corpus)
	if offset+size <= len(corpus) {
		return corpus[offset : offset+size]
	}
	return corpus[offset:] + corpus[:size-(len(corpus)-offset)]
}
func marshal(v any) []byte { b, _ := json.Marshal(v); return bytes.TrimSpace(b) }
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
