package resilience

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

var collabNamedPattern = regexp.MustCompile(`(?i)\bfor\s+item\s+`)
var collabLocatorPattern = regexp.MustCompile(`(?i)\binput(?:[.\[]|\s+[.\[])`)
var collabAtPattern = regexp.MustCompile(`\bat\s+`)

// Text evidence must assert rejection of the encrypted subject itself. Locator
// quotes are allowed; quoted examples and unrelated later clauses are not.
var collabRejectionPattern = regexp.MustCompile(`(?i)^(?:(?:the )?encrypted (?:content|function arguments|function output(?: content)?)(?: (?:for item ['"` + "`" + `]?[a-z0-9]+_[a-z0-9_-]+['"` + "`" + `]?|at input(?:\[[0-9]+\]|\.[0-9]+)(?:\.[a-z_0-9]+)*))? (?:could not be (?:decrypted|decoded|verified)|hydration failed|is invalid for this conversation)|failed to decrypt encrypted content)(?:$|[ .,;:])`)

// ClassifyCollabEvidence does not authorize repairs or change legacy predicates.
// Only structured error fields are evidence: request echoes, output and prose
// that merely mentions an ownership code cannot promote an error.
func ClassifyCollabEvidence(in CollabEvidenceInput) CollabEvidence {
	out := CollabEvidence{Grade: CollabEvidenceGradeUnclaimed, Family: CollabFamilyNone, Ownership: CollabOwnershipClassNone, Source: in.Source}
	root := gjson.ParseBytes(in.Payload)
	if !gjson.ValidBytes(in.Payload) || !root.IsObject() || !collabErrorUnique(root, 0) {
		return out
	}
	var object gjson.Result
	status, wrapped := 0, false
	for depth := 0; ; depth++ {
		if depth > 8 {
			return out
		}
		currentStatus, currentWrapped, valid := collabErrorWrapper(root, in.Event)
		if !valid || root.Get("response.error").Exists() && root.Get("error").Exists() {
			return out
		}
		object = collabErrorObject(root)
		if !currentWrapped {
			break
		}
		status, wrapped = currentStatus, true
		inner := root.Get("upstream_body")
		if !inner.Exists() {
			break
		}
		if inner.Type == gjson.String {
			inner = gjson.Parse(inner.Str)
		}
		if !gjson.Valid(inner.Raw) || !inner.IsObject() || !collabErrorUnique(inner, 0) {
			return out
		}
		root = inner
	}
	if wrapped {
		out.Inner = &CollabInnerError{Validated: true, Status: status, Code: object.Get("code").String(), Type: object.Get("type").String(), Message: object.Get("message").String(), Payload: []byte(object.Raw)}
	}
	if !object.IsObject() {
		return out
	}
	code := strings.ToLower(strings.TrimSpace(object.Get("code").String()))
	message := strings.ToLower(object.Get("message").String())
	family := CollabFamilyOtherEncrypted
	out.Named = collabErrorNamed(object)
	switch {
	case strings.Contains(message, "encrypted function output"):
		family = CollabFamilyToolOutput
	case strings.Contains(message, "encrypted function arguments"), strings.Contains(message, "encrypted_function_args"):
		family = CollabFamilyCallArgs
	case out.Named != nil:
		switch {
		case strings.HasPrefix(out.Named.Identity, "amsg_"):
			family = CollabFamilyAgentMessage
		case strings.HasPrefix(out.Named.Identity, "rs_"):
			family = CollabFamilyReasoning
		case strings.HasPrefix(out.Named.Identity, "cmp_"):
			family = CollabFamilyCompaction
		case strings.HasPrefix(out.Named.Identity, "fco_"):
			family = CollabFamilyToolOutput
		case strings.HasPrefix(out.Named.Identity, "fc_"):
			family = CollabFamilyCallArgs
		}
	}
	if out.Named != nil {
		out.Named.Family = family
	}
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
	if contentPolicyCode(code, strings.ToLower(object.Get("type").String()), strings.ToLower(object.Raw)) || contentPolicyText(400, message, strings.ToLower(object.Raw)) ||
		code == "context_length_exceeded" || code == "context_window_exceeded" || code == "invalid_prompt" || code == "cyber_policy_violation" || strings.Contains(message, "maximum context length") {
		return out
	}
	verified := IsEncryptedContentRejection(code, "") || code == "encrypted_content_rejected"
	verified = verified || collabRejectionPattern.MatchString(strings.TrimSpace(message))
	if !verified {
		if wrapped && status == 400 && (in.Shape.AgentCiphertext || in.Shape.ToolOutputCiphertext) {
			out.Grade = CollabEvidenceGradeClaimed
		}
		return out
	}
	out.Grade, out.Family = CollabEvidenceGradeVerified, family
	return out
}

func collabErrorObject(root gjson.Result) gjson.Result {
	if object := root.Get("response.error"); object.IsObject() {
		if root.Get("error").Exists() {
			return gjson.Result{}
		}
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
		value := object.Get(field).String()
		for _, match := range collabNamedPattern.FindAllStringIndex(value, -1) {
			token, valid := collabReferenceToken(value[match[1]:])
			add(token)
			if !valid || token == "" {
				if named == nil {
					named = &CollabNamedItem{}
				}
				named.Ambiguous = true
			}
		}
		var locators []string
		if field == "param" && value != "" {
			locators = []string{value}
		} else if field == "message" {
			starts := map[int]bool{}
			for _, match := range collabLocatorPattern.FindAllStringIndex(value, -1) {
				start := match[0]
				// Keep the entire token, including qualifiers such as body/.
				for start > 0 && !strings.ContainsRune(" \t\r\n", rune(value[start-1])) {
					start--
				}
				starts[start] = true
			}
			for _, match := range collabAtPattern.FindAllStringIndex(value, -1) {
				starts[match[1]] = true
			}
			for start := range starts {
				token, valid := collabReferenceToken(value[start:])
				if !valid {
					token = "!" + token
				}
				locators = append(locators, token)
			}
		}
		if len(locators) > 1 {
			if named == nil {
				named = &CollabNamedItem{}
			}
			named.Ambiguous = true
		}
		for _, locator := range locators {
			if named == nil {
				named = &CollabNamedItem{}
			}
			named.Locators = append(named.Locators, locator)
			if named.Path == "" {
				named.Path = locator
			}
		}
	}
	return named
}

// Consume a whole reference, including matching quotes. Punctuation inside a
// quoted ID is part of its identity; it must never turn into a valid prefix.
func collabReferenceToken(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	if strings.ContainsRune("'\"`", rune(value[0])) {
		end := strings.IndexByte(value[1:], value[0])
		if end < 0 {
			return value, false
		}
		end++
		tail := value[end+1:]
		if len(tail) > 0 && strings.ContainsRune(" \t\r\n", rune(tail[0])) && !collabReferenceTail(tail) {
			return value, false
		}
		if tail != "" && !strings.ContainsRune(" \t\r\n", rune(tail[0])) {
			if !strings.ContainsRune(".,;:!?", rune(tail[0])) || len(tail) > 1 && !strings.ContainsRune(" \t\r\n", rune(tail[1])) {
				return value, false
			}
		}
		return value[1:end], true
	}
	end := strings.IndexAny(value, " \t\r\n")
	if end < 0 {
		end = len(value)
	}
	token := value[:end]
	if end < len(value) && !collabReferenceTail(value[end:]) {
		return value, false
	}
	if strings.ContainsAny(token, "'\"`") {
		return token, false
	}
	// A single sentence terminator belongs to the surrounding prose.
	if len(token) > 0 && strings.ContainsRune(".,;:!?", rune(token[len(token)-1])) {
		if end != len(value) {
			return token, false
		}
		token = token[:len(token)-1]
	}
	return token, token != ""
}

// Only the upstream's rejection clause may follow a reference. An arbitrary
// suffix cannot turn into a shorter valid locator at the first whitespace.
func collabReferenceTail(tail string) bool {
	for _, clause := range []string{" could not be decrypted", " could not be decoded", " could not be verified", " hydration failed", " is invalid for this conversation"} {
		if tail == clause || tail == clause+"." {
			return true
		}
	}
	return false
}
