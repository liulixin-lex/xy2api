package resilience

import (
	"context"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
)

// RecordConversionNotices records ordered, unique field/action pairs.
func RecordConversionNotices(ctx context.Context, notices ...apicompat.ConversionNotice) {
	s := StateFrom(ctx)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gatewayRecord != nil && s.gatewayRecord.final != nil {
		return
	}
	for _, n := range notices {
		if strings.TrimSpace(n.Field) == "" {
			continue
		}
		switch n.Action {
		case "omit", "map", "default":
		default:
			continue
		}
		duplicate := false
		for _, old := range s.conversionNotices {
			if old == n {
				duplicate = true
				break
			}
		}
		if !duplicate {
			s.conversionNotices = append(s.conversionNotices, n)
		}
	}
}

func SnapshotConversionNotices(ctx context.Context) []apicompat.ConversionNotice {
	s := StateFrom(ctx)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apicompat.ConversionNotice(nil), s.conversionNotices...)
}
