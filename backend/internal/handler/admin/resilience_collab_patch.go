package admin

import (
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type collabAdminPatch struct{}

func (*collabAdminPatch) UnmarshalJSON(data []byte) error {
	return resilience.CheckCollabSettingsJSON(data)
}

// Check each repeated section before the tolerant decoder can hide bad input.
func checkCollabPatch(body []byte) error {
	var fields struct {
		Collab collabAdminPatch `json:"collab"`
	}
	return json.Unmarshal(body, &fields)
}
