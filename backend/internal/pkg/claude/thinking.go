package claude

import (
	"regexp"
	"strconv"
	"strings"
)

var thinkingModelVersion = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable|mythos)-([0-9]{1,2})(?:[-.]([0-9]{1,2}))?(?:[-@][0-9]{8})?(?:-v[0-9]+:[0-9]+)?$`)

// ThinkingModeForModel chooses adaptive thinking on Claude 4.6 and later;
// older models use enabled thinking with an explicit token budget. Callers
// supply output_config.effort or budget_tokens for the chosen mode.
func ThinkingModeForModel(model string) string {
	if claudeVersionAtLeast(model, 4, 6) {
		return "adaptive"
	}
	return "enabled"
}

// SupportsDisabledThinking reports whether reasoning_effort=none can turn
// thinking off. Claude 5+ and Mythos Preview keep thinking enabled.
func SupportsDisabledThinking(model string) bool {
	return !claudeVersionAtLeast(model, 5, 0)
}

// IsClaudeModel recognizes normalized Claude IDs, including provider prefixes.
// Conversion policies use it to leave other providers' native behavior intact.
func IsClaudeModel(model string) bool {
	return strings.HasPrefix(normalizeEffortModelID(model), "claude-")
}

// RejectsNonDefaultSampling describes the OpenAI SDK compatibility sampling
// limits for Claude 4.7+ and Mythos Preview. Native Messages callers retain
// their own validation; this predicate does not rewrite their requests.
func RejectsNonDefaultSampling(model string) bool {
	return claudeVersionAtLeast(model, 4, 7)
}

func claudeVersionAtLeast(model string, major, minor int) bool {
	id := normalizeEffortModelID(model)
	if id == "claude-mythos-preview" {
		return true
	}
	match := thinkingModelVersion.FindStringSubmatch(id)
	if match == nil {
		return false
	}
	gotMajor, _ := strconv.Atoi(match[1])
	gotMinor, _ := strconv.Atoi(match[2])
	return gotMajor > major || gotMajor == major && gotMinor >= minor
}
