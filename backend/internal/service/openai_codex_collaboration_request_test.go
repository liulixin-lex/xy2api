//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func collabDeclarationCheck(t *testing.T, body []byte) ([]byte, CollabDeclarationResult) {
	t.Helper()
	original := bytes.Clone(body)
	// The old planner remains the baseline oracle. Every synthetic request in
	// this corpus, including rejected ones, also exercises the disabled switch.
	legacyBytes, legacyPlan, legacyOutcome := applyCodexCollaborationPlaintext(body)
	disabledBytes, disabled := ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{ClientVersion: "0.100.0"})
	require.Equal(t, legacyBytes, disabledBytes)
	require.Equal(t, resilience.CollabPlanOutcome(legacyOutcome), disabled.Outcome)
	require.Equal(t, legacyPlan == nil, disabled.Plan == nil)
	if legacyPlan != nil {
		require.Equal(t, legacyPlan.tools, disabled.Plan.HistoricalTools)
		require.Equal(t, legacyPlan.devTextRewrites, disabled.Plan.DeveloperTextRewrites)
		for name, state := range disabled.Plan.EffectiveTools {
			_, removed := legacyPlan.messageTools[name]
			require.Equal(t, removed, state.MessageMarkerRemoved)
			require.Equal(t, legacyPlan.extraEncrypted[name], state.ExtraEncrypted)
			require.True(t, state.Available)
		}
	}
	options := CollabDeclarationOptions{IncrementalDeclarations: true, ClientVersion: "0.166.0"}
	planned := PlanCodexCollaborationDeclarations(body, options)
	out, result := ApplyCodexCollaborationDeclarations(body, options)
	require.Equal(t, planned, result)
	require.Equal(t, original, body)
	if result.Plan == nil {
		require.Equal(t, original, out)
	}
	return out, result
}

func collabDeclarationNotice(t *testing.T, body []byte, text string) []byte {
	t.Helper()
	return codexCollabTestSet(t, body, "input.-1", map[string]any{
		"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": text}},
	})
}

func collabDeclarationAppend(t *testing.T, body []byte, namespace []byte) []byte {
	t.Helper()
	return codexCollabTestSet(t, body, "input.-1", map[string]any{
		"type": "additional_tools", "tools": []json.RawMessage{namespace},
	})
}

func collabDeclarationSingleTool(t *testing.T, index int) []byte {
	t.Helper()
	tool := gjson.Get(codexCollabTestNamespace, fmt.Sprintf("tools.%d", index)).Raw
	return codexCollabTestSet(t, []byte(codexCollabTestNamespace), "tools", []json.RawMessage{json.RawMessage(tool)})
}

func TestCollabDeclarationsPreserveLegacySingleShapes(t *testing.T) {
	for _, carrier := range []string{"input", "tools", "both"} {
		for _, spawn := range []string{"true", "false", "absent"} {
			for _, send := range []string{"true", "false", "absent"} {
				for _, followup := range []string{"true", "false", "absent"} {
					t.Run(carrier+"/"+spawn+"/"+send+"/"+followup, func(t *testing.T) {
						namespace := []byte(codexCollabTestNamespace)
						for i, marker := range []string{spawn, send, followup} {
							path := fmt.Sprintf("tools.%d.parameters.properties.message.encrypted", i)
							if marker == "absent" {
								var err error
								namespace, err = sjson.DeleteBytes(namespace, path)
								require.NoError(t, err)
							} else {
								namespace = codexCollabTestSet(t, namespace, path, marker == "true")
							}
						}
						namespace = codexCollabTestSet(t, namespace, "tools.0.parameters.properties.task_name.encrypted", true)
						body := []byte(`{"input":[]}`)
						if carrier != "tools" {
							body = collabDeclarationAppend(t, body, namespace)
						}
						if carrier != "input" {
							body = codexCollabTestSet(t, body, "tools", []json.RawMessage{namespace})
						}
						legacy, _, outcome := applyCodexCollaborationPlaintext(body)
						require.Equal(t, codexCollabApplied, outcome)
						out, result := collabDeclarationCheck(t, body)
						require.NotNil(t, result.Plan)
						require.Equal(t, legacy, out)
						require.True(t, result.Plan.EffectiveTools["spawn_agent"].ExtraEncrypted)
					})
				}
			}
		}
	}
	for _, typ := range []string{"custom", "unknown", " function "} {
		body := codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.tools.0.type", typ)
		old, _, outcome := applyCodexCollaborationPlaintext(body)
		require.Equal(t, codexCollabApplied, outcome)
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.Equal(t, old, out)
	}
}

func TestCollabDeclarationsIncrementalDefinitions(t *testing.T) {
	for _, carrier := range []string{"input", "tools"} {
		t.Run(carrier, func(t *testing.T) {
			namespace := codexCollabTestSet(t, []byte(codexCollabTestNamespace), "tools.-1", map[string]any{"type": "function", "name": "interrupt_agent", "parameters": map[string]any{"type": "object"}})
			body := []byte(`{"input":[]}`)
			if carrier == "tools" {
				body = codexCollabTestSet(t, body, "tools", []json.RawMessage{namespace})
			} else {
				body = collabDeclarationAppend(t, body, namespace)
			}
			body = collabDeclarationNotice(t, body, codexCollabUpdateHint)
			update := collabDeclarationSingleTool(t, 0)
			update = codexCollabTestSet(t, update, "tools.0.description", "A revised task description.")
			body = collabDeclarationAppend(t, body, update)
			out, result := collabDeclarationCheck(t, body)
			require.Equal(t, resilience.CollabPlanOutcomeApplied, result.Outcome)
			require.Len(t, result.Plan.EffectiveTools, 6)
			require.True(t, result.Plan.EffectiveTools["send_message"].MessageMarkerRemoved)
			namespaces := codexCollabFindNamespaces(gjson.ParseBytes(body))
			require.Len(t, namespaces, 2)
			for _, ns := range namespaces {
				require.Equal(t, "collaboration-xy", gjson.GetBytes(out, ns.path+".name").String())
				require.Equal(t, len(ns.tools.Array()), len(gjson.GetBytes(out, ns.path+".tools").Array()))
			}
			require.Equal(t, "A revised task description.", gjson.GetBytes(out, namespaces[1].path+".tools.0.description").String())
		})
	}
	t.Run("replacement removes old ordinary fields and encryption state", func(t *testing.T) {
		body := codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.tools.0.parameters.properties.task_name.encrypted", true)
		update := collabDeclarationSingleTool(t, 0)
		var err error
		update, err = sjson.DeleteBytes(update, "tools.0.parameters.properties.task_name")
		require.NoError(t, err)
		update = codexCollabTestSet(t, update, "tools.0.parameters.required", []string{"message"})
		body = collabDeclarationAppend(t, body, update)
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.False(t, result.Plan.EffectiveTools["spawn_agent"].ExtraEncrypted)
		require.False(t, gjson.GetBytes(out, "input.1.tools.0.tools.0.parameters.properties.task_name").Exists())
		require.True(t, gjson.GetBytes(out, "input.0.tools.0.tools.0.parameters.properties.task_name.encrypted").Bool())
	})
	t.Run("append and replace tool kind", func(t *testing.T) {
		update := []byte(`{"type":"namespace","name":"collaboration","tools":[{"type":"custom","name":"inspect_agent"},{"type":"custom","name":"wait_agent"}]}`)
		body := collabDeclarationAppend(t, codexCollabTestRequest(), update)
		_, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.Len(t, result.Plan.HistoricalTools, 6)
		require.Equal(t, CollabToolTypeCustom, result.Plan.EffectiveTools["wait_agent"].Type)
		require.False(t, result.Plan.EffectiveTools["wait_agent"].MessageMarkerRemoved)
	})
}

func TestCollabDeclarationsNoticesAndReenable(t *testing.T) {
	for _, wholeNamespace := range []bool{false, true} {
		t.Run(fmt.Sprint(wholeNamespace), func(t *testing.T) {
			text := codexCollabRemovedTools + "\n- collaboration.spawn_agent\n- other.sample"
			if wholeNamespace {
				text = codexCollabRemovedNamespaces + "\n- collaboration\n- other"
			}
			body := collabDeclarationNotice(t, codexCollabTestRequest(), text)
			body = codexCollabTestSet(t, body, "input.-1", json.RawMessage(`{"type":"function_call","namespace":"collaboration-xy","name":"collaboration-xy.spawn_agent","arguments":"{ \"message\": \"history\" }"}`))
			out, result := collabDeclarationCheck(t, body)
			require.NotNil(t, result.Plan)
			require.Contains(t, result.Plan.HistoricalTools, "spawn_agent")
			require.False(t, result.Plan.EffectiveTools["spawn_agent"].Available)
			require.Equal(t, !wholeNamespace, result.Plan.EffectiveTools["send_message"].Available)
			require.Contains(t, gjson.GetBytes(out, "input.1.content.0.text").String(), "collaboration-xy")
			require.Equal(t, "spawn_agent", gjson.GetBytes(out, "input.2.name").String())
			require.Equal(t, gjson.GetBytes(body, "input.2.arguments").Raw, gjson.GetBytes(out, "input.2.arguments").Raw)
			body = collabDeclarationNotice(t, body, "Updated instructions for the collaboration namespace:\nUse collaboration.spawn_agent carefully.")
			body = collabDeclarationNotice(t, body, "The collaboration namespace no longer has additional instructions.")
			out, result = collabDeclarationCheck(t, body)
			require.NotNil(t, result.Plan)
			require.False(t, result.Plan.EffectiveTools["spawn_agent"].Available)
			require.Equal(t, "Updated instructions for the collaboration-xy namespace:\nUse collaboration.spawn_agent carefully.", gjson.GetBytes(out, "input.3.content.0.text").String())
			body = collabDeclarationAppend(t, body, collabDeclarationSingleTool(t, 0))
			_, result = collabDeclarationCheck(t, body)
			require.NotNil(t, result.Plan)
			require.True(t, result.Plan.EffectiveTools["spawn_agent"].Available)
			require.Equal(t, !wholeNamespace, result.Plan.EffectiveTools["send_message"].Available)
		})
	}
	t.Run("combined notice", func(t *testing.T) {
		text := codexCollabRemovedNamespaces + "\n- other\n\n" + codexCollabRemovedTools + "\n- collaboration.spawn_agent"
		body := collabDeclarationNotice(t, codexCollabTestRequest(), text)
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.False(t, result.Plan.EffectiveTools["spawn_agent"].Available)
		require.True(t, result.Plan.EffectiveTools["send_message"].Available)
		require.Equal(t, 1, result.Plan.DeveloperTextRewrites)
		require.Contains(t, gjson.GetBytes(out, "input.1.content.0.text").String(), "- other\n\n")
	})
	t.Run("namespace description delta", func(t *testing.T) {
		update := codexCollabTestSet(t, []byte(codexCollabTestNamespace), "description", "A different namespace description.")
		update = codexCollabTestSet(t, update, "tools", []any{})
		body := collabDeclarationAppend(t, codexCollabTestRequest(), update)
		_, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.Len(t, result.Plan.EffectiveTools, 5)
	})
}

func TestCollabDeclarationsUnionTypes(t *testing.T) {
	for _, tc := range []struct {
		typ      string
		accepted bool
	}{
		{`"string"`, true}, {`["string"]`, true}, {`["null","string"]`, true}, {`["string","null"]`, true},
		{`[]`, false}, {`["null"]`, false}, {`["string","string"]`, false}, {`["string","null","null"]`, false},
		{`["string","number"]`, false}, {`["string",null]`, false}, {`"null"`, false}, {`{}`, false},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			body := codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.tools.0.parameters.properties.message.type", json.RawMessage(tc.typ))
			out, result := collabDeclarationCheck(t, body)
			require.Equal(t, tc.accepted, result.Plan != nil)
			if tc.accepted {
				require.Equal(t, tc.typ, gjson.GetBytes(out, "input.0.tools.0.tools.0.parameters.properties.message.type").Raw)
			}
		})
	}
}

func TestCollabDeclarationsClientVersionGate(t *testing.T) {
	for _, tc := range []struct {
		version string
		old     bool
	}{
		{"0.146.9", true}, {"0.147.0-alpha", true}, {"0.147.0-alpha.0", true},
		{"0.147.0-alpha.1", false}, {"0.147.0-alpha.2", false}, {"0.147.0-beta.1", false}, {"0.147.0", false},
		{"0.166.0+build", false}, {"", false}, {"unknown", false}, {"0.146.0-invalid!", false},
		{"0.146", false}, {"0", false}, {"0.146.0+build", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			body := codexCollabTestRequest()
			out, result := ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{IncrementalDeclarations: true, ClientVersion: tc.version})
			require.Equal(t, !tc.old, result.Plan != nil)
			if tc.old {
				require.Equal(t, resilience.CollabPlanReasonClientTooOld, result.Reason)
				require.Equal(t, body, out)
			}
			disabled, _ := ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{ClientVersion: tc.version})
			legacy, _, _ := applyCodexCollaborationPlaintext(body)
			require.Equal(t, legacy, disabled)
		})
	}
}

func TestCollabDeclarationsUnprovenUpdatesFallBack(t *testing.T) {
	t.Run("mixed markers in an initial declaration do not authorize new updates", func(t *testing.T) {
		body := codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.tools.1.parameters.properties.message.encrypted", false)
		body = collabDeclarationAppend(t, body, collabDeclarationSingleTool(t, 0))
		_, result := collabDeclarationCheck(t, body)
		require.Nil(t, result.Plan)
		require.Equal(t, resilience.CollabPlanReasonSchemaInvalid, result.Reason)
	})
	t.Run("missing marker in a replacement", func(t *testing.T) {
		update, err := sjson.DeleteBytes(collabDeclarationSingleTool(t, 0), "tools.0.parameters.properties.message.encrypted")
		require.NoError(t, err)
		_, result := collabDeclarationCheck(t, collabDeclarationAppend(t, codexCollabTestRequest(), update))
		require.Nil(t, result.Plan)
		require.Equal(t, resilience.CollabPlanReasonSchemaInvalid, result.Reason)
	})
	for _, tc := range []struct {
		name, path string
		value      any
	}{
		{"marker false", "tools.0.parameters.properties.message.encrypted", false},
		{"marker null", "tools.0.parameters.properties.message.encrypted", nil},
		{"marker string", "tools.0.parameters.properties.message.encrypted", "true"},
		{"missing message", "tools.0.parameters.properties", map[string]any{}},
		{"custom message tool", "tools.0.type", "custom"},
		{"schema union", "tools.0.parameters.properties.message.anyOf", []any{}},
		{"parameter reference", "tools.0.parameters.$ref", "#/$defs/task"},
		{"namespace metadata", "experimental", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			update := codexCollabTestSet(t, collabDeclarationSingleTool(t, 0), tc.path, tc.value)
			body := collabDeclarationAppend(t, codexCollabTestRequest(), update)
			_, result := collabDeclarationCheck(t, body)
			require.Nil(t, result.Plan)
			require.Equal(t, resilience.CollabPlanOutcomeConflict, result.Outcome)
		})
	}
	for _, notice := range []string{
		"Unavailable tools:\n- collaboration.spawn_agent", "Unavailable namespaces:\n- collaboration", "\n- collaboration.spawn_agent",
		codexCollabRemovedTools, codexCollabRemovedTools + "\n- collaboration", codexCollabRemovedTools + "\n- collaboration.spawn_agent.extra",
		codexCollabRemovedNamespaces + "\n- collaboration.spawn_agent", codexCollabRemovedTools + "\n- collaboration.spawn_agent\nunknown suffix",
		"This is an incremental tools update. Overridden guidance.",
		"Updated instructions for the collaboration namespace: unsupported delimiter",
		"The collaboration namespace no longer has alternate guidance.",
	} {
		t.Run(notice, func(t *testing.T) {
			body := collabDeclarationNotice(t, codexCollabTestRequest(), notice)
			legacy, legacyPlan, outcome := applyCodexCollaborationPlaintext(body)
			require.Equal(t, codexCollabApplied, outcome)
			out, result := collabDeclarationCheck(t, body)
			require.Equal(t, legacy, out)
			require.Equal(t, codexCollabSharedLegacyNamespaces(codexCollabFindNamespaces(gjson.ParseBytes(body)), legacyPlan), result.Plan)
			// With no legacy floor, only a complete, header-led notice whose
			// identities cannot be interpreted still declines conversion.
			update := codexCollabTestSet(t, collabDeclarationSingleTool(t, 0), "description", "Updated task guidance.")
			body = collabDeclarationAppend(t, body, update)
			_, result = collabDeclarationCheck(t, body)
			uninterpretable := notice == codexCollabRemovedTools+"\n- collaboration" ||
				notice == codexCollabRemovedTools+"\n- collaboration.spawn_agent.extra" ||
				notice == codexCollabRemovedNamespaces+"\n- collaboration.spawn_agent"
			if uninterpretable {
				require.Nil(t, result.Plan)
				require.Equal(t, resilience.CollabPlanReasonControlText, result.Reason)
			} else {
				require.NotNil(t, result.Plan)
			}
			// Client metadata establishes a catalog notice even when the text
			// alone is insufficient. Every unparsed template now declines.
			body = codexCollabTestSet(t, body, "input.1.internal_chat_message_metadata_passthrough.content_item_kinds", []string{"tools.removed_definition"})
			out, result = collabDeclarationCheck(t, body)
			require.Nil(t, result.Plan)
			require.Equal(t, resilience.CollabPlanReasonControlText, result.Reason)
			require.Equal(t, body, out)
		})
	}
}

func TestCollabDeclarationsNoticeScopeAndKnownHistory(t *testing.T) {
	t.Run("partial update retains other members encryption state", func(t *testing.T) {
		body := codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.tools.1.parameters.properties.target.encrypted", true)
		body = collabDeclarationAppend(t, body, collabDeclarationSingleTool(t, 0))
		_, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.True(t, result.Plan.EffectiveTools["send_message"].ExtraEncrypted)
	})
	t.Run("template identity and address example edits compose", func(t *testing.T) {
		body := collabDeclarationNotice(t, codexCollabTestRequest(), "Updated instructions for the collaboration namespace:\nCall functions.collaboration.spawn_agent.")
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.Equal(t, 2, result.Plan.DeveloperTextRewrites)
		require.Equal(t, "Updated instructions for the collaboration-xy namespace:\nCall functions.collaboration-xy.spawn_agent.", gjson.GetBytes(out, "input.1.content.0.text").String())
		var textEdits []codexCollabJSONEdit
		for _, edit := range result.edits {
			if edit.path == "input.1.content.0.text" {
				textEdits = append(textEdits, edit)
			}
		}
		require.Len(t, textEdits, 2)
		require.Contains(t, textEdits[0].value, "functions.collaboration-xy.spawn_agent")
		require.Contains(t, textEdits[1].value, "functions.collaboration-xy.spawn_agent")
	})
	for _, role := range []string{"user", "assistant", "system"} {
		body := collabDeclarationNotice(t, codexCollabTestRequest(), codexCollabRemovedTools+"\n- collaboration.spawn_agent")
		body = codexCollabTestSet(t, body, "input.1.role", role)
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.True(t, result.Plan.EffectiveTools["spawn_agent"].Available)
		require.Equal(t, gjson.GetBytes(body, "input.1").Raw, gjson.GetBytes(out, "input.1").Raw)
	}
	for _, carrier := range []string{"input.1.content", "input.1.content.0.text"} {
		body := collabDeclarationNotice(t, codexCollabTestRequest(), "Plain collaboration.spawn_agent mention.")
		if carrier == "input.1.content" {
			body = codexCollabTestSet(t, body, carrier, codexCollabRemovedTools+"\n- collaboration.spawn_agent")
		} else {
			body = codexCollabTestSet(t, body, "input.1.content.0.type", "output_text")
		}
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.True(t, result.Plan.EffectiveTools["spawn_agent"].Available)
		require.Equal(t, gjson.GetBytes(body, "input.1").Raw, gjson.GetBytes(out, "input.1").Raw)
	}
	body := collabDeclarationNotice(t, codexCollabTestRequest(), codexCollabRemovedTools+"\n- collaboration.unknown_tool")
	body = codexCollabTestSet(t, body, "input.-1", json.RawMessage(`{"type":"function_call","namespace":"collaboration-xy","name":"unknown_tool","arguments":"{}"}`))
	_, result := collabDeclarationCheck(t, body)
	require.Nil(t, result.Plan, "a notice cannot establish historical membership")
	require.Equal(t, resilience.CollabPlanReasonHistoryIdentity, result.Reason)
}

func TestCollabDeclarationsOrdinaryProjectInstructions(t *testing.T) {
	for _, text := range []string{
		"Project rules:\n- collaboration.spawn_agent is expensive, use it rarely\n- run the tests",
		"Skills:\n- testing\n- collaboration\n- writing",
		"Example of a notice:\nThe following tools are no longer available. Do not call them:\n(none today)",
		"This is an incremental tools update. Nothing changed.",
	} {
		notice := codexCollabParseCatalogNotice(text)
		require.False(t, notice.recognized)
		require.False(t, notice.invalid)
		body := collabDeclarationNotice(t, codexCollabTestRequest(), text)
		legacy, _, outcome := applyCodexCollaborationPlaintext(body)
		require.Equal(t, codexCollabApplied, outcome)
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.Equal(t, legacy, out)
		// An incremental-only declaration proves the text is treated as ordinary
		// even when the legacy fallback cannot hide a classification mistake.
		body = collabDeclarationAppend(t, body, collabDeclarationSingleTool(t, 0))
		_, legacyPlan, _ := applyCodexCollaborationPlaintext(body)
		require.Nil(t, legacyPlan)
		out, result = collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.Equal(t, strings.Count(text, "collaboration"), result.OrdinaryMentions)
		require.Equal(t, text, gjson.GetBytes(out, "input.1.content.0.text").String())
	}
}

func TestCollabDeclarationsConfirmedCatalogNotices(t *testing.T) {
	for _, tc := range []struct {
		name, kind, text string
		recognized       bool
	}{
		{"removed member override", "tools.removed_definition", "Unavailable tools:\n- collaboration.spawn_agent", false},
		{"removed namespace override", "tools.removed_definition", "Unavailable namespaces:\n- collaboration", false},
		{"empty header", "tools.removed_definition", "\n- collaboration.spawn_agent", false},
		{"bare identity", "tools.removed_definition", "- collaboration.spawn_agent", false},
		{"combined override", "tools.removed_definition", codexCollabRemovedTools + "\n- other.sample\n\nUnavailable namespaces:\n- collaboration", false},
		{"combined reversed", "tools.removed_definition", "Unavailable namespaces:\n- collaboration\n\n" + codexCollabRemovedTools + "\n- other.sample", false},
		{"update override", "tools.incremental_update", "Catalog updated.", false},
		{"empty update", "tools.incremental_update", "", false},
		{"mismatched template", "tools.removed_definition", codexCollabUpdateHint, false},
		{"default removal", "tools.removed_definition", codexCollabRemovedTools + "\n- collaboration.spawn_agent", true},
		{"default namespace removal", "tools.removed_definition", codexCollabRemovedNamespaces + "\n- collaboration", true},
		{"default combined", "tools.removed_definition", codexCollabRemovedNamespaces + "\n- other\n\n" + codexCollabRemovedTools + "\n- collaboration.spawn_agent", true},
		{"default update", "tools.incremental_update", codexCollabUpdateHint, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := collabDeclarationNotice(t, codexCollabTestRequest(), tc.text)
			body = codexCollabTestSet(t, body, "input.1.internal_chat_message_metadata_passthrough.content_item_kinds", []string{tc.kind})
			// The accepted legacy floor remains byte-identical on every new decline.
			if !tc.recognized {
				legacy, _, outcome := applyCodexCollaborationPlaintext(body)
				require.Equal(t, codexCollabApplied, outcome)
				out, result := collabDeclarationCheck(t, body)
				require.NotNil(t, result.Plan)
				require.Equal(t, legacy, out)
			}
			body = collabDeclarationAppend(t, body, collabDeclarationSingleTool(t, 1))
			_, legacyPlan, _ := applyCodexCollaborationPlaintext(body)
			require.Nil(t, legacyPlan)
			out, result := collabDeclarationCheck(t, body)
			if !tc.recognized {
				require.Nil(t, result.Plan)
				require.Equal(t, resilience.CollabPlanReasonControlText, result.Reason)
				require.Equal(t, body, out)
			} else {
				require.NotNil(t, result.Plan)
				require.Equal(t, tc.kind != "tools.removed_definition", result.Plan.EffectiveTools["spawn_agent"].Available)
				require.Equal(t, gjson.GetBytes(body, "input.1.internal_chat_message_metadata_passthrough").Raw, gjson.GetBytes(out, "input.1.internal_chat_message_metadata_passthrough").Raw)
			}
		})
	}
}

func TestCollabDeclarationsQualifiedCustomHistory(t *testing.T) {
	for _, name := range []string{"collaboration.spawn_agent", "collaboration__spawn_agent"} {
		for _, namespace := range []string{"", "collaboration", "other"} {
			body := codexCollabTestSet(t, codexCollabTestRequest(), "input.-1", map[string]any{
				"type": "custom_tool_call", "name": name, "namespace": namespace, "input": "opaque payload",
			})
			legacy, _, outcome := applyCodexCollaborationPlaintext(body)
			require.Equal(t, codexCollabApplied, outcome)
			out, result := collabDeclarationCheck(t, body)
			require.NotNil(t, result.Plan)
			require.Equal(t, legacy, out)
			body = collabDeclarationAppend(t, body, collabDeclarationSingleTool(t, 0))
			out, result = collabDeclarationCheck(t, body)
			require.Nil(t, result.Plan)
			require.Equal(t, resilience.CollabPlanReasonUnsupportedShape, result.Reason)
			require.Equal(t, body, out)
		}
	}
	for _, tc := range []struct{ name, namespace string }{
		{"spawn_agent", "collaboration"},
		{"collaboration-xy.spawn_agent", ""},
		{"collaboration-xy__spawn_agent", "collaboration-xy"},
		{"other.spawn_agent", "other"},
	} {
		body := collabDeclarationAppend(t, codexCollabTestRequest(), collabDeclarationSingleTool(t, 0))
		body = codexCollabTestSet(t, body, "input.-1", map[string]any{"type": "custom_tool_call", "name": tc.name, "namespace": tc.namespace, "input": "opaque payload"})
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.Equal(t, gjson.GetBytes(body, "input.2.input").Raw, gjson.GetBytes(out, "input.2.input").Raw)
	}
}

func TestCollabDeclarationsCatalogMetadataScope(t *testing.T) {
	body := collabDeclarationAppend(t, codexCollabTestRequest(), collabDeclarationSingleTool(t, 0))
	body = collabDeclarationNotice(t, body, "Project rules:\n- collaboration\n- testing")
	body = codexCollabTestSet(t, body, "input.2.content.-1", map[string]any{"type": "input_text", "text": codexCollabRemovedTools + "\n- collaboration.spawn_agent"})
	body = codexCollabTestSet(t, body, "input.2.internal_chat_message_metadata_passthrough.content_item_kinds", []string{"generic.developer_instructions", "tools.removed_definition"})
	out, result := collabDeclarationCheck(t, body)
	require.NotNil(t, result.Plan)
	require.False(t, result.Plan.EffectiveTools["spawn_agent"].Available)
	require.Equal(t, gjson.GetBytes(body, "input.2.content.0").Raw, gjson.GetBytes(out, "input.2.content.0").Raw)
	for _, kinds := range [][]string{
		{"tools.removed_definition", "generic.developer_instructions"},
		{"tools.removed_definition"},
		{"generic.developer_instructions", "tools.removed_definition", "tools.incremental_update"},
	} {
		bad := codexCollabTestSet(t, body, "input.2.internal_chat_message_metadata_passthrough.content_item_kinds", kinds)
		_, result = collabDeclarationCheck(t, bad)
		require.Nil(t, result.Plan)
		require.Equal(t, resilience.CollabPlanReasonControlText, result.Reason)
	}
	for _, role := range []string{"user", "assistant", "system"} {
		scoped := codexCollabTestSet(t, body, "input.2.role", role)
		scoped = codexCollabTestSet(t, scoped, "input.2.content.1.text", "Unavailable tools:\n- collaboration.spawn_agent")
		out, result = collabDeclarationCheck(t, scoped)
		require.NotNil(t, result.Plan)
		require.True(t, result.Plan.EffectiveTools["spawn_agent"].Available)
		require.Equal(t, gjson.GetBytes(scoped, "input.2").Raw, gjson.GetBytes(out, "input.2").Raw)
	}
	for _, kind := range []string{"generic.developer_instructions", "unknown", "user.text"} {
		ordinary := collabDeclarationNotice(t, collabDeclarationAppend(t, codexCollabTestRequest(), collabDeclarationSingleTool(t, 0)), "Skills:\n- collaboration\n- testing")
		ordinary = codexCollabTestSet(t, ordinary, "input.2.internal_chat_message_metadata_passthrough.content_item_kinds", []string{kind})
		_, result = collabDeclarationCheck(t, ordinary)
		require.NotNil(t, result.Plan)
		require.True(t, result.Plan.EffectiveTools["spawn_agent"].Available)
	}
}

func TestCollabDeclarationsNormalizedPlanReuse(t *testing.T) {
	options := CollabDeclarationOptions{IncrementalDeclarations: true}
	body := collabDeclarationAppend(t, codexCollabTestRequest(), collabDeclarationSingleTool(t, 0))
	raw := PlanCodexCollaborationDeclarations(body, options)
	expected, expectedResult := ApplyCodexCollaborationDeclarations(body, options)
	for _, normalized := range [][]byte{body, bytes.Clone(body)} {
		out, result := ApplyCodexCollaborationNormalizedDeclarations(body, normalized, raw, options)
		require.Equal(t, expected, out)
		require.Equal(t, expectedResult, result)
		require.Same(t, raw.Plan, result.Plan, "equal bytes reuse the exact plan")
	}
	changed := codexCollabTestSet(t, body, "input.1.tools.0.tools.0.parameters.properties.task_name.encrypted", true)
	expected, expectedResult = ApplyCodexCollaborationDeclarations(changed, options)
	out, result := ApplyCodexCollaborationNormalizedDeclarations(body, changed, raw, options)
	require.Equal(t, expected, out)
	require.Equal(t, expectedResult, result)
	require.NotSame(t, raw.Plan, result.Plan)
	require.True(t, result.Plan.EffectiveTools["spawn_agent"].ExtraEncrypted)
	// Normalization removes the evidence confirming an overridden notice.
	body = collabDeclarationNotice(t, body, "Unavailable tools:\n- collaboration.spawn_agent")
	body = codexCollabTestSet(t, body, "input.2.internal_chat_message_metadata_passthrough.content_item_kinds", []string{"tools.removed_definition"})
	raw = PlanCodexCollaborationDeclarations(body, options)
	require.Nil(t, raw.Plan)
	normalized, wasChanged, err := normalizeOpenAIOAuthResponsesCompatibilityBody(body)
	require.NoError(t, err)
	require.True(t, wasChanged)
	require.NotNil(t, PlanCodexCollaborationDeclarations(normalized, options).Plan, "metadata absence alone no longer confirms the notice")
	out, result = ApplyCodexCollaborationNormalizedDeclarations(body, normalized, raw, options)
	require.Equal(t, normalized, out)
	require.Equal(t, raw, result, "the raw rejection remains binding")
}

func TestCollabDeclarationsDeveloperCarriers(t *testing.T) {
	for _, typ := range []string{"omitted", "message"} {
		for _, content := range []string{"text parts", "string"} {
			for _, text := range []string{
				codexCollabRemovedTools + "\n- collaboration.spawn_agent",
				"Unavailable tools:\n- collaboration.spawn_agent",
				"\n- collaboration.spawn_agent",
			} {
				t.Run(typ+"/"+content+"/"+text, func(t *testing.T) {
					body := collabDeclarationNotice(t, codexCollabTestRequest(), text)
					body = codexCollabTestSet(t, body, "input.1.internal_chat_message_metadata_passthrough.content_item_kinds", []string{"tools.removed_definition"})
					if typ == "omitted" {
						var err error
						body, err = sjson.DeleteBytes(body, "input.1.type")
						require.NoError(t, err)
					}
					if content == "string" {
						body = codexCollabTestSet(t, body, "input.1.content", text)
					}
					unsupportedCarrier := typ == "omitted" || content == "string"
					if unsupportedCarrier {
						legacy, _, outcome := applyCodexCollaborationPlaintext(body)
						require.Equal(t, codexCollabApplied, outcome)
						out, result := collabDeclarationCheck(t, body)
						require.NotNil(t, result.Plan)
						require.Equal(t, legacy, out, "legacy conversions remain byte-identical")
					}
					body = collabDeclarationAppend(t, body, collabDeclarationSingleTool(t, 1))
					out, result := collabDeclarationCheck(t, body)
					if unsupportedCarrier || text != codexCollabRemovedTools+"\n- collaboration.spawn_agent" {
						require.Nil(t, result.Plan)
						require.Equal(t, resilience.CollabPlanReasonControlText, result.Reason)
						require.Equal(t, body, out)
					} else {
						require.NotNil(t, result.Plan)
						require.False(t, result.Plan.EffectiveTools["spawn_agent"].Available)
						require.Contains(t, gjson.GetBytes(out, "input.1.content.0.text").String(), "collaboration-xy.spawn_agent")
					}
					if unsupportedCarrier {
						withoutMetadata, err := sjson.DeleteBytes(body, "input.1.internal_chat_message_metadata_passthrough")
						require.NoError(t, err)
						out, result = collabDeclarationCheck(t, withoutMetadata)
						require.Nil(t, result.Plan, "unsupported carriers cannot hide unannotated notices either")
						require.Equal(t, withoutMetadata, out)
					}
				})
			}
		}
	}
	t.Run("role-only user remains opaque", func(t *testing.T) {
		body := collabDeclarationAppend(t, codexCollabTestRequest(), collabDeclarationSingleTool(t, 0))
		body = codexCollabTestSet(t, body, "input.-1", map[string]any{"role": "user", "content": codexCollabRemovedTools + "\n- collaboration.spawn_agent"})
		out, result := collabDeclarationCheck(t, body)
		require.NotNil(t, result.Plan)
		require.True(t, result.Plan.EffectiveTools["spawn_agent"].Available)
		require.Equal(t, gjson.GetBytes(body, "input.2").Raw, gjson.GetBytes(out, "input.2").Raw)
	})
}

func TestCollabDeclarationsPlanReuseBinding(t *testing.T) {
	on := CollabDeclarationOptions{IncrementalDeclarations: true, ClientVersion: "0.166.0"}
	good := collabDeclarationAppend(t, codexCollabTestRequest(), collabDeclarationSingleTool(t, 0))
	bad := collabDeclarationNotice(t, good, "Unavailable tools:\n- collaboration.spawn_agent")
	bad = codexCollabTestSet(t, bad, "input.2.internal_chat_message_metadata_passthrough.content_item_kinds", []string{"tools.removed_definition"})
	normalizedBad, _, err := normalizeOpenAIOAuthResponsesCompatibilityBody(bad)
	require.NoError(t, err)
	accepted := PlanCodexCollaborationDeclarations(good, on)
	rejected := PlanCodexCollaborationDeclarations(bad, on)
	require.NotNil(t, accepted.Plan)
	require.Nil(t, rejected.Plan)
	for _, tc := range []struct {
		name                 string
		original, normalized []byte
		raw                  CollabDeclarationResult
		options              CollabDeclarationOptions
	}{
		{"foreign accepted result", bad, normalizedBad, accepted, on},
		{"foreign rejected result", good, good, rejected, on},
		{"unbound result", good, bytes.Clone(good), CollabDeclarationResult{}, on},
		{"changed version", good, good, accepted, CollabDeclarationOptions{IncrementalDeclarations: true, ClientVersion: "0.146.0"}},
		{"disabled after planning", good, good, accepted, CollabDeclarationOptions{}},
		{"enabled after planning", good, good, PlanCodexCollaborationDeclarations(good, CollabDeclarationOptions{}), on},
		{"newer client after rejection", good, good, PlanCodexCollaborationDeclarations(good, CollabDeclarationOptions{IncrementalDeclarations: true, ClientVersion: "0.146.0"}), on},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := PlanCodexCollaborationDeclarations(tc.original, tc.options)
			expected := tc.normalized
			expectedResult := fresh
			if fresh.Plan != nil {
				expected, expectedResult = ApplyCodexCollaborationDeclarations(tc.normalized, tc.options)
			}
			out, result := ApplyCodexCollaborationNormalizedDeclarations(tc.original, tc.normalized, tc.raw, tc.options)
			require.Equal(t, expected, out)
			require.Equal(t, expectedResult, result)
			if result.Plan != nil {
				require.NotSame(t, tc.raw.Plan, result.Plan)
			}
		})
	}
	t.Run("in place mutation", func(t *testing.T) {
		original := bytes.Clone(good)
		raw := PlanCodexCollaborationDeclarations(original, on)
		marker := bytes.Index(original, []byte(`"encrypted":true`))
		require.NotEqual(t, -1, marker)
		copy(original[marker+len(`"encrypted":`):], "null")
		require.Nil(t, PlanCodexCollaborationDeclarations(original, on).Plan)
		out, result := ApplyCodexCollaborationNormalizedDeclarations(original, good, raw, on)
		require.Nil(t, result.Plan)
		require.Equal(t, good, out, "normalization cannot conceal changed original evidence")
	})
}

func TestCollabDeclarationsOpaqueBytesAndOrdinaryMentions(t *testing.T) {
	body := codexCollabTestRequest()
	for _, item := range []string{
		`{ "type":"agent_message", "content":[ {"type":"encrypted_content","encrypted_content":"synthetic-sealed-body"} ] }`,
		`{ "type":"function_call_output", "call_id":"call_fixture", "output":"unchanged\\ncollaboration.spawn_agent" }`,
		`{ "type":"message", "role":"user", "content":[{"type":"input_text","text":"collaboration.spawn_agent"}] }`,
		`{ "type":"function_call", "namespace":"collaboration", "name":"spawn_agent", "id":"item_fixture", "call_id":"call_fixture", "arguments":"{  \"message\":\"unicode λ \\\" \\n\" }", "encrypted_function_args":[ "message" ] }`,
	} {
		body = codexCollabTestSet(t, body, "input.-1", json.RawMessage(item))
	}
	body = collabDeclarationNotice(t, body, "A plain mention of collaboration.spawn_agent.")
	body = collabDeclarationAppend(t, body, codexCollabTestSet(t, collabDeclarationSingleTool(t, 0), "description", "Changed description."))
	out, result := collabDeclarationCheck(t, body)
	require.NotNil(t, result.Plan)
	require.Equal(t, 1, result.OrdinaryMentions)
	for _, path := range []string{"input.1", "input.2", "input.3", "input.4.id", "input.4.call_id", "input.4.arguments", "input.4.encrypted_function_args", "input.5"} {
		require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(out, path).Raw, path)
	}
}

func TestCollabDeclarationsRawAmbiguityCannotGainPlan(t *testing.T) {
	body := codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.tools.0.parameters.properties.message", json.RawMessage(`{"type":"string","encrypted":false,"encrypted":true}`))
	_, raw := collabDeclarationCheck(t, body)
	require.Nil(t, raw.Plan)
	var normalized any
	require.NoError(t, json.Unmarshal(body, &normalized))
	normalizedBytes, err := json.Marshal(normalized)
	require.NoError(t, err)
	_, result := collabDeclarationCheck(t, normalizedBytes)
	require.NotNil(t, result.Plan, "the raw guard must be retained even when normalization conceals duplicate markers")
	for _, body := range [][]byte{
		[]byte(`{"tools":`), []byte(`{}`), []byte(`{"tools":[],"tools":[]}`),
		codexCollabTestSet(t, codexCollabTestRequest(), "store", true),
		codexCollabTestSet(t, codexCollabTestRequest(), "previous_response_id", "response_fixture"),
		codexCollabTestSet(t, codexCollabTestRequest(), "tool_choice", map[string]any{"type": "function", "name": "spawn_agent"}),
		codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.name", "collaboration-xy"),
		codexCollabTestSet(t, codexCollabTestRequest(), "input.0.tools.0.tools.1.name", "spawn_agent"),
	} {
		_, result := collabDeclarationCheck(t, body)
		require.Nil(t, result.Plan)
	}
}

func TestCollabDeclarationsDifferentialGenerated(t *testing.T) {
	texts := []struct {
		text   string
		notice bool
	}{
		{"Project rules:\n- collaboration.spawn_agent is expensive, use it rarely\n- run the tests", false},
		{"Skills:\n- testing\n- collaboration\n- writing", false},
		{"Example of a notice:\n" + codexCollabRemovedTools + "\n(none today)", false},
		{"This is an incremental tools update. Nothing changed.", false},
		{"Quoted:\n" + codexCollabRemovedTools + "\n- collaboration.spawn_agent", false},
		{"> " + codexCollabRemovedTools + "\n> - collaboration.spawn_agent", false},
		{"Unavailable tools:\n- collaboration.spawn_agent", false},
		{codexCollabRemovedTools + "\n- collaboration.spawn_agent\nCall it rarely.", false},
		{codexCollabRemovedTools + "\n- collaboration", false},
		{"ordinary functions.collaboration.spawn_agent example", false},
		{"λ\n- collaboration\nUse functions.collaboration.send_message.", false},
		{codexCollabUpdateHint, true},
		{codexCollabRemovedTools + "\n- collaboration.spawn_agent", true},
		{codexCollabRemovedNamespaces + "\n- collaboration", true},
		{codexCollabRemovedNamespaces + "\n- other\n\n" + codexCollabRemovedTools + "\n- collaboration.send_message", true},
		{"Updated instructions for the collaboration namespace:\nUse functions.collaboration.spawn_agent.", true},
		{"The collaboration namespace no longer has additional instructions.", true},
	}
	rng := rand.New(rand.NewPCG(17, 29))
	applied := 0
	for i := range 2048 {
		namespace := []byte(codexCollabTestNamespace)
		for tool := range 3 {
			path := fmt.Sprintf("tools.%d.parameters.properties.message.encrypted", tool)
			switch rng.IntN(3) {
			case 0:
				namespace = codexCollabTestSet(t, namespace, path, false)
			case 1:
				var err error
				namespace, err = sjson.DeleteBytes(namespace, path)
				require.NoError(t, err)
			}
		}
		switch i % 8 {
		case 0:
			namespace = codexCollabTestSet(t, namespace, "tools.0.type", "custom")
		case 1:
			namespace = codexCollabTestSet(t, namespace, "tools.0.parameters.properties.message.type", []string{"string", "null"})
		case 2:
			namespace = codexCollabTestSet(t, namespace, "tools.0.parameters.properties.message", json.RawMessage(`{"type":"string","encrypted":true,"encrypted":false}`))
		case 3:
			namespace = codexCollabTestSet(t, namespace, "tools.1.name", "spawn_agent")
		case 4:
			namespace = codexCollabTestSet(t, namespace, "tools.0.parameters.properties.task_name.encrypted", true)
		}
		body := []byte(`{ "input": [] }`)
		switch rng.IntN(4) {
		case 0:
			body = collabDeclarationAppend(t, body, namespace)
		case 1:
			body = codexCollabTestSet(t, body, "tools", []json.RawMessage{namespace})
		case 2:
			body = codexCollabTestSet(t, body, "tools", []json.RawMessage{namespace})
			body = collabDeclarationAppend(t, body, namespace)
		case 3:
			body = collabDeclarationAppend(t, body, namespace)
			body = collabDeclarationAppend(t, body, namespace)
		}
		noticePaths := make(map[string]bool)
		for j := range 2 {
			entry := texts[(i+j)%len(texts)]
			index := len(gjson.GetBytes(body, "input").Array())
			body = collabDeclarationNotice(t, body, entry.text)
			role := "developer"
			if (i/len(texts)+j)%2 == 0 {
				role = "user"
			}
			body = codexCollabTestSet(t, body, fmt.Sprintf("input.%d.role", index), role)
			if entry.notice && role == "developer" {
				noticePaths[fmt.Sprintf("input.%d.content.0.text", index)] = true
			}
		}
		history := []string{
			`{"type":"function_call","namespace":"collaboration","name":"spawn_agent","arguments":"{}"}`,
			`{"type":"function_call","namespace":"collaboration-xy","name":"send_message","arguments":"{}"}`,
			`{"type":"function_call","name":"collaboration-xy__followup_task","arguments":"{}"}`,
			`{"type":"function_call","namespace":"collaboration-xy","name":"unknown","arguments":"{}"}`,
			`{"type":"custom_tool_call","namespace":"other","name":"inspect","input":"opaque"}`,
			`{"type":"function_call","namespace":"collaboration","name":"spawn_agent","name":"send_message","arguments":"{}"}`,
		}
		body = codexCollabTestSet(t, body, "input.-1", json.RawMessage(history[rng.IntN(len(history))]))
		if i%19 == 0 {
			body = append([]byte(`{"tools":[],`), body[1:]...)
		}
		legacyBytes, legacyPlan, legacyOutcome := applyCodexCollaborationPlaintext(body)
		off, offResult := ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{})
		require.Equal(t, legacyBytes, off, "case %d", i)
		require.Equal(t, resilience.CollabPlanOutcome(legacyOutcome), offResult.Outcome)
		out, result := ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{IncrementalDeclarations: true})
		if legacyPlan == nil {
			continue
		}
		applied++
		require.NotNil(t, result.Plan, "case %d", i)
		require.Equal(t, resilience.CollabPlanOutcomeApplied, result.Outcome, "case %d", i)
		// Restore only independently known notice paths. The remaining bytes
		// must match exactly, including all original formatting and history.
		for path := range noticePaths {
			var err error
			out, err = sjson.SetRawBytes(out, path, []byte(gjson.GetBytes(legacyBytes, path).Raw))
			require.NoError(t, err)
		}
		require.Equal(t, legacyBytes, out, "case %d", i)
	}
	require.Greater(t, applied, 500, "ensure the implication exercises accepted bodies")
	t.Logf("generated 2048 requests; %d legacy applications preserved", applied)
}

func TestCollabDeclarationsLegacyFloorDiscardsPartialEdits(t *testing.T) {
	body := collabDeclarationNotice(t, codexCollabTestRequest(), "Updated instructions for the collaboration namespace:\nUse functions.collaboration.spawn_agent.")
	body = collabDeclarationNotice(t, body, codexCollabRemovedTools+"\n- collaboration")
	legacyEdits, legacyPlan, outcome := planCodexCollaborationPlaintext(body)
	require.Equal(t, codexCollabApplied, outcome)
	result := PlanCodexCollaborationDeclarations(body, CollabDeclarationOptions{IncrementalDeclarations: true})
	require.Equal(t, legacyEdits, result.edits)
	require.Equal(t, codexCollabSharedLegacyNamespaces(codexCollabFindNamespaces(gjson.ParseBytes(body)), legacyPlan), result.Plan)
	legacy, _, _ := applyCodexCollaborationPlaintext(body)
	out, _ := ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{IncrementalDeclarations: true})
	require.Equal(t, legacy, out)
	// The sole intentional exception is the client version gate.
	_, old := ApplyCodexCollaborationDeclarations(body, CollabDeclarationOptions{IncrementalDeclarations: true, ClientVersion: "0.146.0"})
	require.Nil(t, old.Plan)
	require.Equal(t, resilience.CollabPlanReasonClientTooOld, old.Reason)
}

func collabDeclarationBenchmarkHistory(b *testing.B, size int, incremental bool) []byte {
	b.Helper()
	var history strings.Builder
	_, _ = history.WriteString(`{"tools":[` + codexCollabTestNamespace + `],"input":[`)
	for i := range 999 {
		if i != 0 {
			_ = history.WriteByte(',')
		}
		_, _ = history.WriteString(`{"type":"message","role":"user","content":"`)
		_, _ = history.WriteString(strings.Repeat("synthetic history ", 270))
		_, _ = history.WriteString(`"}`)
	}
	if incremental {
		_, _ = history.WriteString(`,{"type":"additional_tools","tools":[` + codexCollabTestNamespace + `]}`)
	}
	_, _ = history.WriteString(`,{"type":"message","role":"user","content":"`)
	_, _ = history.WriteString(strings.Repeat("x", size-history.Len()-4))
	_, _ = history.WriteString(`"}]}`)
	body := []byte(history.String())
	if incremental {
		var err error
		body, err = sjson.SetBytes(body, "input.999.tools.0.description", "Tool guides")
		require.NoError(b, err)
	}
	require.Len(b, body, size)
	return body
}

func BenchmarkCollabDeclarationsHistory(b *testing.B) {
	for _, size := range []int{5_000_000, 10_000_000} {
		for _, incremental := range []bool{false, true} {
			b.Run(fmt.Sprintf("%dMB/incremental=%t", size/1_000_000, incremental), func(b *testing.B) {
				raw := collabDeclarationBenchmarkHistory(b, size, incremental)
				var value any
				require.NoError(b, json.Unmarshal(raw, &value))
				normalized, err := json.Marshal(value)
				require.NoError(b, err)
				equalCopy := bytes.Clone(raw)
				options := CollabDeclarationOptions{IncrementalDeclarations: true}
				for _, mode := range []string{"legacy", "off", "on", "on_identical", "off_adapter"} {
					b.Run(mode, func(b *testing.B) {
						b.SetBytes(int64(len(raw) + len(normalized)))
						b.ReportAllocs()
						for b.Loop() {
							switch mode {
							case "legacy", "off":
								// Production off is the original planner, twice.
								for _, body := range [][]byte{raw, normalized} {
									_, plan, outcome := planCodexCollaborationPlaintext(body)
									if (plan == nil) != incremental || outcome == codexCollabInvalid {
										b.Fatal("unexpected legacy result")
									}
								}
							case "on", "on_identical":
								result := PlanCodexCollaborationDeclarations(raw, options)
								next := normalized
								if mode == "on_identical" {
									next = equalCopy
								}
								result = planCodexCollaborationNormalizedDeclarations(raw, next, result, options)
								if result.Plan == nil {
									b.Fatal("missing plan")
								}
							case "off_adapter":
								for _, body := range [][]byte{raw, normalized} {
									result := PlanCodexCollaborationDeclarations(body, CollabDeclarationOptions{})
									if (result.Plan == nil) != incremental {
										b.Fatal("unexpected disabled result")
									}
								}
							}
						}
					})
				}
			})
		}
	}
}
