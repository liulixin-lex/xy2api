package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fcoDiffBS is a single backslash. JSON unicode escapes in this file are
// assembled from it at run time so they cannot be folded away by tooling.
var fcoDiffBS = string(rune(92))

// fcoDiffEscape renders the JSON escape for one BMP code unit, e.g. 0x5f.
func fcoDiffEscape(unit int) string {
	return fmt.Sprintf("%su%04x", fcoDiffBS, unit)
}

// fcoDiffVariant is one raw JSON value for a field, or the field's absence.
type fcoDiffVariant struct {
	label   string
	raw     string
	present bool
}

func fcoDiffPresent(label, raw string) fcoDiffVariant {
	return fcoDiffVariant{label: label, raw: raw, present: true}
}

func fcoDiffMissing() fcoDiffVariant { return fcoDiffVariant{label: "missing"} }

// fcoDiffItem renders one input item. Field order is fixed; a separate test
// covers other orders and non-compact formatting.
func fcoDiffItem(typ, namespace, name, callID, output fcoDiffVariant) string {
	var fields []string
	add := func(key string, v fcoDiffVariant) {
		if v.present {
			fields = append(fields, fmt.Sprintf("%q:%s", key, v.raw))
		}
	}
	add("type", typ)
	add("namespace", namespace)
	add("name", name)
	add("call_id", callID)
	add("output", output)
	return "{" + strings.Join(fields, ",") + "}"
}

func fcoDiffDecode(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value), "decode %s", raw)
	return value
}

var (
	fcoDiffCoreNames = []string{
		"browser_info", "goal_objective", "untrusted_context", "ide_context",
		"untrusted_content", "proposed_plan", "codex_apps_open_page",
		"codex_apps_selected_page_part_1_of_1", "codex_apps_selected_page_part_2_of_3",
		"codex_apps_selected_page_part_3_of_2", "codex_apps_selected_page_part_01_of_2",
		"codex_apps_selected_page_part_0_of_1", "codex_apps_selected_page_part_1_of_99999999999999999999",
	}
	fcoDiffExtNames   = []string{"terminal_info", "browser_tabs"}
	fcoDiffOtherNames = []string{"unknown_name", "Browser_Info", "browser_info ", ""}

	fcoDiffOutputs = []fcoDiffVariant{
		fcoDiffPresent("plain", `"x"`),
		fcoDiffPresent("padded", `"  x \n"`),
		fcoDiffPresent("empty", `""`),
		fcoDiffPresent("blank", `"   "`),
		fcoDiffPresent("markup", `"</external_browser_info><b>&`+fcoDiffBS+`"`+fcoDiffBS+fcoDiffBS+` `+fcoDiffEscape(0xe9)+fcoDiffEscape(0xd83d)+fcoDiffEscape(0xde00)+` `+fcoDiffEscape(0x01)+`"`),
		fcoDiffPresent("page", `"{\"page_id\":\"p1\"}"`),
		fcoDiffPresent("page_null", `"{\"page_id\":null}"`),
		fcoDiffPresent("page_empty", `"{\"page_id\":\"\"}"`),
		fcoDiffPresent("page_blank", `"{\"page_id\":\"  \"}"`),
		fcoDiffPresent("page_number", `"{\"page_id\":7}"`),
		fcoDiffPresent("page_dup", `"{\"page_id\":null,\"page_id\":\"x\"}"`),
		fcoDiffPresent("page_none", `"{}"`),
		fcoDiffPresent("page_trailing", `"{\"page_id\":\"p1\"} tail"`),
		fcoDiffPresent("part_1_1", `"Companion Page snapshot part 1 of 1:\n{\"page_id\":\"p\"}"`),
		fcoDiffPresent("part_1_1_bad", `"Companion Page snapshot part 1 of 1:\nnot json"`),
		fcoDiffPresent("part_2_3_crlf", `"Companion Page snapshot part 2 of 3:\r\nchunk"`),
		fcoDiffPresent("part_2_3_blank", `"Companion Page snapshot part 2 of 3:\n   "`),
		fcoDiffPresent("part_2_3_no_body", `"Companion Page snapshot part 2 of 3:"`),
		fcoDiffPresent("part_3_2", `"Companion Page snapshot part 3 of 2:\nx"`),
		fcoDiffPresent("part_overflow", `"Companion Page snapshot part 1 of 9223372036854775807:\nx"`),
		fcoDiffPresent("part_wrong_header", `"companion page snapshot part 2 of 3:\nchunk"`),
		fcoDiffPresent("null", `null`),
		fcoDiffPresent("array", `["x"]`),
		fcoDiffPresent("empty_array", `[]`),
		fcoDiffPresent("object", `{"text":"x"}`),
		fcoDiffPresent("number", `42`),
		fcoDiffMissing(),
	}

	fcoDiffCallIDs = []fcoDiffVariant{
		fcoDiffMissing(),
		fcoDiffPresent("null", `null`),
		fcoDiffPresent("empty", `""`),
		fcoDiffPresent("blank", `"  "`),
		fcoDiffPresent("string", `"call_1"`),
		fcoDiffPresent("number", `42`),
		fcoDiffPresent("object", `{}`),
	}
)

// fcoDiffEnvelopes wrap one item in request bodies that exercise unrelated
// items, formatting, large numbers and same-bytes decoys.
func fcoDiffEnvelopes(item string) map[string]string {
	return fcoDiffEnvelopesWith(item, item)
}

// fcoDiffEnvelopesWith places inputItem inside input and decoyItem everywhere
// else, so the expected bytes after a rewrite can be built exactly.
func fcoDiffEnvelopesWith(inputItem, decoyItem string) map[string]string {
	item := inputItem
	return map[string]string{
		"single": `{"model":"m","input":[` + item + `]}`,
		"mixed": "{\n  \"metadata\" : { \"n\" : 9007199254740993, \"f\" : 1.0e+2 },\n  \"input\" : [ { \"type\" : \"message\", \"role\" : \"user\", \"content\" : \"keep\" },\n    " +
			item + " ,\n    {\"type\":\"function_call\",\"call_id\":\"call_9\",\"name\":\"f\",\"arguments\":\"{}\"}, {\"type\":\"function_call_output\",\"call_id\":\"call_9\",\"output\":\"done\"} ],\n  \"stream\" : true\n}",
		"decoy_before": `{"shadow":[` + decoyItem + `],"note":` + fmt.Sprintf("%q", decoyItem) + `,"input":[` + item + `]}`,
		"decoy_after":  `{"input":[` + item + `,` + item + `],"shadow":[` + decoyItem + `]}`,
	}
}

func fcoDiffIsBlankString(v fcoDiffVariant) bool {
	return v.present && (v.label == "empty" || v.label == "blank")
}

// fcoDiffUpstream runs the verbatim upstream port behind the literal half of
// upstream's candidate pre-filter. Upstream additionally accepts bodies that
// spell function_call_output with JSON escapes; this gateway deliberately
// leaves those untouched, see TestFCOCompatLeavesEscapedTypeLiteralUntouched.
func fcoDiffUpstream(body []byte) ([]byte, bool) {
	if !bytes.Contains(body, []byte("function_call_output")) {
		return body, false
	}
	return upstreamRefNormalizeUntrustedContext(body)
}

// fcoDiffStringOutput decodes the variant when it is a JSON string.
func fcoDiffStringOutput(t *testing.T, v fcoDiffVariant) (string, bool) {
	t.Helper()
	if !v.present || !strings.HasPrefix(v.raw, `"`) {
		return "", false
	}
	var decoded string
	require.NoError(t, json.Unmarshal([]byte(v.raw), &decoded), "output variant %s", v.label)
	return decoded, true
}

// fcoDiffMessage is the exact message a rule without the page template must
// produce for one item.
func fcoDiffMessage(name, output string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": "<external_" + name + ">" + output + "</external_" + name + ">",
		}},
	}
}

// fcoDiffReplacement returns the bytes one item is rewritten to, read from a
// minimal request, or false when the item is left alone.
func fcoDiffReplacement(t *testing.T, item string, rules FCOCompatRules) (string, bool) {
	t.Helper()
	const prefix, suffix = `{"model":"m","input":[`, `]}`
	body := prefix + item + suffix
	got, _, err := normalizeOpenAIFCOBody([]byte(body), rules)
	require.NoError(t, err, "item=%s", item)
	if string(got) == body {
		return "", false
	}
	require.True(t, strings.HasPrefix(string(got), prefix) && strings.HasSuffix(string(got), suffix),
		"bytes around input changed: %s", got)
	return string(got[len(prefix) : len(got)-len(suffix)]), true
}

// fcoDiffRequireRewrite checks one body three ways: the rewrite is idempotent,
// every byte outside the candidate items inside input is preserved (decoys
// with identical bytes elsewhere included), and the outcome is returned for
// the caller to compare with upstream.
func fcoDiffRequireRewrite(t *testing.T, rules FCOCompatRules, item, envelope, label string) (got []byte, changed bool, replacement string) {
	t.Helper()
	body := fcoDiffEnvelopes(item)[envelope]
	got, _, err := normalizeOpenAIFCOBody([]byte(body), rules)
	require.NoError(t, err, label)
	again, _, err := normalizeOpenAIFCOBody(got, rules)
	require.NoError(t, err, label)
	require.Equal(t, string(got), string(again), "not idempotent: %s", label)

	replacement, changed = fcoDiffReplacement(t, item, rules)
	want := body
	if changed {
		want = fcoDiffEnvelopesWith(replacement, item)[envelope]
	}
	require.Equal(t, want, string(got), "bytes outside the rewritten items changed, or the wrong item was rewritten: %s", label)
	return got, changed, replacement
}

func fcoDiffRequireSameAsUpstream(t *testing.T, body string, got []byte, changed bool, label string) {
	t.Helper()
	upstream, upstreamChanged := fcoDiffUpstream([]byte(body))
	require.Equal(t, upstreamChanged, changed, "rewrite decision differs from upstream: %s\nbody=%s\ngot=%s\nupstream=%s", label, body, got, upstream)
	if changed {
		require.True(t, reflect.DeepEqual(fcoDiffDecode(t, upstream), fcoDiffDecode(t, got)),
			"rewrite differs from upstream: %s\nbody=%s\ngot=%s\nupstream=%s", label, body, got, upstream)
	}
}

// TestFCOCompatMatchesUpstreamReference compares the rewrite with the verbatim
// upstream port on a cross product of names, outputs and call_id shapes, once
// with the built-in rules and once with an administrator list. Every body is
// also checked byte for byte outside the rewritten items. Only these
// differences from upstream are allowed:
//
//  1. an empty or whitespace call_id: upstream rewrites, this gateway does not;
//  2. an explicit JSON null call_id: this gateway rewrites exactly as if the
//     key were missing, upstream does not;
//  3. the ext names and administrator names: this gateway rewrites them with
//     the plain template, upstream does not know them.
//
// A fourth difference, the escaped type literal, has its own test.
func TestFCOCompatMatchesUpstreamReference(t *testing.T) {
	const adminName = "custom_name"
	adminRules := DefaultOpenAIFCORules([]string{adminName, "browser_info", "codex_apps_selected_page_part_1_of_1", "terminal_info", "bad-name"})
	require.Equal(t, []string{adminName}, adminRules.AdminNames, "built-in and malformed names must not enter the administrator list")

	ruleSets := []struct {
		label string
		rules FCOCompatRules
		admin map[string]bool
	}{
		{"builtin", DefaultOpenAIFCORules(nil), map[string]bool{}},
		{"admin", adminRules, map[string]bool{adminName: true}},
	}

	typ := fcoDiffPresent("fco", `"function_call_output"`)
	namespace := fcoDiffPresent("agent", `"agent"`)
	names := append(append(append([]string{}, fcoDiffCoreNames...), fcoDiffExtNames...), fcoDiffOtherNames...)
	names = append(names, adminName)
	isExt := map[string]bool{}
	for _, name := range fcoDiffExtNames {
		isExt[name] = true
	}

	compared := 0
	for _, ruleSet := range ruleSets {
		for _, name := range names {
			nameVariant := fcoDiffPresent(name, fmt.Sprintf("%q", name))
			for _, output := range fcoDiffOutputs {
				for _, callID := range fcoDiffCallIDs {
					item := fcoDiffItem(typ, namespace, nameVariant, callID, output)
					for envelope, body := range fcoDiffEnvelopes(item) {
						label := fmt.Sprintf("rules=%s name=%q output=%s call_id=%s envelope=%s", ruleSet.label, name, output.label, callID.label, envelope)
						got, changed, replacement := fcoDiffRequireRewrite(t, ruleSet.rules, item, envelope, label)
						_, upstreamChanged := fcoDiffUpstream([]byte(body))
						compared++

						switch {
						case fcoDiffIsBlankString(callID):
							require.False(t, changed, "blank call_id must stay untouched: %s", label)
						case callID.present && callID.label == "null":
							require.False(t, upstreamChanged, "upstream does not rewrite a null call_id: %s", label)
							twin := fcoDiffItem(typ, namespace, nameVariant, fcoDiffMissing(), output)
							twinReplacement, twinChanged := fcoDiffReplacement(t, twin, ruleSet.rules)
							require.Equal(t, twinChanged, changed, "null and missing call_id must be decided alike: %s", label)
							require.Equal(t, twinReplacement, replacement, "null and missing call_id must rewrite to the same bytes: %s", label)
						case isExt[name] || ruleSet.admin[name]:
							require.False(t, upstreamChanged, "upstream does not know this name: %s", label)
							decoded, isString := fcoDiffStringOutput(t, output)
							wantChanged := !callID.present && isString && strings.TrimSpace(decoded) != ""
							require.Equal(t, wantChanged, changed, label)
							if changed {
								require.Equal(t, fcoDiffMessage(name, decoded), fcoDiffDecode(t, []byte(replacement)), label)
							}
						default:
							fcoDiffRequireSameAsUpstream(t, body, got, changed, label)
						}
					}
				}
			}
		}
	}
	require.Greater(t, compared, 25000)
}

// TestFCOCompatMatchesUpstreamReferenceOnItemShape varies the fields that
// decide whether an item is a candidate at all. A missing call_id keeps the
// comparison free of the intentional differences.
func TestFCOCompatMatchesUpstreamReferenceOnItemShape(t *testing.T) {
	rules := DefaultOpenAIFCORules(nil)
	types := []fcoDiffVariant{
		fcoDiffPresent("fco", `"function_call_output"`),
		fcoDiffPresent("padded", `" function_call_output"`),
		fcoDiffPresent("custom", `"custom_tool_call_output"`),
		fcoDiffPresent("message", `"message"`),
		fcoDiffPresent("null", `null`),
		fcoDiffPresent("number", `42`),
		fcoDiffMissing(),
	}
	namespaces := []fcoDiffVariant{
		fcoDiffPresent("agent", `"agent"`),
		fcoDiffPresent("upper", `"Agent"`),
		fcoDiffPresent("padded", `" agent"`),
		fcoDiffPresent("other", `"codex_app"`),
		fcoDiffPresent("null", `null`),
		fcoDiffPresent("number", `42`),
		fcoDiffMissing(),
	}
	nameVariants := []fcoDiffVariant{
		fcoDiffPresent("known", `"browser_info"`),
		fcoDiffPresent("page", `"codex_apps_open_page"`),
		fcoDiffPresent("null", `null`),
		fcoDiffPresent("number", `42`),
		fcoDiffPresent("array", `["browser_info"]`),
		fcoDiffMissing(),
	}
	outputs := []fcoDiffVariant{
		fcoDiffPresent("plain", `"x"`),
		fcoDiffPresent("page", `"{\"page_id\":\"p1\"}"`),
	}
	for _, typ := range types {
		for _, namespace := range namespaces {
			for _, name := range nameVariants {
				for _, output := range outputs {
					item := fcoDiffItem(typ, namespace, name, fcoDiffMissing(), output)
					for envelope, body := range fcoDiffEnvelopes(item) {
						label := fmt.Sprintf("type=%s namespace=%s name=%s output=%s envelope=%s", typ.label, namespace.label, name.label, output.label, envelope)
						got, changed, _ := fcoDiffRequireRewrite(t, rules, item, envelope, label)
						fcoDiffRequireSameAsUpstream(t, body, got, changed, label)
					}
				}
			}
		}
	}
}

// TestFCOCompatMatchesUpstreamReferenceOnRequestShape covers whole-request
// shapes: duplicate members at every depth, non-array input, non-object
// elements, key order, JSON-escaped keys and values, and trailing data.
func TestFCOCompatMatchesUpstreamReferenceOnRequestShape(t *testing.T) {
	rules := DefaultOpenAIFCORules(nil)
	item := `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x"}`
	underscore, letterP, letterY, letterA := fcoDiffEscape(0x5f), fcoDiffEscape(0x70), fcoDiffEscape(0x79), fcoDiffEscape(0x61)
	bodies := map[string]string{
		"duplicate_top_level":      `{"model":"a","model":"b","input":[` + item + `]}`,
		"duplicate_input":          `{"input":[],"input":[` + item + `]}`,
		"duplicate_in_item":        `{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x","output":"y"}]}`,
		"duplicate_in_other_item":  `{"input":[` + item + `,{"type":"message","role":"user","role":"system","content":"c"}]}`,
		"duplicate_deep":           `{"metadata":{"a":{"b":1,"b":2}},"input":[` + item + `]}`,
		"duplicate_escaped_key":    `{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x","out` + letterP + `ut":"y"}]}`,
		"duplicate_escaped_input":  `{"in` + letterP + `ut":[],"input":[` + item + `]}`,
		"input_string":             `{"input":"function_call_output"}`,
		"input_object":             `{"input":` + item + `}`,
		"input_null":               `{"input":null,"note":"function_call_output"}`,
		"input_missing":            `{"note":"function_call_output"}`,
		"non_object_elements":      `{"input":["function_call_output",42,null,[` + item + `],` + item + `]}`,
		"nested_input":             `{"wrapper":{"input":[` + item + `]},"input":[]}`,
		"key_order":                `{"input":[{"output":"x","name":"browser_info","namespace":"agent","type":"function_call_output","id":"fco_1","status":"completed"}]}`,
		"escaped_call_id_key":      `{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","call` + underscore + `id":"call_1","output":"x"}]}`,
		"escaped_type_key":         `{"input":[{"t` + letterY + `pe":"function_call_output","namespace":"agent","name":"browser_info","output":"x"}]}`,
		"escaped_namespace_key":    `{"input":[{"type":"function_call_output","n` + letterA + `mespace":"agent","name":"browser_info","output":"x"}]}`,
		"escaped_name_key":         `{"input":[{"type":"function_call_output","namespace":"agent","n` + letterA + `me":"browser_info","output":"x"}]}`,
		"escaped_output_key":       `{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","out` + letterP + `ut":"x"}]}`,
		"escaped_name_value":       `{"input":[{"type":"function_call_output","namespace":"agent","name":"browser` + underscore + `info","output":"x"}]}`,
		"escaped_namespace_value":  `{"input":[{"type":"function_call_output","namespace":"` + letterA + `gent","name":"browser_info","output":"x"}]}`,
		"escaped_input_key":        `{"in` + letterP + `ut":[` + item + `]}`,
		"escaped_decoy_then_input": `{"shadow":[` + item + `],"in` + letterP + `ut":[` + item + `]}`,
		"trailing_data":            `{"input":[` + item + `]} {"x":1}`,
		"top_level_array":          `[` + item + `]`,
		"top_level_string":         `"function_call_output"`,
		"invalid_json":             `{"input":[` + item + `]`,
		"empty_input":              `{"input":[],"note":"function_call_output"}`,
		"whitespace_wrapped":       "  \n{ \"input\" :\t[ " + item + " ]\n}  \n",
	}
	for label, body := range bodies {
		t.Run(label, func(t *testing.T) {
			if strings.Contains(label, "escaped") {
				require.Contains(t, body, fcoDiffBS+"u", "an escaped case must really carry a JSON escape")
			}
			got, _, err := normalizeOpenAIFCOBody([]byte(body), rules)
			require.NoError(t, err)
			changed := !bytes.Equal(got, []byte(body))
			fcoDiffRequireSameAsUpstream(t, body, got, changed, label)
			if changed {
				again, _, err := normalizeOpenAIFCOBody(got, rules)
				require.NoError(t, err)
				require.Equal(t, string(got), string(again))
			}
		})
	}
}

// TestFCOCompatLeavesEscapedTypeLiteralUntouched pins the fourth intentional
// difference. Upstream's pre-filter lets a body through when it may spell
// function_call_output with JSON escapes; this gateway requires the literal
// text and leaves such a body alone. No known client escapes plain ASCII, and
// leaving the body alone only means the upstream decides.
func TestFCOCompatLeavesEscapedTypeLiteralUntouched(t *testing.T) {
	underscore := fcoDiffEscape(0x5f)
	body := `{"input":[{"type":"function` + underscore + `call` + underscore + `output","namespace":"agent","name":"browser_info","output":"x"}]}`
	require.NotContains(t, body, "function_call_output")

	got, summary, err := normalizeOpenAIFCOBody([]byte(body), DefaultOpenAIFCORules(nil))
	require.NoError(t, err)
	require.Equal(t, body, string(got))
	require.Zero(t, summary.Matched)

	_, upstreamChanged := upstreamRefNormalizeUntrustedContext([]byte(body))
	require.True(t, upstreamChanged, "the verbatim upstream port rewrites it once past the pre-filter")
}
