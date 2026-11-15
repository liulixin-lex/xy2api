package service

import (
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"strings"
)

// Unknown result facts never overwrite observations retained in request metadata.
func applyUsageResponseFacts(row *UsageLog, known, present bool, source string, seen, valid bool, kind string) {
	if row == nil || !known {
		return
	}
	row.UsageObserved = &present
	row.UsageSource = &source
	row.TerminalEventSeen = &seen
	row.TerminalEventValid = &valid
	row.ResponseBodyType = &kind
}

func cloneUsageFactValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyConversionNotices(notices []apicompat.ConversionNotice) []apicompat.ConversionNotice {
	var result []apicompat.ConversionNotice
	seen := make(map[apicompat.ConversionNotice]bool, len(notices))
	for _, notice := range notices {
		notice.Field = strings.TrimSpace(notice.Field)
		if notice.Field == "" || notice.Action != "omit" && notice.Action != "map" && notice.Action != "default" || seen[notice] {
			continue
		}
		seen[notice] = true
		result = append(result, notice)
	}
	return result
}
