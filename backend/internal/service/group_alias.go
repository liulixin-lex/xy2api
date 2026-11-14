package service

import (
	"strings"
	"unicode"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"golang.org/x/text/width"
)

func normalizeGroupAlias(alias string) (string, error) {
	if strings.IndexFunc(alias, unicode.IsControl) >= 0 {
		return "", infraerrors.BadRequest("INVALID_GROUP_ALIAS", "alias must not contain control characters")
	}
	alias = strings.TrimSpace(alias)
	// Count thirds: wide/fullwidth runes use 3 units, all others use 2.
	widthUnits := 0
	for _, r := range alias {
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			widthUnits += 3
		default:
			widthUnits += 2
		}
		if widthUnits > 6 {
			return "", infraerrors.BadRequest("INVALID_GROUP_ALIAS", "alias display width must be at most 2")
		}
	}
	return alias, nil
}
