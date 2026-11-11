package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// AnthropicCompatProvenanceStore stores digests and routing constraints only.
// Register is all-or-nothing; existing, different constraints must never be overwritten.
type AnthropicCompatProvenanceStore interface {
	Lookup(context.Context, []string) ([][]byte, error)
	Register(context.Context, []string, []byte, time.Duration) error
	HMACSeed() []byte
	HMACSeeds() [][]byte
}

const anthropicCompatSignatureLimit = 128
const anthropicCompatOpaqueBytesLimit = 1 << 20
const anthropicCompatProvenanceTTL = 24 * time.Hour

// Reasons refine the existing HTTP error codes without exposing stored data.
type AnthropicCompatReason string

const (
	AnthropicCompatReasonUnknownHistory       AnthropicCompatReason = "unknown_or_expired_history"
	AnthropicCompatReasonConstraints          AnthropicCompatReason = "history_constraints_conflict"
	AnthropicCompatReasonInvalidRecord        AnthropicCompatReason = "invalid_provenance_record"
	AnthropicCompatReasonProfile              AnthropicCompatReason = "profile_revision_changed"
	AnthropicCompatReasonCredential           AnthropicCompatReason = "credential_revision_changed"
	AnthropicCompatReasonAccount              AnthropicCompatReason = "bound_account_unavailable"
	AnthropicCompatReasonRegistrationConflict AnthropicCompatReason = "registration_conflict"
	AnthropicCompatReasonRegistrationCapacity AnthropicCompatReason = "registration_capacity"
	AnthropicCompatReasonRegistrationInvalid  AnthropicCompatReason = "registration_invalid"
	AnthropicCompatReasonChatContinuation     AnthropicCompatReason = "chat_continuation_unsupported"
)

// Store errors are comparable with errors.Is, including when wrapped.
type AnthropicCompatRegistrationError string

func (e AnthropicCompatRegistrationError) Error() string { return string(e) }

const (
	AnthropicCompatRegistrationConflict AnthropicCompatRegistrationError = "provenance constraint conflict"
	AnthropicCompatRegistrationCapacity AnthropicCompatRegistrationError = "provenance capacity exceeded"
	AnthropicCompatRegistrationInvalid  AnthropicCompatRegistrationError = "invalid compatibility provenance registration"
)

type AnthropicCompatError struct {
	Status  int
	Code    string
	Message string
}

type anthropicCompatReasonError struct {
	err    *AnthropicCompatError
	reason AnthropicCompatReason
}

func (e *anthropicCompatReasonError) Error() string { return e.err.Error() }
func (e *anthropicCompatReasonError) Unwrap() error { return e.err }

// AnthropicCompatErrorReason preserves the existing error detail/status API.
func AnthropicCompatErrorReason(err error) AnthropicCompatReason {
	var detail *anthropicCompatReasonError
	if errors.As(err, &detail) {
		return detail.reason
	}
	var compat *AnthropicCompatError
	if errors.As(err, &compat) {
		return AnthropicCompatReason(compat.Code)
	}
	return ""
}

type anthropicCompatCandidateError struct{ cause error }

func (e *anthropicCompatCandidateError) Error() string { return e.cause.Error() }
func (e *anthropicCompatCandidateError) Unwrap() error { return e.cause }
func IsAnthropicCompatCandidateError(err error) bool {
	var target *anthropicCompatCandidateError
	return errors.As(err, &target)
}
func compatCandidateError(scope *anthropicCompatScope, err error) error {
	if scope != nil && (scope.Record != nil || len(scope.Signatures) > 0 || scope.HistoryErr != nil) {
		return err
	}
	return &anthropicCompatCandidateError{err}
}

func (e *AnthropicCompatError) Error() string { return e.Code + ": " + e.Message }

func compatInvalid(code, message string, reasons ...AnthropicCompatReason) error {
	err := &AnthropicCompatError{Status: http.StatusBadRequest, Code: code, Message: message}
	if len(reasons) > 0 {
		return &anthropicCompatReasonError{err: err, reason: reasons[0]}
	}
	return err
}
func compatUnavailable(message string, reasons ...AnthropicCompatReason) error {
	err := &AnthropicCompatError{Status: http.StatusServiceUnavailable, Code: "anthropic_compat_state_unavailable", Message: message}
	if len(reasons) > 0 {
		return &anthropicCompatReasonError{err: err, reason: reasons[0]}
	}
	return err
}

func AnthropicCompatErrorDetails(err error) (int, string, string, bool) {
	var target *AnthropicCompatError
	if !errors.As(err, &target) {
		return 0, "", "", false
	}
	return target.Status, target.Code, target.Message, true
}

type anthropicCompatRecord struct {
	Version            int    `json:"v"`
	AccountID          int64  `json:"account"`
	CredentialRevision string `json:"credential"`
	ProfileRevision    string `json:"profile"`
	Model              string `json:"model"`
	Prefix             string `json:"prefix"`
	Beta               string `json:"beta"`
	Anchor             string `json:"anchor"`
}

type anthropicCompatScope struct {
	UserID, KeyID    int64
	Entry            string
	Anchor           string
	Signatures       []anthropicCompatOpaque
	ReadKeys         []string
	Record           *anthropicCompatRecord
	HistoryErr       error
	ChatContinuation bool
	ClientBeta       string
	NativeCompatible bool
}
type anthropicCompatOpaque struct{ Type, Value string }
type anthropicCompatScopeKey struct{}

func compatScope(ctx context.Context) *anthropicCompatScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(anthropicCompatScopeKey{}).(*anthropicCompatScope)
	return scope
}

func compatDigest(seed []byte, parts ...string) string {
	h := hmac.New(sha256.New, seed)
	var n [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func compatOpaqueKey(store AnthropicCompatProvenanceStore, scope *anthropicCompatScope, opaque anthropicCompatOpaque) string {
	return compatOpaqueKeyForSeed(store.HMACSeed(), scope, opaque)
}
func compatOpaqueKeyForSeed(seed []byte, scope *anthropicCompatScope, opaque anthropicCompatOpaque) string {
	return "anthropic_compat:provenance:v1:" + compatDigest(seed, strconv.FormatInt(scope.UserID, 10), strconv.FormatInt(scope.KeyID, 10), opaque.Type, opaque.Value)
}

func extractAnthropicCompatOpaque(body []byte, entry string) ([]anthropicCompatOpaque, error) {
	values := make([]anthropicCompatOpaque, 0)
	var decodeErr error
	add := func(block gjson.Result) bool {
		kind, value := block.Get("type").String(), ""
		switch kind {
		case "thinking":
			value = block.Get("signature").String()
		case "redacted_thinking":
			value = block.Get("data").String()
		}
		if value == "" {
			return true
		}
		// History is a sequence, not a set. Repeated blocks retain their
		// positions and count toward the limits; only storage keys are deduped.
		values = append(values, anthropicCompatOpaque{kind, value})
		return true
	}
	if entry == "responses" {
		gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() != "reasoning" {
				return true
			}
			block, marked, err := apicompat.DecodeAnthropicThinkingEnvelope(item.Get("encrypted_content").String())
			if err != nil {
				if decodeErr == nil {
					decodeErr = compatInvalid("anthropic_compat_history_incompatible", err.Error())
				}
				// Continue looking for recognized history. A malformed extra item
				// cannot hide a known binding later in the request.
				return true
			}
			if !marked {
				return true
			}
			raw, _ := json.Marshal(block)
			return add(gjson.ParseBytes(raw))
		})
	} else {
		gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
			msg.Get("content").ForEach(func(_, block gjson.Result) bool { return add(block) })
			return true
		})
	}
	return values, decodeErr
}

func compatChatRequestHasContinuation(body []byte) bool {
	found := false
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		found = compatChatHasContinuation(msg)
		return !found
	})
	return found
}

// Chat conversion retains tool_call IDs but drops signed thinking envelopes.
// IDs alone are not registered provenance and cannot restore the ordered signed
// blocks. Reject tool/reasoning continuation on adaptation, including legacy
// function calls, rather than infer a session from IDs, prompts or client hints.
func compatChatHasContinuation(msg gjson.Result) bool {
	role := msg.Get("role").String()
	if role == "tool" || role == "function" {
		return true
	}
	if role != "assistant" {
		return false
	}
	if len(msg.Get("tool_calls").Array()) > 0 || msg.Get("function_call").IsObject() || msg.Get("reasoning_content").String() != "" || msg.Get("reasoning").String() != "" {
		return true
	}
	found := false
	msg.Get("content").ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "thinking", "redacted_thinking", "reasoning":
			found = true
		}
		return !found
	})
	return found
}

func compatChatContinuationError() error {
	return compatInvalid("anthropic_compat_request_unsupported", "Chat tool/reasoning continuation cannot preserve signed history; use Messages or Responses with the original signed blocks", AnthropicCompatReasonChatContinuation)
}

func validateAnthropicCompatHistory(scope *anthropicCompatScope) error {
	if scope.HistoryErr != nil {
		return scope.HistoryErr
	}
	totalBytes := 0
	for _, value := range scope.Signatures {
		totalBytes += len(value.Value)
	}
	if len(scope.Signatures) > anthropicCompatSignatureLimit || totalBytes > anthropicCompatOpaqueBytesLimit {
		return compatInvalid("anthropic_compat_history_limit", "Signed history exceeds the gateway provenance limits; history was not truncated")
	}
	if len(scope.Signatures) > 0 && scope.Record == nil {
		return compatInvalid("anthropic_compat_history_incompatible", "Unknown or expired signed history cannot enter compatibility adaptation", AnthropicCompatReasonUnknownHistory)
	}
	return nil
}

func validAnthropicCompatRecord(record anthropicCompatRecord) bool {
	return record.Version == 1 && record.AccountID > 0 && record.ProfileRevision != "" && record.CredentialRevision != "" && record.Model != "" && record.Prefix != "" && record.Beta != "" && record.Anchor != ""
}

// PrepareAnthropicCompatibility runs after authentication and before selection.
// The caller supplies only authenticated IDs. Session strings are hashed here and never persisted.
func (s *GatewayService) PrepareAnthropicCompatibility(ctx context.Context, userID, keyID int64, body []byte, entry, explicitSession string, headers ...http.Header) (context.Context, error) {
	values, err := extractAnthropicCompatOpaque(body, entry)
	scope := &anthropicCompatScope{UserID: userID, KeyID: keyID, Entry: entry, Anchor: uuid.NewString(), Signatures: values, HistoryErr: err}
	if entry == "chat" {
		scope.ChatContinuation = compatChatRequestHasContinuation(body)
	}
	if len(headers) > 0 {
		scope.ClientBeta = headers[0].Get("anthropic-beta")
		scope.NativeCompatible = entry == "messages" && compatCompleteNative(body, headers[0], &claudeCodeV1CompatProfile)
	}
	store, _ := s.cache.(AnthropicCompatProvenanceStore)
	if explicitSession != "" {
		// A client chooses the session identifier, never its tenant scope.
		if store != nil && len(store.HMACSeed()) >= 32 {
			scope.Anchor = compatDigest(store.HMACSeed(), "session", strconv.FormatInt(userID, 10), strconv.FormatInt(keyID, 10), explicitSession)
		}
	}
	if len(values) > 0 {
		if store == nil || len(store.HMACSeed()) < 32 {
			return ctx, compatUnavailable("Signed history provenance is unavailable")
		}
		seeds := store.HMACSeeds()
		if len(seeds) == 0 || len(seeds) > 2 {
			return ctx, compatUnavailable("Invalid provenance keyring")
		}
		for _, seed := range seeds {
			if len(seed) < 32 {
				return ctx, compatUnavailable("Invalid provenance keyring")
			}
		}
		unknown := false
		// Legacy/off requests retain their existing body-size boundary. Resolve
		// every digest in bounded batches before deciding whether adaptation applies.
		for start := 0; start < len(values); start += anthropicCompatSignatureLimit {
			batch := values[start:min(start+anthropicCompatSignatureLimit, len(values))]
			keys := make([]string, 0, len(batch)*len(seeds))
			for _, v := range batch {
				for _, seed := range seeds {
					keys = append(keys, compatOpaqueKeyForSeed(seed, scope, v))
				}
			}
			records, lookupErr := store.Lookup(ctx, keys)
			if lookupErr != nil || len(records) != len(keys) {
				return ctx, compatUnavailable("Signed history provenance lookup failed")
			}
			for i := range batch {
				found := false
				for j := range seeds {
					position := i*len(seeds) + j
					raw := records[position]
					if len(raw) == 0 {
						continue
					}
					found = true
					var record anthropicCompatRecord
					if json.Unmarshal(raw, &record) != nil || !validAnthropicCompatRecord(record) {
						return ctx, compatUnavailable("Signed history provenance is incomplete", AnthropicCompatReasonInvalidRecord)
					}
					if scope.Record != nil && *scope.Record != record {
						return ctx, compatInvalid("anthropic_compat_history_incompatible", "Signed history contains different account, profile or prefix constraints", AnthropicCompatReasonConstraints)
					}
					scope.Record = &record
					scope.ReadKeys = append(scope.ReadKeys, keys[position])
				}
				if !found {
					unknown = true
				}
			}
		}
		if scope.Record != nil && unknown {
			return ctx, compatInvalid("anthropic_compat_history_incompatible", "Signed history includes an unknown or expired block", AnthropicCompatReasonUnknownHistory)
		}
		if scope.Record != nil {
			if scope.HistoryErr != nil {
				return ctx, scope.HistoryErr
			}
			scope.Anchor = scope.Record.Anchor
		}
	}
	return context.WithValue(ctx, anthropicCompatScopeKey{}, scope), nil
}

func (s *GatewayService) registerAnthropicCompat(ctx context.Context, plan *anthropicCompatPlan, values []anthropicCompatOpaque) error {
	if plan == nil || !plan.Active {
		return nil
	}
	if plan.Scope == nil || !validAnthropicCompatRecord(plan.Record) {
		return compatUnavailable("Signed response provenance is incomplete", AnthropicCompatReasonInvalidRecord)
	}
	if bound := plan.Scope.Record; bound != nil && *bound != plan.Record {
		return compatInvalid("anthropic_compat_history_incompatible", "Signed response changed its history constraints", AnthropicCompatReasonConstraints)
	}
	if len(values) == 0 && len(plan.Scope.Signatures) == 0 {
		return nil
	}
	store, _ := s.cache.(AnthropicCompatProvenanceStore)
	if store == nil || len(store.HMACSeed()) < 32 {
		return compatUnavailable("Signed response provenance is unavailable")
	}
	if len(values) > anthropicCompatSignatureLimit {
		return compatUnavailable("Signed response exceeds the gateway provenance limit")
	}
	totalBytes := 0
	for _, value := range values {
		totalBytes += len(value.Value)
		if totalBytes > anthropicCompatOpaqueBytesLimit {
			return compatUnavailable("Signed response exceeds the gateway opaque byte limit")
		}
	}
	keys := append([]string(nil), plan.Scope.ReadKeys...)
	for _, v := range append(append([]anthropicCompatOpaque(nil), plan.Scope.Signatures...), values...) {
		keys = append(keys, compatOpaqueKey(store, plan.Scope, v))
	}
	seen := make(map[string]bool, len(keys))
	unique := keys[:0]
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			unique = append(unique, key)
		}
	}
	keys = unique
	raw, err := json.Marshal(plan.Record)
	if err != nil {
		return compatUnavailable("Signed response provenance encoding failed")
	}
	// This is part of the active upstream attempt, including its drain deadline.
	if err = store.Register(ctx, keys, raw, anthropicCompatProvenanceTTL); err != nil {
		reason := AnthropicCompatReason("anthropic_compat_state_unavailable")
		switch {
		case errors.Is(err, AnthropicCompatRegistrationConflict):
			reason = AnthropicCompatReasonRegistrationConflict
		case errors.Is(err, AnthropicCompatRegistrationCapacity):
			reason = AnthropicCompatReasonRegistrationCapacity
		case errors.Is(err, AnthropicCompatRegistrationInvalid):
			reason = AnthropicCompatReasonRegistrationInvalid
		}
		return compatUnavailable("Signed response provenance could not be saved", reason)
	}
	return nil
}

func (s *GatewayService) guardAnthropicCompatAccount(ctx context.Context, account *Account) error {
	scope := compatScope(ctx)
	if scope == nil || scope.Record == nil {
		return nil
	}
	if account == nil || account.ID != scope.Record.AccountID {
		return compatUnavailable("The account bound to signed history is unavailable", AnthropicCompatReasonAccount)
	}
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil || profile == nil || profile.Revision != scope.Record.ProfileRevision {
		// v1 stores a revision, not an archived wire profile. A transition may
		// resume only with the exact original revision; never alias or rewrite it.
		return compatInvalid("anthropic_compat_history_incompatible", "Signed history requires its original compatibility profile", AnthropicCompatReasonProfile)
	}
	store, _ := s.cache.(AnthropicCompatProvenanceStore)
	if store == nil || len(store.HMACSeed()) < 32 {
		return compatUnavailable("Signed history provenance is unavailable")
	}
	if compatCredentialRevision(store, account) != scope.Record.CredentialRevision {
		return compatInvalid("anthropic_compat_history_incompatible", "Signed history credentials or endpoint have changed", AnthropicCompatReasonCredential)
	}
	return nil
}

func compatCredentialRevision(_ AnthropicCompatProvenanceStore, account *Account) string {
	// Account-owned identity seed is stable across global index root rotation.
	seed, _ := hex.DecodeString(account.AnthropicCompatIdentitySeed())
	return compatDigest(seed, account.Platform, account.Type, account.GetBaseURL(), account.GetCredential("api_key"))
}
