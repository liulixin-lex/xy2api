// Package fcorelay provides a small deterministic Responses upstream fixture.
// It intentionally depends only on the standard library so compatibility tests
// can exercise the wire contract without a live provider. The three versions
// use the same validation order and differ only in their rewrite allowlists;
// v0.2.20 performs no agent-context rewrite. codex_app delegation and
// automation rewrites are deliberately not modeled here.
package fcorelay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	VersionV0220         = "v0.2.20"
	VersionV0222         = "v0.2.22"
	VersionV0224         = "v0.2.24"
	refusal              = "function_call_output requires call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2"
	itemReferenceRefusal = "function_call_output requires item_reference ids matching each call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2"
)

var selectedPage = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

// NewHandler returns a fake upstream for one compatibility version.
func NewHandler(version string) http.Handler {
	if version != VersionV0220 && version != VersionV0222 && version != VersionV0224 {
		panic("unsupported fcorelay version: " + version)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handle(w, r, version) })
}

func handle(w http.ResponseWriter, r *http.Request, version string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	var items []json.RawMessage
	_ = json.Unmarshal(req["input"], &items)
	if hasUniqueJSONMembers(body) {
		items = rewrite(items, version)
	}
	if hasPreciseFCO(items) {
		if strings.TrimSpace(nonEmptyWireValue(req["previous_response_id"])) != "" || hasNonEmptyCallContext(items) {
			goto success
		}
		if missingOutputCallID(items) {
			writeRefusal(w, refusal)
			return
		}
		if !itemReferencesCovered(items) {
			writeRefusal(w, itemReferenceRefusal)
			return
		}
	}
success:
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\"}}\n\n")
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\"}}\n\n")
}

func writeRefusal(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "invalid_request_error", "message": message}})
}

func hasPreciseFCO(items []json.RawMessage) bool {
	for _, raw := range items {
		var x map[string]json.RawMessage
		if json.Unmarshal(raw, &x) == nil && stringValue(x["type"]) == "function_call_output" {
			return true
		}
	}
	return false
}
func hasNonEmptyCallContext(items []json.RawMessage) bool {
	for _, raw := range items {
		var x map[string]json.RawMessage
		if json.Unmarshal(raw, &x) != nil {
			continue
		}
		typ := stringValue(x["type"])
		if typ == "function_call" || typ == "tool_call" || typ == "local_shell_call" || typ == "tool_search_call" || typ == "custom_tool_call" || typ == "mcp_tool_call" {
			if strings.TrimSpace(nonEmptyWireValue(x["call_id"])) != "" {
				return true
			}
		}
	}
	return false
}

var outputTypes = map[string]bool{
	"function_call_output":    true,
	"custom_tool_call_output": true,
	"mcp_tool_call_output":    true,
	"tool_search_output":      true,
}

func missingOutputCallID(items []json.RawMessage) bool {
	for _, raw := range items {
		var x map[string]json.RawMessage
		if json.Unmarshal(raw, &x) != nil {
			continue
		}
		if !outputTypes[stringValue(x["type"])] {
			continue
		}
		if strings.TrimSpace(nonEmptyWireValue(x["call_id"])) == "" {
			return true
		}
	}
	return false
}
func itemReferencesCovered(items []json.RawMessage) bool {
	ids := map[string]bool{}
	refs := map[string]bool{}
	for _, raw := range items {
		var x map[string]json.RawMessage
		if json.Unmarshal(raw, &x) != nil {
			continue
		}
		typ := stringValue(x["type"])
		if outputTypes[stringValue(x["type"])] {
			if id := nonEmptyWireValue(x["call_id"]); strings.TrimSpace(id) != "" {
				ids[id] = true
			}
		}
		if typ == "item_reference" {
			if id := nonEmptyWireValue(x["id"]); strings.TrimSpace(id) != "" {
				refs[id] = true
			}
		}
	}
	for id := range ids {
		if !refs[id] {
			return false
		}
	}
	return true
}

func stringValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func nonEmptyWireValue(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func rewrite(items []json.RawMessage, version string) []json.RawMessage {
	allow := map[string]bool{}
	if version != VersionV0220 {
		allow = map[string]bool{"browser_info": true, "goal_objective": true, "untrusted_context": true, "codex_apps_open_page": true}
	}
	if version == VersionV0224 {
		allow["ide_context"] = true
		allow["untrusted_content"] = true
		allow["proposed_plan"] = true
	}
	changed := make([]json.RawMessage, len(items))
	copy(changed, items)
	for i, raw := range items {
		var x map[string]json.RawMessage
		if json.Unmarshal(raw, &x) != nil {
			continue
		}
		if stringValue(x["type"]) != "function_call_output" || stringValue(x["namespace"]) != "agent" {
			continue
		}
		name := stringValue(x["name"])
		if !allow[name] && (version == VersionV0220 || !selectedPage.MatchString(name)) {
			continue
		}
		if !rewriteCallIDEligible(x["call_id"], hasKey(x, "call_id")) {
			continue
		}
		out, ok := x["output"]
		if !ok {
			continue
		}
		var output string
		if json.Unmarshal(out, &output) != nil {
			continue
		}
		if name == "codex_apps_open_page" && !validPageSnapshot(output) {
			continue
		}
		if selectedPage.MatchString(name) && !validSelectedPage(name, output) {
			continue
		}
		if output == "" && name != "untrusted_content" {
			continue
		}
		if strings.TrimSpace(output) == "" && name != "untrusted_content" && name != "proposed_plan" {
			continue
		}
		text := "<external_" + name + ">" + output + "</external_" + name + ">"
		if name == "codex_apps_open_page" || selectedPage.MatchString(name) {
			text = "<external_" + name + ">\n" + output + "\n</external_" + name + ">"
		}
		changed[i], _ = json.Marshal(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}})
	}
	return changed
}

func hasKey(object map[string]json.RawMessage, key string) bool { _, ok := object[key]; return ok }

func rewriteCallIDEligible(raw json.RawMessage, exists bool) bool {
	if !exists {
		return true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return strings.TrimSpace(value) == ""
}

func validPageSnapshot(output string) bool {
	if !hasUniqueJSONMembers([]byte(output)) {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(output), &object) != nil {
		return false
	}
	id, ok := object["page_id"]
	if !ok {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		return true
	}
	var s string
	return json.Unmarshal(id, &s) == nil && strings.TrimSpace(s) != ""
}
func validSelectedPage(name, output string) bool {
	parts := selectedPage.FindStringSubmatch(name)
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
	header, rest, ok := strings.Cut(output, "\n")
	if !ok || strings.TrimSuffix(header, "\r") != fmt.Sprintf("Companion Page snapshot part %d of %d:", part, total) || strings.TrimSpace(rest) == "" {
		return false
	}
	return total > 1 || validPageSnapshot(rest)
}

func hasUniqueJSONMembers(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if !consumeUniqueJSONValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func consumeUniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		members := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false
			}
			key, ok := keyToken.(string)
			if !ok {
				return false
			}
			if _, exists := members[key]; exists {
				return false
			}
			members[key] = struct{}{}
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
