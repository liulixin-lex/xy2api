package claude

import (
	"regexp"
	"strconv"
)

// Match a complete model ID, keeping an eight-digit snapshot date separate
// from the minor version (claude-opus-4-20250514 is Opus 4, not Opus 4.20250514).
var opusSamplingModelID = regexp.MustCompile(`^claude-opus-([0-9]{1,2})(?:[-.]([0-9]{1,2}))?(?:[-@][0-9]{8})?(?:-v[0-9]+:[0-9]+)?$`)

// RejectsSamplingParameters reports whether the model rejects every value of
// temperature, top_p and top_k, even on API version 2023-06-01: Opus 4.7 onward.
func RejectsSamplingParameters(model string) bool {
	version := opusSamplingModelID.FindStringSubmatch(normalizeEffortModelID(model))
	if version == nil {
		return false
	}
	major, _ := strconv.Atoi(version[1])
	minor, _ := strconv.Atoi(version[2])
	return major > 4 || (major == 4 && minor >= 7)
}
