package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/tidwall/gjson"
)

type fcoArrayElement struct {
	start, end int
	raw        []byte
}

// FCOCompatRules is an immutable snapshot of the built-in and administrator
// supplied function_call_output rules. AdminNames are deliberately accepted as
// data so callers can bind a request to one rules snapshot.
type FCOCompatRules struct {
	Version    string
	AdminNames []string
}

const (
	fcoCoreVersion  = "core-328db26f4"
	fcoExtVersion   = "ext-1"
	fcoAdminVersion = "admin-1"
)

var fcoAdminNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)
var fcoSelectedPageNameRE = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

// DefaultOpenAIFCORules returns a fresh rules snapshot.
func DefaultOpenAIFCORules(adminNames []string) FCOCompatRules {
	r := FCOCompatRules{Version: fcoCoreVersion}
	seen := make(map[string]struct{}, len(adminNames))
	for _, name := range adminNames {
		if _, builtin := fcoBuiltinLayer(name); builtin || !fcoAdminNameRE.MatchString(name) {
			continue
		}
		if _, ok := seen[name]; ok || len(r.AdminNames) >= 64 {
			continue
		}
		seen[name] = struct{}{}
		r.AdminNames = append(r.AdminNames, name)
	}
	return r
}

// FCOCompatMatch identifies a rule that matched one item.
type FCOCompatMatch struct {
	Layer   string
	Name    string
	Version string
	Wrapped bool
}

func fcoBuiltinLayer(name string) (string, bool) {
	switch name {
	case "browser_info", "goal_objective", "untrusted_context", "ide_context", "untrusted_content", "proposed_plan", "codex_apps_open_page":
		return "core", true
	case "terminal_info", "browser_tabs":
		return "ext", true
	}
	if fcoSelectedPageNameRE.MatchString(name) {
		return "core", true
	}
	return "", false
}

func fcoBuiltinValid(name, output string) (bool, bool) {
	_, ok := fcoBuiltinLayer(name)
	if !ok {
		return false, false
	}
	if name == "codex_apps_open_page" || fcoSelectedPageNameRE.MatchString(name) {
		return validFCOPageContext(name, output), true
	}
	switch name {
	case "untrusted_content":
		return true, true
	case "proposed_plan":
		return output != "", true
	default:
		return strings.TrimSpace(output) != "", true
	}
}

// matchOpenAIFCOItem applies the exact PR1 matching rules to one input item.
func matchOpenAIFCOItem(item gjson.Result, rules FCOCompatRules) (FCOCompatMatch, bool) {
	if item.Get("type").Type != gjson.String || item.Get("type").String() != "function_call_output" ||
		item.Get("namespace").Type != gjson.String || item.Get("namespace").String() != "agent" {
		return FCOCompatMatch{}, false
	}
	callID := item.Get("call_id")
	if callID.Exists() && callID.Type != gjson.Null {
		return FCOCompatMatch{}, false
	}
	if item.Get("output").Type != gjson.String {
		return FCOCompatMatch{}, false
	}
	name := item.Get("name")
	if name.Type != gjson.String {
		return FCOCompatMatch{}, false
	}
	output := item.Get("output").String()
	if layer, builtin := fcoBuiltinLayer(name.String()); builtin {
		valid, _ := fcoBuiltinValid(name.String(), output)
		if !valid {
			return FCOCompatMatch{}, false
		}
		version := fcoVersionForLayer(name.String())
		if callID.Exists() && callID.Type == gjson.Null && layer == "core" {
			layer, version = "ext", fcoExtVersion
		}
		return FCOCompatMatch{Layer: layer, Name: name.String(), Version: version, Wrapped: name.String() == "codex_apps_open_page" || fcoSelectedPageNameRE.MatchString(name.String())}, true
	}
	for _, admin := range rules.AdminNames {
		if admin == name.String() && fcoAdminNameRE.MatchString(admin) && strings.TrimSpace(output) != "" {
			return FCOCompatMatch{Layer: "admin", Name: admin, Version: fcoAdminVersion}, true
		}
	}
	return FCOCompatMatch{}, false
}

func fcoVersionForLayer(name string) string {
	if layer, _ := fcoBuiltinLayer(name); layer == "ext" {
		return fcoExtVersion
	}
	return fcoCoreVersion
}

// FCOCompatSummary describes a normalization pass without retaining payloads.
type FCOCompatSummary struct {
	Matched int
	Layers  map[string]int
	Names   map[string]int
}

// normalizeOpenAIFCOBody replaces recognized standalone outputs with user
// messages. It returns the original bytes for malformed or duplicate-member
// requests, and is idempotent.
func normalizeOpenAIFCOBody(body []byte, rules FCOCompatRules) ([]byte, FCOCompatSummary, error) {
	summary := FCOCompatSummary{Layers: map[string]int{}, Names: map[string]int{}}
	if !bytes.Contains(body, []byte(`function_call_output`)) || !pkghttputil.HasUniqueJSONMembers(body) {
		return body, summary, nil
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, summary, nil
	}
	inputResult := gjson.GetBytes(body, "input")
	if !inputResult.Exists() || inputResult.Index < 0 {
		return body, summary, nil
	}
	rawInput := []byte(inputResult.Raw)
	elements, ok := splitFCOJSONArray(rawInput)
	if !ok {
		return body, summary, nil
	}
	changed := false
	replacements := make(map[int][]byte)
	for i, element := range elements {
		raw := element.raw
		item := gjson.ParseBytes(raw)
		match, ok := matchOpenAIFCOItem(item, rules)
		if !ok {
			continue
		}
		output := item.Get("output").String()
		text := "<external_" + match.Name + ">" + output + "</external_" + match.Name + ">"
		if match.Wrapped {
			text = "<external_" + match.Name + ">\n" + output + "\n</external_" + match.Name + ">"
		}
		encoded, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}})
		if err != nil {
			return body, summary, err
		}
		replacements[i], changed = encoded, true
		summary.Matched++
		summary.Layers[match.Layer]++
		if match.Layer == "admin" {
			summary.Names["other"]++
		} else if fcoSelectedPageNameRE.MatchString(match.Name) {
			summary.Names["codex_apps_selected_page_part"]++
		} else {
			summary.Names[match.Name]++
		}
	}
	if !changed {
		return body, summary, nil
	}
	var rebuilt bytes.Buffer
	pos := 0
	for i, element := range elements {
		if _, err := rebuilt.Write(rawInput[pos:element.start]); err != nil {
			return body, summary, err
		}
		if replacement, ok := replacements[i]; ok {
			if _, err := rebuilt.Write(replacement); err != nil {
				return body, summary, err
			}
		} else if _, err := rebuilt.Write(element.raw); err != nil {
			return body, summary, err
		}
		pos = element.end
	}
	if _, err := rebuilt.Write(rawInput[pos:]); err != nil {
		return body, summary, err
	}
	inputPos := inputResult.Index
	if inputPos < 0 || inputPos+len(rawInput) > len(body) || !bytes.Equal(body[inputPos:inputPos+len(rawInput)], rawInput) {
		return body, summary, nil
	}
	result := make([]byte, 0, len(body)-len(rawInput)+rebuilt.Len())
	result = append(result, body[:inputPos]...)
	result = append(result, rebuilt.Bytes()...)
	result = append(result, body[inputPos+len(rawInput):]...)
	return result, summary, nil
}

// trimFCOJSONSpaceEnd moves end back over JSON whitespace so an element's
// range covers the value only. Whitespace between a value and the following
// comma or bracket then survives a rewrite of that value.
func trimFCOJSONSpaceEnd(raw []byte, start, end int) int {
	for end > start && (raw[end-1] == ' ' || raw[end-1] == '\n' || raw[end-1] == '\r' || raw[end-1] == '\t') {
		end--
	}
	return end
}

func splitFCOJSONArray(raw []byte) ([]fcoArrayElement, bool) {
	start := 0
	for start < len(raw) && (raw[start] == ' ' || raw[start] == '\n' || raw[start] == '\r' || raw[start] == '\t') {
		start++
	}
	if start >= len(raw) || raw[start] != '[' {
		return nil, false
	}
	i := start + 1
	result := []fcoArrayElement{}
	for {
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\n' || raw[i] == '\r' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw) {
			return nil, false
		}
		if raw[i] == ']' {
			return result, true
		}
		valueStart := i
		depth, stringMode, escaped := 0, false, false
		for i < len(raw) {
			c := raw[i]
			if stringMode {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					stringMode = false
				}
				i++
				continue
			}
			switch c {
			case '"':
				stringMode = true
			case '[', '{':
				depth++
			case ']', '}':
				if c == ']' && depth == 0 {
					valueEnd := trimFCOJSONSpaceEnd(raw, valueStart, i)
					result = append(result, fcoArrayElement{valueStart, valueEnd, raw[valueStart:valueEnd]})
					return result, true
				}
				depth--
			case ',':
				if depth == 0 {
					valueEnd := trimFCOJSONSpaceEnd(raw, valueStart, i)
					result = append(result, fcoArrayElement{valueStart, valueEnd, raw[valueStart:valueEnd]})
					i++
					goto next
				}
			}
			i++
		}
		return nil, false
	next:
	}
}

// matchOpenAIFCOShape also categorizes recognized names with a non-null call ID.
// These items are not eligible for rewriting, but their shapes remain useful on refusal.
func matchOpenAIFCOShape(item gjson.Result, rules FCOCompatRules) (FCOCompatMatch, bool) {
	if match, ok := matchOpenAIFCOItem(item, rules); ok {
		return match, true
	}
	callID := item.Get("call_id")
	name, output := item.Get("name"), item.Get("output")
	if !callID.Exists() || callID.Type == gjson.Null || name.Type != gjson.String || output.Type != gjson.String {
		return FCOCompatMatch{}, false
	}
	if layer, builtin := fcoBuiltinLayer(name.String()); builtin {
		if valid, _ := fcoBuiltinValid(name.String(), output.String()); valid {
			return FCOCompatMatch{Layer: layer, Name: name.String()}, true
		}
		return FCOCompatMatch{}, false
	}
	for _, admin := range rules.AdminNames {
		if admin == name.String() && fcoAdminNameRE.MatchString(admin) && strings.TrimSpace(output.String()) != "" {
			return FCOCompatMatch{Layer: "admin", Name: admin}, true
		}
	}
	return FCOCompatMatch{}, false
}

// FCOShapeSummary is a bounded structural summary suitable for logs/metrics.
type FCOShapeSummary struct {
	IndependentFCOCount string   `json:"independent_fco_count"`
	Layers              []string `json:"layers,omitempty"`
	Names               []string `json:"names,omitempty"`
	CallIDShapes        []string `json:"call_id_shapes,omitempty"`
	OutputShapes        []string `json:"output_shapes,omitempty"`
	CallsWithCallID     string   `json:"calls_with_call_id"`
	PreviousResponseID  bool     `json:"previous_response_id"`
	WireDuplicateKeys   bool     `json:"wire_duplicate_keys"`
}

func bucketFCOCount(n int) string {
	if n == 0 {
		return "0"
	}
	if n == 1 {
		return "1"
	}
	if n == 2 {
		return "2"
	}
	return "3_plus"
}
func bucketFCOCalls(n int) string {
	if n == 0 {
		return "0"
	}
	if n == 1 {
		return "1"
	}
	if n == 2 {
		return "2"
	}
	return "3_plus"
}

// summarizeOpenAIFCOBody extracts only bounded shape categories.
func summarizeOpenAIFCOBody(body []byte, rules FCOCompatRules) FCOShapeSummary {
	return summarizeOpenAIFCOBodyMode(body, rules, true)
}

// summarizeOpenAIFCOBodyMode skips duplicate-key parsing for unobserved success samples.
func summarizeOpenAIFCOBodyMode(body []byte, rules FCOCompatRules, full bool) FCOShapeSummary {
	result := FCOShapeSummary{IndependentFCOCount: "0", CallsWithCallID: "0"}
	if !bytes.Contains(body, []byte(`function_call_output`)) {
		return result
	}
	items := gjson.GetBytes(body, "input").Array()
	independent, callsWithID := 0, 0
	callShapes := map[string]struct{}{}
	outputShapes := map[string]struct{}{}
	layerBuckets := map[string]struct{}{}
	nameBuckets := map[string]struct{}{}
	for _, item := range items {
		if item.Get("type").String() == "function_call_output" {
			if item.Get("namespace").String() != "agent" {
				layerBuckets["other"] = struct{}{}
				nameBuckets["other"] = struct{}{}
			} else if match, ok := matchOpenAIFCOShape(item, rules); ok {
				layerBuckets[match.Layer] = struct{}{}
				if match.Layer == "admin" {
					nameBuckets["other"] = struct{}{}
				} else if fcoSelectedPageNameRE.MatchString(match.Name) {
					nameBuckets["codex_apps_selected_page_part"] = struct{}{}
				} else {
					nameBuckets[match.Name] = struct{}{}
				}
			} else {
				layerBuckets["other"] = struct{}{}
				nameBuckets["other"] = struct{}{}
			}
			if !item.Get("call_id").Exists() || item.Get("call_id").Type == gjson.Null {
				independent++
			}
			var shape string
			if !item.Get("call_id").Exists() {
				shape = "missing"
			} else if item.Get("call_id").Type == gjson.Null {
				shape = "null"
			} else if item.Get("call_id").Type != gjson.String {
				shape = "wrong_type"
			} else if item.Get("call_id").String() == "" {
				shape = "empty"
			} else if strings.TrimSpace(item.Get("call_id").String()) == "" {
				shape = "whitespace"
			} else {
				shape = "string"
			}
			callShapes[shape] = struct{}{}
			outShape := "other"
			switch item.Get("output").Type {
			case gjson.String:
				outShape = "string"
			case gjson.JSON:
				if strings.HasPrefix(strings.TrimSpace(item.Get("output").Raw), "[") {
					outShape = "array"
				}
			}
			outputShapes[outShape] = struct{}{}
		}
		t := item.Get("type").String()
		if (t == "function_call" || t == "tool_call" || strings.HasSuffix(t, "_call")) && item.Get("call_id").Type == gjson.String && strings.TrimSpace(item.Get("call_id").String()) != "" {
			callsWithID++
		}
	}
	result.IndependentFCOCount = bucketFCOCount(independent)
	result.CallsWithCallID = bucketFCOCalls(callsWithID)
	if independent == 0 && !full {
		return result
	}
	result.WireDuplicateKeys = !pkghttputil.HasUniqueJSONMembers(body)
	previous := gjson.GetBytes(body, "previous_response_id")
	result.PreviousResponseID = previous.Type == gjson.String && strings.TrimSpace(previous.String()) != ""
	for _, v := range []string{"core", "ext", "admin", "other"} {
		if _, ok := layerBuckets[v]; ok {
			result.Layers = append(result.Layers, v)
		}
	}
	for _, v := range []string{"browser_info", "goal_objective", "untrusted_context", "ide_context", "untrusted_content", "proposed_plan", "codex_apps_open_page", "codex_apps_selected_page_part", "terminal_info", "browser_tabs", "other"} {
		if _, ok := nameBuckets[v]; ok {
			result.Names = append(result.Names, v)
		}
	}
	for _, v := range []string{"missing", "null", "empty", "whitespace", "wrong_type", "string"} {
		if _, ok := callShapes[v]; ok {
			result.CallIDShapes = append(result.CallIDShapes, v)
		}
	}
	for _, v := range []string{"string", "array", "other"} {
		if _, ok := outputShapes[v]; ok {
			result.OutputShapes = append(result.OutputShapes, v)
		}
	}
	return result
}

func validFCOPageContext(name, output string) bool {
	if name == "codex_apps_open_page" {
		return validFCOSnapshot(output)
	}
	parts := fcoSelectedPageNameRE.FindStringSubmatch(name)
	if len(parts) != 3 {
		return false
	}
	part, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	total, err := strconv.Atoi(parts[2])
	if err != nil {
		return false
	}
	if part > total {
		return false
	}
	header, snapshot, ok := strings.Cut(output, "\n")
	if !ok || strings.TrimSuffix(header, "\r") != fmt.Sprintf("Companion Page snapshot part %d of %d:", part, total) || strings.TrimSpace(snapshot) == "" {
		return false
	}
	return total > 1 || validFCOSnapshot(snapshot)
}
func validFCOSnapshot(text string) bool {
	if !pkghttputil.HasUniqueJSONMembers([]byte(text)) {
		return false
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &snapshot) != nil {
		return false
	}
	id, ok := snapshot["page_id"]
	if !ok {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		return true
	}
	var value string
	return json.Unmarshal(id, &value) == nil && strings.TrimSpace(value) != ""
}
