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
		for key, raw := range credAccount.Credentials {
			identityKey := credAccount.IsOpenAIAgentIdentity() && (key == "agent_runtime_id" || key == "task_id")
			if value, ok := raw.(string); ok && (isSensitiveKey(key) || identityKey) {
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
	for _, secret := range secrets {
		redacted = replaceOpenAITerminalOpsCredentialValue(redacted, secret.text, secret.substring)
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

// Eight characters avoids replacing common short fragments; real opaque
// credentials still receive substring scrubbing, including JSON escapes.
const minimumCredentialSubstringRunes = 8

// replaceOpenAITerminalOpsCredentialValue scrubs short values only at word
// boundaries, preventing a one-character credential from erasing every
// matching letter in operational text.
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
		_, width := utf8.DecodeRuneInString(body[start:])
		_, _ = out.WriteString(body[offset : start+width])
		offset = start + width
	}
	return out.String()
}

func credentialWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}
