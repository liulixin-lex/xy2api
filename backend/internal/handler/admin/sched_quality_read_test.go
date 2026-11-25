//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestReadCurrentVersionWithEmptyDecisionHistory(t *testing.T) {
	h, _, _ := schedAdminHandlerFixture()
	router := schedAdminHandlerRouter(h)
	detail := schedAdminHandlerData(t, schedAdminHandlerRequest(router, "GET", "/accounts/1", ""))
	var version string
	require.NoError(t, json.Unmarshal(detail["current_rule_version"], &version))
	require.Equal(t, q.RuleVersion, version)
	decisions := schedAdminHandlerRequest(router, "GET", "/accounts/1/decisions", "")
	require.Equal(t, 200, decisions.Code)
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(decisions.Body.Bytes(), &envelope))
	require.JSONEq(t, `[]`, string(envelope.Data))
}
