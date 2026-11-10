package claude

import "fmt"

// Claude55Sampling names the sampling parameters a Claude 5.5 model accepts.
type Claude55Sampling int

const (
	// Claude55SamplingUpstream leaves sampling parameters to the upstream.
	Claude55SamplingUpstream Claude55Sampling = iota
	// Claude55SamplingDefaultOnly accepts temperature 1 and top_p 0.99-1;
	// top_k is rejected.
	Claude55SamplingDefaultOnly
	// Claude55SamplingDefaultExclusive accepts temperature 1 or top_p 0.99,
	// not both; top_k is rejected.
	Claude55SamplingDefaultExclusive
)

// Claude55Spec is the request surface of one Claude 5.5 model. The family
// shares adaptive-only thinking, five effort levels and signed thinking
// history; the fields below are the per-model differences.
type Claude55Spec struct {
	ID            string
	DefaultEffort string
	// DisabledThinkingMaxEffort is the highest effort accepted with
	// thinking.type=disabled; empty means disabled is rejected.
	DisabledThinkingMaxEffort string
	BetweenTools              bool
	ForcedToolChoice          bool
	Sampling                  Claude55Sampling
}

var claude55Specs = []Claude55Spec{
	{ID: "claude-opus-5-5", DefaultEffort: "medium", Sampling: Claude55SamplingUpstream},
	{ID: "claude-sonnet-5-5", DefaultEffort: "high", BetweenTools: true, Sampling: Claude55SamplingDefaultOnly},
	{ID: "claude-haiku-5-5", DefaultEffort: "medium", DisabledThinkingMaxEffort: "high", ForcedToolChoice: true, Sampling: Claude55SamplingDefaultExclusive},
}

// Claude55 returns the spec of a Claude 5.5 model, matched by its full ID
// after provider and alias normalization.
func Claude55(model string) (Claude55Spec, bool) {
	id := normalizeEffortModelID(model)
	for _, spec := range claude55Specs {
		if id == spec.ID {
			return spec, true
		}
	}
	return Claude55Spec{}, false
}

// IsClaude55 identifies the Claude 5.5 models.
func IsClaude55(model string) bool {
	_, ok := Claude55(model)
	return ok
}

// IsHaiku55 identifies the fixed Haiku 5.5 ID after provider/local suffix normalization.
func IsHaiku55(model string) bool {
	return normalizeEffortModelID(model) == "claude-haiku-5-5"
}

// SamplingValue is one sampling parameter of a request: whether it is set,
// and its value when it is a JSON number.
type SamplingValue struct {
	Set    bool
	Number bool
	Value  float64
}

// SamplingFloat describes an optional numeric sampling parameter.
func SamplingFloat(v *float64) SamplingValue {
	if v == nil {
		return SamplingValue{}
	}
	return SamplingValue{Set: true, Number: true, Value: *v}
}

// CheckSampling rejects sampling parameters the model does not accept.
func (s Claude55Spec) CheckSampling(temperature, topP SamplingValue, topK bool) error {
	switch s.Sampling {
	case Claude55SamplingDefaultOnly:
		if temperature.Set && (!temperature.Number || temperature.Value != 1) {
			return fmt.Errorf("%s does not support non-default temperature", s.ID)
		}
		if topP.Set && (!topP.Number || topP.Value < 0.99 || topP.Value > 1) {
			return fmt.Errorf("%s does not support non-default top_p", s.ID)
		}
		if topK {
			return fmt.Errorf("%s does not support top_k", s.ID)
		}
	case Claude55SamplingDefaultExclusive:
		if temperature.Set && (!temperature.Number || temperature.Value != 1) {
			return fmt.Errorf("%s does not support non-default temperature; omit temperature or set it to 1", s.ID)
		}
		if topP.Set && (!topP.Number || topP.Value != 0.99) {
			return fmt.Errorf("%s does not support non-default top_p; omit top_p or set it to 0.99", s.ID)
		}
		if topK {
			return fmt.Errorf("%s does not support top_k", s.ID)
		}
		if temperature.Set && topP.Set {
			return fmt.Errorf("%s does not accept temperature and top_p together; omit at least one", s.ID)
		}
	}
	return nil
}
