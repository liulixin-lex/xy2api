package resilience

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

var collabNamedPattern = regexp.MustCompile("(?i)\\bfor\\s+item\\s+['\"`]?([a-z0-9]+_[a-z0-9_-]+)\\b")
var collabIndexPattern = regexp.MustCompile(`\binput(?:\[([0-9]+)\]|\.([0-9]+))`)

// ClassifyCollabEvidence does not authorize repairs or change legacy predicates.
// Only structured error fields are evidence: request echoes, output and prose
// that merely mentions an ownership code cannot promote an error.
func ClassifyCollabEvidence(in CollabEvidenceInput) CollabEvidence {
	out := CollabEvidence{Grade: CollabEvidenceGradeUnclaimed, Family: CollabFamilyNone, Ownership: CollabOwnershipClassNone, Source: in.Source}
	root := gjson.ParseBytes(in.Payload)
	if !gjson.ValidBytes(in.Payload) || !root.IsObject() || !collabErrorUnique(root, 0) {
		return out
	}
	object := collabErrorObject(root)
	status, wrapped, valid := collabErrorWrapper(root, in.Event)
	if !valid {
		return out
	}
	if wrapped {
		if inner := root.Get("upstream_body"); inner.Exists() {
			if inner.Type == gjson.String {
				inner = gjson.Parse(inner.Str)
			}
			if !gjson.Valid(inner.Raw) || !inner.IsObject() || !collabErrorUnique(inner, 0) {
				return out
			}
			object = collabErrorObject(inner)
			if !object.IsObject() {
				return out
			}
		}
		out.Inner = &CollabInnerError{Validated: true, Status: status, Code: object.Get("code").String(), Type: object.Get("type").String(), Message: object.Get("message").String(), Payload: []byte(object.Raw)}
	}
	if !object.IsObject() {
		return out
	}
	code := strings.ToLower(strings.TrimSpace(object.Get("code").String()))
	message := strings.ToLower(object.Get("message").String())
	// This relay state is independent of ciphertext and must stay unclaimed.
	if code == "client_tool_continuation_unavailable" || strings.Contains(message, "opaque conversation state") || strings.Contains(message, "unrecognized conversation state") {
		return out
	}
	if strings.HasPrefix(code, "encrypted_agent_recovery_") {
		out.Grade, out.Family = CollabEvidenceGradeVerified, CollabFamilyOwnership
		switch code {
		case "encrypted_agent_recovery_owner_conflict":
			out.Ownership = CollabOwnershipClassSourceConflict
		case "encrypted_agent_recovery_wire_changed", "encrypted_agent_recovery_owner_unavailable":
			out.Ownership = CollabOwnershipClassRecognized
		default:
			out.Ownership = CollabOwnershipClassUnknownSuffix
		}
		if out.Ownership != CollabOwnershipClassUnknownSuffix {
			if in.Shape.ContinuationPointer {
				out.Ownership = CollabOwnershipClassContinuation
			} else if in.HTTPStatus == 429 || in.HTTPStatus == 503 || status == 429 || status == 503 {
				out.Ownership = CollabOwnershipClassCapacity
			}
		}
		return out
	}
	// Policy and context errors cannot be promoted by a quoted decryption phrase.
	if code == "context_length_exceeded" || code == "content_policy_violation" || code == "invalid_prompt" || code == "cyber_policy_violation" {
		return out
	}
	verified := IsEncryptedContentRejection(code, message) || code == "encrypted_content_rejected" ||
		strings.Contains(message, "encrypted content is invalid for this conversation") ||
		strings.Contains(message, "failed to decrypt encrypted content") ||
		strings.Contains(message, "encrypted function arguments could not be decrypted")
	if !verified {
		if wrapped && status == 400 && (in.Shape.AgentCiphertext || in.Shape.ToolOutputCiphertext) {
			out.Grade = CollabEvidenceGradeClaimed
		}
		return out
	}
	out.Grade, out.Family = CollabEvidenceGradeVerified, CollabFamilyOtherEncrypted
	out.Named = collabErrorNamed(object)
	switch {
	case strings.Contains(message, "encrypted function output"):
		out.Family = CollabFamilyToolOutput
	case strings.Contains(message, "encrypted function arguments"), strings.Contains(message, "encrypted_function_args"):
		out.Family = CollabFamilyCallArgs
	case out.Named != nil:
		switch {
		case strings.HasPrefix(out.Named.Identity, "amsg_"):
			out.Family = CollabFamilyAgentMessage
		case strings.HasPrefix(out.Named.Identity, "rs_"):
			out.Family = CollabFamilyReasoning
		case strings.HasPrefix(out.Named.Identity, "cmp_"):
			out.Family = CollabFamilyCompaction
		case strings.HasPrefix(out.Named.Identity, "fc_"):
			out.Family = CollabFamilyCallArgs
		}
	}
	if out.Named != nil {
		out.Named.Family = out.Family
	}
	return out
}

func collabErrorObject(root gjson.Result) gjson.Result {
	if object := root.Get("response.error"); object.IsObject() {
		return object
	}
	if object := root.Get("error"); object.IsObject() {
		return object
	}
	if root.Get("type").String() == "error" || root.Get("type").String() == "proxy.upstream_error" {
		return root
	}
	return gjson.Result{}
}

// Validate the same two wrapper shapes as the legacy stream path. A status
// supplied separately by a caller is never a substitute for validation.
func collabErrorWrapper(root gjson.Result, event string) (int, bool, bool) {
	var number gjson.Result
	switch {
	case root.Get("type").String() == "proxy.upstream_error":
		if event != "error" {
			return 0, false, false
		}
		number = root.Get("status")
	case root.Get("type").String() == "response.failed" && root.Get("response.error.type").String() == "upstream_error":
		number = root.Get("response.error.status_code")
		code := root.Get("response.error.code")
		if code.Type != gjson.String || code.Str != number.Raw {
			return 0, false, false
		}
	default:
		return 0, false, true
	}
	status, err := strconv.Atoi(number.Raw)
	valid := number.Type == gjson.Number && err == nil && status >= 400 && status <= 599
	return status, valid, valid
}

func collabErrorUnique(value gjson.Result, depth int) bool {
	if depth > 32 {
		return false
	}
	valid := true
	seen := map[string]bool{}
	value.ForEach(func(key, child gjson.Result) bool {
		if value.IsObject() {
			if seen[key.String()] {
				valid = false
				return false
			}
			seen[key.String()] = true
		}
		if child.IsObject() || child.IsArray() {
			valid = collabErrorUnique(child, depth+1)
		}
		return valid
	})
	return valid
}

func collabErrorNamed(object gjson.Result) *CollabNamedItem {
	var named *CollabNamedItem
	add := func(id string) {
		if id == "" {
			return
		}
		if named == nil {
			named = &CollabNamedItem{Identity: id}
		} else if named.Identity != id {
			named.Ambiguous = true
		}
	}
	add(object.Get("item_id").String())
	for _, field := range []string{"message", "param"} {
		for _, match := range collabNamedPattern.FindAllStringSubmatch(object.Get(field).String(), -1) {
			add(match[1])
		}
		for _, match := range collabIndexPattern.FindAllStringSubmatch(object.Get(field).String(), -1) {
			index, err := strconv.Atoi(match[1] + match[2])
			if named == nil {
				named = &CollabNamedItem{}
			}
			if err != nil || named.InputIndex != nil && *named.InputIndex != index {
				named.Ambiguous = true
			} else {
				named.InputIndex = &index
			}
		}
	}
	return named
}
