package service

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	openAIJSONSensitiveCredentialField = regexp.MustCompile(`(?i)(["']?(?:agent_private_key|agent_runtime_id|task_id|access_token|refresh_token|id_token|api_key|session_key|cookie|authorization|token|secret)["']?\s*:\s*)"(?:\\.|[^"\\])*"`)
	openAIAssignmentSensitiveField     = regexp.MustCompile(`(?i)(\b(?:agent_private_key|agent_runtime_id|task_id|access_token|refresh_token|id_token|api_key|session_key|cookie|authorization|token|secret)\b\s*[:=]\s*)[^\s"',;}\]]+`)
	openAIAssertionCredential          = regexp.MustCompile(`(?i)\bAgentAssertion\s+[^\s"',}]+`)
	openAIBearerCredential             = regexp.MustCompile(`(?i)\bBearer\s+[^\s"',}]+`)
)

// redactOpenAITerminalOpsSensitiveBody only prepares copies for terminal SSE
// ops persistence. Classification and client responses keep their original
// inputs. If a shadow credential lookup fails, no raw evidence is persisted.
func redactOpenAITerminalOpsSensitiveBody(ctx context.Context, repo AccountRepository, account *Account, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	credAccount := account
	if account != nil && account.IsShadow() {
		// Unknown parent credentials may appear anywhere in an echoed error.
		// Discard the evidence instead of allowing a repository outage to
		// turn this persistence boundary into a fail-open path.
		if repo == nil {
			return []byte("[redacted: credentials unavailable]")
		}
		resolved, err := resolveCredentialAccount(ctx, repo, account)
		if err != nil || resolved == nil {
			return []byte("[redacted: credentials unavailable]")
		}
		credAccount = resolved
	}
	redacted := string(body)
	// HTTP ops' shared field sanitizer cannot identify credential values in
	// free text. Remove this account's known secrets before any truncation,
	// independently of Agent Identity mode. Use the same sensitive-key policy
	// as the HTTP ops storage sanitizer and replace longer values first.
	type credentialValue struct {
		text      string
		substring bool
	}
	var secrets []credentialValue
	if credAccount != nil {
		identity := credAccount.IsOpenAIAgentIdentity()
		for key, raw := range credAccount.Credentials {
			identityKey := identity && (key == "agent_runtime_id" || key == "task_id")
			value, ok := raw.(string)
			if identity {
				// Preserve the identity scrubber's legacy numeric-value handling.
				value, ok = credAccount.GetCredential(key), true
			}
			if ok && (isSensitiveKey(key) || identityKey) {
				if value = strings.TrimSpace(value); value != "" {
					substring := utf8.RuneCountInString(value) >= minimumCredentialSubstringRunes
					secrets = append(secrets, credentialValue{value, substring})
					// Error JSON may escape a quoted cookie or credential value.
					encoded, _ := json.Marshal(value)
					if escaped := string(encoded[1 : len(encoded)-1]); escaped != value {
						secrets = append(secrets, credentialValue{escaped, substring})
					}
				}
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i].text) > len(secrets[j].text) })
	redactText := func(text string) string {
		for _, secret := range secrets {
			text = replaceOpenAITerminalOpsCredentialValue(text, secret.text, secret.substring)
		}
		return text
	}
	if json.Valid(body) {
		// Boundaries belong to decoded strings: a serialized \n ends in a
		// letter, while its decoded newline is a credential boundary.
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return []byte("[redacted: invalid payload]")
		}
		encoded, err := json.Marshal(redactOpenAITerminalOpsJSON(value, redactText))
		if err != nil {
			return []byte("[redacted: invalid payload]")
		}
		redacted = string(encoded)
	} else {
		redacted = redactText(redacted)
	}
	redacted = openAIJSONSensitiveCredentialField.ReplaceAllString(redacted, `${1}"[redacted]"`)
	redacted = openAIAssignmentSensitiveField.ReplaceAllString(redacted, `${1}[redacted]`)
	redacted = openAIBearerCredential.ReplaceAllString(redacted, "Bearer [redacted]")
	redacted = openAIAssertionCredential.ReplaceAllString(redacted, "AgentAssertion [redacted]")
	// This is the same sanitizer used by HTTP error ops detail/body storage.
	// Keep it at the persistence-only boundary, after strict credential and
	// assertion handling and before the caller's configured detail limit.
	redacted, _ = sanitizeErrorBodyForStorage(redacted, OpsErrorLogQueueBodyMaxBytes)
	return []byte(redacted)
}

// Only longer credentials receive arbitrary substring replacement. If a short
// value remains inside another word, hide that text instead of leaking it.
const minimumCredentialSubstringRunes = 8

// replaceOpenAITerminalOpsCredentialValue operates on decoded text. Complete
// short values are scrubbed at word boundaries; ambiguous embedded values hide
// the entire text, so the length floor cannot weaken credential protection.
func replaceOpenAITerminalOpsCredentialValue(body, credential string, substring bool) string {
	if substring {
		return strings.ReplaceAll(body, credential, "[redacted]")
	}
	var out strings.Builder
	for offset := 0; offset < len(body); {
		relative := strings.Index(body[offset:], credential)
		if relative < 0 {
			_, _ = out.WriteString(body[offset:])
			break
		}
		start := offset + relative
		end := start + len(credential)
		beforeOK := start == 0
		if !beforeOK {
			before, _ := utf8.DecodeLastRuneInString(body[:start])
			beforeOK = !credentialWordRune(before)
		}
		afterOK := end == len(body)
		if !afterOK {
			after, _ := utf8.DecodeRuneInString(body[end:])
			afterOK = !credentialWordRune(after)
		}
		if beforeOK && afterOK {
			_, _ = out.WriteString(body[offset:start])
			_, _ = out.WriteString("[redacted]")
			offset = end
			continue
		}
		return "[redacted]"
	}
	return out.String()
}

func credentialWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}

// Clean every JSON value before storage, including nested diagnostic fields.
// Recognize sensitive fields by their original keys. Hide credential-bearing
// objects instead of renaming keys into possible collisions.
func redactOpenAITerminalOpsJSON(value any, redactText func(string) string) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			if redactText(key) != key {
				return "[redacted]"
			}
			if isSensitiveKey(key) || strings.EqualFold(key, "agent_runtime_id") || strings.EqualFold(key, "task_id") {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactOpenAITerminalOpsJSON(item, redactText)
			}
		}
		return out
	case []any:
		for i, item := range value {
			value[i] = redactOpenAITerminalOpsJSON(item, redactText)
		}
		return value
	case string:
		return redactText(value)
	default:
		// Preserve exact JSON numbers unless they themselves echo credentials.
		encoded, err := json.Marshal(value)
		if err != nil {
			return "[redacted]"
		}
		if text := redactText(string(encoded)); text != string(encoded) {
			return text
		}
		return value
	}
}
