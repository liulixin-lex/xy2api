package repository

import "encoding/json"

func nullableProfitBasis(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
