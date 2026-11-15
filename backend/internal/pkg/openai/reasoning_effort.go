package openai

import "fmt"

// NearestSupportedReasoningEffort applies ordinal distance, preferring lower ties.
func NearestSupportedReasoningEffort(effort string, supported []string) (string, error) {
	levels := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	index := func(value string) int {
		for i, v := range levels {
			if v == value {
				return i
			}
		}
		return -1
	}
	requested := index(effort)
	if requested < 0 || len(supported) == 0 {
		return "", fmt.Errorf("invalid reasoning effort or empty supported set")
	}
	best, distance := -1, len(levels)+1
	for _, value := range supported {
		i := index(value)
		if i < 0 {
			return "", fmt.Errorf("invalid supported reasoning effort %q", value)
		}
		d := i - requested
		if d < 0 {
			d = -d
		}
		if d < distance || d == distance && i < best {
			best, distance = i, d
		}
	}
	return levels[best], nil
}
