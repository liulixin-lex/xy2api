package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// API-key requests identify this application; OAuth continues its existing authenticated protocol.
func applyIQAPIKeyHeaders(h http.Header) { h.Set("User-Agent", "XY2API-IQ-Monitor/1") }

// Each physical API probe (including a persisted retry) gets its own affinity.
// OAuth uses the account's existing convergence policy, never API pool rotation.
// Run after model mapping and header overrides, before dispatch to any transport.
func applyIQProbeRequestIdentity(req *http.Request, account *Account) error {
	apiKey := account.Type == AccountTypeAPIKey
	var ids *codexFingerprintIDs
	if !apiKey {
		ids = resolveCodexFingerprintIDsFromRequest(account, nil)
		if ids == nil {
			return nil
		}
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}
	_ = req.Body.Close()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if apiKey {
		session := uuid.NewString()
		payload["prompt_cache_key"] = session
		for _, name := range []string{
			"session_id", "conversation_id", "session-id", "conversation-id",
			"x-session-id", "x-session-affinity", "x-opencode-session", "x-conversation-id",
			"thread-id", "x-client-request-id", "x-codex-window-id",
			"previous_response_id", "previous-response-id", "x-codex-turn-state", "x-codex-turn-metadata",
		} {
			for key := range req.Header {
				if strings.EqualFold(key, name) {
					delete(req.Header, key)
				}
			}
		}
		for _, name := range []string{"session_id", "conversation_id", "session-id", "x-session-id", "x-session-affinity", "x-conversation-id"} {
			req.Header.Set(name, session)
		}
	} else {
		applyCodexFingerprintHeaders(req.Header, ids)
		applyCodexFingerprintClientMetadata(payload, ids)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(encoded))
	req.ContentLength = int64(len(encoded))
	req.GetBody = nil // Keep the existing one-wire-attempt/no-POST-replay contract.
	return nil
}
