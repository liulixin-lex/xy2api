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

type AnthropicCompatError struct {
	Status  int
	Code    string
	Message string
}

type anthropicCompatCandidateError struct{ cause error }

func (e *anthropicCompatCandidateError) Error() string { return e.cause.Error() }
func (e *anthropicCompatCandidateError) Unwrap() error { return e.cause }
func IsAnthropicCompatCandidateError(err error) bool {
	var target *anthropicCompatCandidateError
	return errors.As(err, &target)
}
func compatCandidateError(scope *anthropicCompatScope, err error) error {
	if scope != nil && scope.Record != nil {
		return err
	}
	return &anthropicCompatCandidateError{err}
}

func (e *AnthropicCompatError) Error() string { return e.Code + ": " + e.Message }

func compatInvalid(code, message string) error {
	return &AnthropicCompatError{http.StatusBadRequest, code, message}
}
func compatUnavailable(message string) error {
	return &AnthropicCompatError{http.StatusServiceUnavailable, "anthropic_compat_state_unavailable", message}
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
	seen := make(map[anthropicCompatOpaque]struct{})
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
		item := anthropicCompatOpaque{kind, value}
		if _, found := seen[item]; !found {
			seen[item] = struct{}{}
			values = append(values, item)
		}
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
	return nil
}

// PrepareAnthropicCompatibility runs after authentication and before selection.
// The caller supplies only authenticated IDs. Session strings are hashed here and never persisted.
func (s *GatewayService) PrepareAnthropicCompatibility(ctx context.Context, userID, keyID int64, body []byte, entry, explicitSession string, headers ...http.Header) (context.Context, error) {
	values, err := extractAnthropicCompatOpaque(body, entry)
	scope := &anthropicCompatScope{UserID: userID, KeyID: keyID, Entry: entry, Anchor: uuid.NewString(), Signatures: values, HistoryErr: err}
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
					if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.AccountID <= 0 || record.ProfileRevision == "" || record.CredentialRevision == "" || record.Model == "" || record.Prefix == "" || record.Beta == "" || record.Anchor == "" {
						return ctx, compatUnavailable("Signed history provenance is incomplete")
					}
					if scope.Record != nil && *scope.Record != record {
						return ctx, compatInvalid("anthropic_compat_history_incompatible", "Signed history contains different account, profile or prefix constraints")
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
			return ctx, compatInvalid("anthropic_compat_history_incompatible", "Signed history includes an unknown or expired block")
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
	if plan == nil || !plan.Active || (len(values) == 0 && len(plan.Scope.Signatures) == 0) {
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
		return compatUnavailable("Signed response provenance could not be saved")
	}
	return nil
}

func (s *GatewayService) guardAnthropicCompatAccount(ctx context.Context, account *Account) error {
	scope := compatScope(ctx)
	if scope == nil || scope.Record == nil {
		return nil
	}
	if account == nil || account.ID != scope.Record.AccountID {
		return compatUnavailable("The account bound to signed history is unavailable")
	}
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil || profile == nil || profile.Revision != scope.Record.ProfileRevision {
		return compatInvalid("anthropic_compat_history_incompatible", "Signed history requires its original compatibility profile")
	}
	store, _ := s.cache.(AnthropicCompatProvenanceStore)
	if store == nil || len(store.HMACSeed()) < 32 {
		return compatUnavailable("Signed history provenance is unavailable")
	}
	if compatCredentialRevision(store, account) != scope.Record.CredentialRevision {
		return compatInvalid("anthropic_compat_history_incompatible", "Signed history credentials or endpoint have changed")
	}
	return nil
}

func compatCredentialRevision(_ AnthropicCompatProvenanceStore, account *Account) string {
	// Account-owned identity seed is stable across global index root rotation.
	seed, _ := hex.DecodeString(account.AnthropicCompatIdentitySeed())
	return compatDigest(seed, account.Platform, account.Type, account.GetBaseURL(), account.GetCredential("api_key"))
}
