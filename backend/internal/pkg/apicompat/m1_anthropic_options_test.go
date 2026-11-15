package apicompat

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestM1AnthropicFieldOptions(t *testing.T) {
	for _, strict := range []*bool{nil, m1Bool(true), m1Bool(false)} {
		req := &ResponsesRequest{Model: "claude-sonnet-4-6", Tools: []ResponsesTool{{Type: "function", Name: "a", Parameters: json.RawMessage(`{"type":"object"}`), Strict: strict}, {Type: "function", Name: "b", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolChoice: json.RawMessage(`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"a"},{"type":"function","name":"a"},{"type":"function","name":"missing"}]}`)}
		var out, legacy, zero AnthropicRequest
		var notices []ConversionNotice
		opts := ConversionOptions{Bridge: &BridgeOptions{Enabled: true}, Record: func(n ConversionNotice) { notices = append(notices, n) }}
		require.NoError(t, applyAnthropicFieldPoliciesWithOptions(req, &out, opts))
		require.Len(t, out.Tools, 1)
		require.Equal(t, strict, out.Tools[0].Strict)
		require.JSONEq(t, `{"type":"auto"}`, string(out.ToolChoice))
		require.Equal(t, []ConversionNotice{{Field: "tool_choice.tools[2]", Action: "omit"}}, notices)
		require.NoError(t, applyAnthropicFieldPolicies(req, &legacy))
		require.NoError(t, applyAnthropicFieldPoliciesWithOptions(req, &zero, ConversionOptions{}))
		a, _ := json.Marshal(legacy)
		b, _ := json.Marshal(zero)
		require.Equal(t, a, b)
		require.Len(t, zero.Tools, 2)
		req.ToolChoice = json.RawMessage(`{"type":"allowed_tools","mode":"auto","tools":[]}`)
		out = AnthropicRequest{}
		require.NoError(t, applyAnthropicFieldPoliciesWithOptions(req, &out, opts))
		require.Empty(t, out.Tools)
		require.JSONEq(t, `{"type":"none"}`, string(out.ToolChoice))
	}
	id := ToolNameIdentity{Kind: "function", Namespace: "ns", Name: "f"}
	req := &ResponsesRequest{Model: "claude-sonnet-4-6", Tools: []ResponsesTool{{Type: "function", Name: "ns_f", Parameters: json.RawMessage(`{"type":"object"}`)}, {Type: "function", Name: "excluded", Parameters: json.RawMessage(`false`)}}, ToolChoice: json.RawMessage(`{"type":"allowed_tools","tools":[{"type":"function","namespace":"ns","name":"f"}]}`)}
	var out AnthropicRequest
	require.NoError(t, applyAnthropicFieldPoliciesWithOptions(req, &out, ConversionOptions{Bridge: &BridgeOptions{Enabled: true, ToolNames: &BridgeToolNameMap{Forward: map[ToolNameIdentity]string{id: "ns_f"}, Reverse: map[string]ToolNameIdentity{"ns_f": id}}}}))
	require.Len(t, out.Tools, 1)
	require.Equal(t, "ns_f", out.Tools[0].Name)
}

func m1Bool(v bool) *bool { return &v }
