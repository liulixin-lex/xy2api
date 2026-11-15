package claude

import "strings"

type Claude52Spec struct {
	ID, DefaultEffort                        string
	PreserveSignedThinking, ForcedToolChoice bool
}

func Claude52(model string) (Claude52Spec, bool) {
	// The shared normalizer also strips arbitrary dates; 5.2 has no dated aliases.
	raw := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "-thinking")
	if !strings.HasSuffix(raw, "claude-fable-5-2") && !strings.HasSuffix(raw, "claude-mythos-5-2") {
		return Claude52Spec{}, false
	}
	id := normalizeEffortModelID(model)
	switch id {
	case "claude-fable-5-2", "claude-mythos-5-2":
		return Claude52Spec{ID: id, DefaultEffort: "high", PreserveSignedThinking: true}, true
	}
	return Claude52Spec{}, false
}
