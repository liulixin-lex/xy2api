package service

import (
	"context"
	"regexp"
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
	redacted = openAIJSONSensitiveCredentialField.ReplaceAllString(redacted, `${1}"[redacted]"`)
	redacted = openAIAssignmentSensitiveField.ReplaceAllString(redacted, `${1}[redacted]`)
	redacted = openAIBearerCredential.ReplaceAllString(redacted, "Bearer [redacted]")
	redacted = openAIAssertionCredential.ReplaceAllString(redacted, "AgentAssertion [redacted]")
	return []byte(redacted)
}
