package service

import (
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"golang.org/x/text/width"
)

func normalizeGroupAlias(alias string) (string, error) {
	alias = strings.TrimSpace(alias)
	halfWidths := 0
	for _, r := range alias {
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			halfWidths += 2
		default:
			halfWidths++
		}
		if halfWidths > 4 {
			return "", infraerrors.BadRequest("INVALID_GROUP_ALIAS", "alias display width must be at most 2")
		}
	}
	return alias, nil
}
