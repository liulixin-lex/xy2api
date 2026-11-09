package service

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
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
	// Reuse the original account-value/assertion scrubber once credentials
	// are resolved; it cannot query a shadow parent again here.
	redacted := string(redactAgentIdentitySensitiveBodyForAccount(ctx, repo, credAccount, body))
	// HTTP ops' shared field sanitizer cannot identify credential values in
	// free text. Remove this account's known secrets before any truncation,
	// independently of Agent Identity mode. Use the same sensitive-key policy
	// as the HTTP ops storage sanitizer and replace longer values first.
	var secrets []string
	if credAccount != nil {
		for key, raw := range credAccount.Credentials {
			if value, ok := raw.(string); ok && isSensitiveKey(key) {
				if value = strings.TrimSpace(value); value != "" {
					secrets = append(secrets, value)
					// Error JSON may escape a quoted cookie or credential value.
					encoded, _ := json.Marshal(value)
					if escaped := string(encoded[1 : len(encoded)-1]); escaped != value {
						secrets = append(secrets, escaped)
					}
				}
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		redacted = strings.ReplaceAll(redacted, secret, "[redacted]")
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
