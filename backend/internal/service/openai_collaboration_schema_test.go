package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCollaborationSchemaReservedNamespaceUntouched(t *testing.T) {
	fixture, err := os.ReadFile("testdata/codex0166_collaboration_namespace.json")
	require.NoError(t, err)
	for _, decl := range []string{
		`{"tools":[` + string(fixture) + `]}`,
		`{"input":[{"type":"additional_tools","tools":[` + string(fixture) + `]}]}`,
	} {
		out, changed, err := sanitizeOpenAIResponsesToolSchemasForPlatform([]byte(decl), PlatformOpenAI)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, decl, string(out))
		var obj map[string]any
		require.NoError(t, json.Unmarshal(out, &obj))
		require.False(t, normalizeCodexTools(obj))
		normalized, err := json.Marshal(obj)
		require.NoError(t, err)
		require.JSONEq(t, decl, string(normalized))
	}
	// Synthetic schema needing every repair; all leaves (including wait/list)
	// are exempt only inside the exact reserved namespace.
	schema := `{"type":null,"required":null,"properties":{"x":{"type":"string","pattern":"(?=x)"}}}`
	body := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"wait_agent","parameters":` + schema + `}]},{"type":"namespace","name":"other","tools":[{"type":"function","name":"wait_agent","parameters":` + schema + `}]}]}`)
	out, changed, err := sanitizeOpenAIResponsesToolSchemasForPlatform(body, PlatformOpenAI)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, schema, gjson.GetBytes(out, "tools.0.tools.0.parameters").Raw)
	require.Equal(t, "object", gjson.GetBytes(out, "tools.1.tools.0.parameters.type").String())
	require.False(t, gjson.GetBytes(out, "tools.1.tools.0.parameters.required").Exists())
	require.False(t, gjson.GetBytes(out, "tools.1.tools.0.parameters.properties.x.pattern").Exists())
	union := []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"list_agents","parameters":{"oneOf":[{"type":"object"}],"required":null}}]}]}]}`)
	out, changed, err = sanitizeOpenAIResponsesToolSchemasForPlatform(union, PlatformOpenAI)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, union, out)
}
