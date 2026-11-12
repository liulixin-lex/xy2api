package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicToolSchemaPreservesObject(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]},"id":{"enum":[9007199254740993]}},"required":["value"],"additionalProperties":false}`)
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: json.RawMessage(`"hi"`), Tools: []ResponsesTool{{Type: "function", Name: "lookup", Description: "Find a value", Parameters: schema}}})
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	require.Equal(t, "lookup", out.Tools[0].Name)
	require.Equal(t, "Find a value", out.Tools[0].Description)
	require.JSONEq(t, string(schema), string(out.Tools[0].InputSchema))
}

func TestAnthropicToolSchemasRejectLoss(t *testing.T) {
	for _, schema := range []string{`{"oneOf":[{"type":"object"},{"type":"string"}]}`, `{"anyOf":[{"type":"object"},{"type":"array"}]}`, `{"type":"object","allOf":[{"type":"object"},{"type":"number"}]}`, `{"type":"string"}`, `{"type":["object","null"]}`, `true`, `[]`} {
		for _, entry := range []string{"chat", "responses"} {
			t.Run(entry+schema, func(t *testing.T) {
				input := `"tools":[{"type":"function","name":"probe","parameters":` + schema + `}]`
				if entry == "chat" {
					input = `"tools":[{"type":"function","function":{"name":"probe","parameters":` + schema + `}}]`
				}
				_, err := anthropicPolicyRequest(t, entry, input)
				requireUnsupportedConversion(t, err, "parameters")
			})
		}
	}
}

func TestAnthropicObjectUnionsRequiredSemantics(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, tc := range []struct{ schema, want string }{
			{`{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}]}`, `["a","b"]`},
			{`{"anyOf":[{"type":"object","properties":{"a":{},"shared":{}},"required":["a","shared"]},{"type":"object","properties":{"b":{},"shared":{}},"required":["b","shared"]}]}`, `["shared"]`},
			{`{"oneOf":[{"type":"object","properties":{"a":{},"shared":{}},"required":["a","shared"]},{"type":"object","properties":{"b":{},"shared":{}},"required":["b","shared"]}]}`, `["shared"]`},
			{`{"type":"object","properties":{"root":{}},"required":["root"],"anyOf":[{"type":"object","required":["a"]},{"type":"object"}],"allOf":[{"type":"object","required":["b"]}]}`, `["b","root"]`},
		} {
			t.Run(entry+tc.schema, func(t *testing.T) {
				field := `"tools":[{"type":"function","name":"probe","parameters":` + tc.schema + `}]`
				if entry == "chat" {
					field = `"tools":[{"type":"function","function":{"name":"probe","parameters":` + tc.schema + `}}]`
				}
				out, err := anthropicPolicyRequest(t, entry, field)
				require.NoError(t, err)
				var schema map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(out.Tools[0].InputSchema, &schema))
				require.JSONEq(t, tc.want, string(schema["required"]))
				require.NotContains(t, schema, "anyOf")
				require.NotContains(t, schema, "oneOf")
				require.NotContains(t, schema, "allOf")
				require.JSONEq(t, `"object"`, string(schema["type"]))
			})
		}
	}
}
