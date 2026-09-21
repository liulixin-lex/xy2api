package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"time"
)

func codexCredentialGeneration(a *Account) [32]byte {
	// Refresh changes publication eligibility, never existing verified tickets.
	values := map[string]string{}
	for _, key := range []string{"access_token", "refresh_token", "chatgpt_account_id", "account_id", "id_token"} {
		values[key] = a.GetCredential(key)
	}
	raw, _ := json.Marshal(values)
	return sha256.Sum256(raw)
}
func codexStateDigest(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}
func rememberCodexRevoked(a *Account, model string, t *openAICodexTicket, now time.Time) {
	rt := codexTicketRuntimes(a)[model]
	if rt.RevokedValues == nil {
		rt.RevokedValues = map[string]time.Time{}
	}
	for hash, expires := range rt.RevokedValues {
		if !expires.After(now) {
			delete(rt.RevokedValues, hash)
		}
	}
	rt.RevokedValues[codexStateDigest(t.State)] = t.ExpiresAt
	saveCodexTicketRuntime(a, model, rt)
}
func codexPreviouslyRevoked(a *Account, model, state string, now time.Time) bool {
	return codexTicketRuntimes(a)[model].RevokedValues[codexStateDigest(state)].After(now)
}
func (s *OpenAIGatewayService) capturePassiveCodexCandidate(receipt codexTicketReceipt, state string) {
	if !s.openAICodexTicketConfig().StandbyEnabled || !validCodexTicketState(state) || sha256.Sum256([]byte(state)) == receipt.stateHash {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	_, _ = s.mutateCodexTicket(ctx, receipt.accountID, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		ac := codexAccountTicketConfigOf(a)
		active := s.lookupOpenAICodexTicket(a, receipt.model)
		if !ac.manages(receipt.model) || !receipt.matches(active) || len(state) != codexTicketTargetLength(ac.TicketPlan) || codexPreviouslyRevoked(a, receipt.model, state, now) {
			return false, nil
		}
		standby := parseOpenAICodexTicketFromAny(a.ID, receipt.model, a.Extra[codexStandbyKey(receipt.model)])
		if standby != nil && standby.State == state {
			return false, nil
		}
		candidate := &openAICodexTicket{AccountID: a.ID, Model: receipt.model, State: state, Length: len(state), CapturedAt: now, ConfigRevision: ac.Revision, FixedProxyFingerprint: codexTicketFixedProxyFingerprint(a), TransportFingerprint: s.codexTicketTransportFingerprint(a)}
		normalizeCodexTicketTimes(candidate, s.openAICodexTicketConfig().TTLSeconds)
		previous := parseOpenAICodexTicketFromAny(a.ID, receipt.model, a.Extra[codexCandidateKey(receipt.model)])
		if !candidate.timeUsable(now) || previous != nil && (previous.State == state || !candidate.ExpiresAt.After(previous.ExpiresAt)) {
			return false, nil
		}
		a.Extra[codexCandidateKey(receipt.model)] = candidate
		rt := codexTicketRuntimes(a)[receipt.model]
		rt.Requested = true
		saveCodexTicketRuntime(a, receipt.model, rt)
		return true, nil
	})
}

const codexPressureKey = "codex_ticket_pressure"

type codexPressureState struct {
	Limit         int       `json:"limit"`
	Last429       time.Time `json:"last_429"`
	CooldownUntil time.Time `json:"cooldown_until"`
	RecoveryAt    time.Time `json:"recovery_at"`
}

func (s *OpenAIGatewayService) codexPressureEnabled(id int64) bool {
	return slices.Contains(s.openAICodexTicketConfig().AdaptiveConcurrencyAccountIDs, id)
}
func codexPressureOf(a *Account) codexPressureState {
	var p codexPressureState
	raw, _ := json.Marshal(a.Extra[codexPressureKey])
	_ = json.Unmarshal(raw, &p)
	if p.Limit < 1 || p.Limit > 5 {
		p.Limit = 5
	}
	return p
}
func (s *OpenAIGatewayService) codexEffectiveConcurrency(ctx context.Context, id int64, configured int) int {
	if !s.codexPressureEnabled(id) || configured <= 0 {
		return configured
	}
	a, err := s.codexTicketAccountByID(ctx, id)
	if err != nil {
		return min(configured, 1)
	}
	return min(configured, codexPressureOf(a).Limit)
}
func (s *OpenAIGatewayService) observeCodexPressure(req *http.Request, resp *http.Response, a *Account) {
	if a == nil || req == nil || resp == nil || !s.codexPressureEnabled(a.ID) {
		return
	}
	if candidate, _ := req.Context().Value(codexTicketCandidateContextKey{}).(bool); candidate {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.mutateCodexTicket(ctx, a.ID, 0, func(live *Account, _ int, now time.Time) (bool, error) {
		p := codexPressureOf(live)
		if resp.StatusCode == 429 {
			if !p.CooldownUntil.After(now) {
				p.Limit = max(1, p.Limit/2)
			}
			p.Last429 = now
			p.RecoveryAt = now
			p.CooldownUntil = codexTicketRetryAfter(resp.Header.Get("Retry-After"), now)
			if p.CooldownUntil.Before(now.Add(5 * time.Minute)) {
				p.CooldownUntil = now.Add(5 * time.Minute)
			}
		} else if resp.StatusCode >= 200 && resp.StatusCode < 300 && p.Limit < 5 && now.Sub(p.Last429) >= 30*time.Minute && now.Sub(p.RecoveryAt) >= 30*time.Minute {
			p.Limit++
			p.RecoveryAt = now
		} else {
			return false, nil
		}
		live.Extra[codexPressureKey] = p
		return true, nil
	})
}

// Digest only the request structure and public client identity; never tokens,
// proxy credentials, prompts, or opaque STATE values.
func codexRequestStructure(body []byte, h http.Header) string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	identity := map[string]any{"fields": keys, "user_agent": h.Get("User-Agent"), "originator": h.Get("Originator"), "version": h.Get("Version"), "client_version": h.Get("Client-Version"), "has_account": h.Get("Chatgpt-Account-Id") != "", "has_session": h.Get("Session_id") != "", "has_state": h.Get(openAICodexTurnStateHeader) != ""}
	raw, _ := json.Marshal(identity)
	return codexStateDigest(string(raw))
}
