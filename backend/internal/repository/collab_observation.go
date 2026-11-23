package repository

import "github.com/liulixin-lex/xy2api/internal/resilience"

// Invalid optional metadata is SQL NULL. Validate and bound it before the
// existing insert so it cannot fail the usage row or billing transaction.
func collabObservationSQL(raw []byte) any {
	raw = resilience.NormalizeCollabObservationJSON(raw)
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
