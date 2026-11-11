package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

const anthropicCompatPlanKey = "anthropic_compat_attempt_plan"
const anthropicCompatProtectKey = "anthropic_compat_protect_history"

type anthropicCompatProtectedKey struct{}

func compatHistoryProtected(ctx context.Context) bool {
	protected, _ := ctx.Value(anthropicCompatProtectedKey{}).(bool)
	return protected
}

type anthropicCompatPlan struct {
	Active   bool
	Decision string
	Profile  *AnthropicCompatProfile
	Scope    *anthropicCompatScope
	Metadata string
	Beta     string
	Record   anthropicCompatRecord
}

func compatPlan(c *gin.Context) *anthropicCompatPlan {
	if c == nil {
		return nil
	}
	value, _ := c.Get(anthropicCompatPlanKey)
	plan, _ := value.(*anthropicCompatPlan)
	return plan
}

func (s *GatewayService) logAnthropicCompatOutcome(ctx context.Context, c *gin.Context, account *Account, entry string, err error) {
	if account == nil {
		return
	}
	plan, scope := compatPlan(c), compatScope(ctx)
	configured := account.GetExtraString(AnthropicCompatProfileExtraKey)
	if plan == nil && (configured == "" || configured == AnthropicCompatProfileOff) && (scope == nil || len(scope.Signatures) == 0) {
		return
	}
	profile, revision, decision := AnthropicCompatProfileOff, "", "off"
	if plan != nil {
		decision = plan.Decision
		if plan.Profile != nil {
			profile, revision = plan.Profile.ID, plan.Profile.Revision
		}
	} else if configured := account.GetExtraString(AnthropicCompatProfileExtraKey); configured != "" && configured != AnthropicCompatProfileOff {
		profile, decision = configured, "rejected"
	}
	source := "none"
	if scope != nil && scope.Record != nil {
		source = "known"
	} else if scope != nil && len(scope.Signatures) > 0 {
		source = "unknown"
	}
	terminal, errorClass := "complete", "none"
	if err != nil {
		terminal, errorClass = "failed", "upstream_error"
		if _, code, _, local := AnthropicCompatErrorDetails(err); local {
			errorClass = code
		}
		if IsAnthropicCompatCandidateError(err) {
			decision = "candidate_ineligible"
		}
	}
	logger.L().Info("gateway.anthropic_compat_attempt",
		zap.String("profile", profile), zap.String("profile_revision", revision),
		zap.String("entrypoint", entry), zap.String("decision", decision), zap.String("source", source),
		zap.Int64("account_id", account.ID), zap.String("terminal", terminal), zap.String("error_class", errorClass))
}

func compatPreservesHistory(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, _ := c.Get(anthropicCompatProtectKey)
	protected, _ := value.(bool)
	return protected
}

func compatClientHeaders(c *gin.Context) http.Header {
	if c != nil && c.Request != nil {
		return c.Request.Header
	}
	return make(http.Header)
}

func compatCompleteNative(body []byte, headers http.Header, profile *AnthropicCompatProfile) bool {
	if profile == nil || !claudeCliUserAgentRe.MatchString(headers.Get("User-Agent")) || headers.Get("X-App") != "cli" || headers.Get("anthropic-version") != profile.APIVersion {
		return false
	}
	metadata := ParseMetadataUserID(gjson.GetBytes(body, "metadata.user_id").String())
	if metadata == nil || len(metadata.DeviceID) != 64 || !compatValidUUID(metadata.SessionID) || (metadata.AccountUUID != "" && !compatValidUUID(metadata.AccountUUID)) {
		return false
	}
	if device, err := hex.DecodeString(metadata.DeviceID); err != nil || len(device) != 32 {
		return false
	}
	if session := headers.Get("X-Claude-Code-Session-Id"); session != "" && !strings.EqualFold(session, metadata.SessionID) {
		return false
	}
	betas := make(map[string]bool)
	for _, beta := range strings.Split(headers.Get("anthropic-beta"), ",") {
		betas[strings.TrimSpace(beta)] = true
	}
	for _, beta := range profile.RequiredBetas {
		if !betas[beta] {
			return false
		}
	}
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return false
	}
	blocks := system.Array()
	if len(blocks) == 0 {
		return false
	}
	index := 0
	if compatIsBillingHeaderBlock(blocks[0]) {
		if billingVersion := ccVersionInBillingRe.FindString(blocks[0].Get("text").String()); billingVersion != "" && billingVersion != "cc_version="+ExtractCLIVersion(headers.Get("User-Agent")) {
			return false
		}
		index++
	}
	if index >= len(blocks) || blocks[index].Get("type").String() != "text" || blocks[index].Get("text").String() != profile.SystemIdentity {
		return false
	}
	identityCount := 1
	for _, block := range blocks[index+1:] {
		if compatIsBillingHeaderBlock(block) {
			return false
		}
		if block.Get("type").String() == "text" && block.Get("text").String() == profile.SystemIdentity {
			identityCount++
		}
	}
	return identityCount == 1
}

func compatIsBillingHeaderBlock(block gjson.Result) bool {
	return block.Get("type").String() == "text" && strings.HasPrefix(block.Get("text").String(), "x-anthropic-billing-header:")
}

func compatValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && len(value) == 36 && parsed != uuid.Nil && strings.EqualFold(value, parsed.String())
}

// beginAnthropicCompatAttempt clears only per-account derived state. The
// authenticated scope, original input and request budget remain request-owned.
func (s *GatewayService) beginAnthropicCompatAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte, entry string) (context.Context, error) {
	if c != nil {
		c.Set(anthropicCompatPlanKey, (*anthropicCompatPlan)(nil))
		c.Set(anthropicCompatProtectKey, false)
		c.Set(betaPolicyFilterSetKey, nil)
		c.Set(toolNameRewriteKey, (*ToolNameRewrite)(nil))
	}
	if err := s.guardAnthropicCompatAccount(ctx, account); err != nil {
		return ctx, err
	}
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil {
		return ctx, compatCandidateError(compatScope(ctx), compatInvalid("anthropic_compat_configuration_conflict", err.Error()))
	}
	if profile == nil {
		return ctx, nil
	}
	scope := compatScope(ctx)
	if scope == nil {
		ctx, err = s.PrepareAnthropicCompatibility(ctx, 0, 0, body, entry, "")
		if err != nil {
			return ctx, err
		}
		scope = compatScope(ctx)
	}
	protect := scope.Record != nil || entry != "messages" || !compatCompleteNative(body, compatClientHeaders(c), profile)
	if protect {
		if err := validateAnthropicCompatHistory(scope); err != nil {
			return ctx, err
		}
		ctx = context.WithValue(ctx, anthropicCompatProtectedKey{}, true)
		if c != nil {
			c.Set(anthropicCompatProtectKey, true)
		}
	}
	return ctx, nil
}

func compatIdentity(profile *AnthropicCompatProfile, account *Account, scope *anthropicCompatScope) (string, error) {
	seed, err := hex.DecodeString(account.AnthropicCompatIdentitySeed())
	if err != nil || len(seed) != 32 {
		return "", compatUnavailable("Account compatibility identity has not been initialized")
	}
	user, key := strconv.FormatInt(scope.UserID, 10), strconv.FormatInt(scope.KeyID, 10)
	device := compatDigest(seed, "device", user, key)
	accountID := compatUUID(compatDigest(seed, "account", user, key, strconv.FormatInt(account.ID, 10)))
	session := compatUUID(compatDigest(seed, "session", user, key, strconv.FormatInt(account.ID, 10), scope.Anchor))
	return "user_" + device + "_account_" + accountID + "_session_" + session, nil
}

func compatUUID(digest string) string {
	b, _ := hex.DecodeString(digest[:32])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	id, _ := uuid.FromBytes(b)
	return id.String()
}

func compatInjectBody(body []byte, profile *AnthropicCompatProfile, metadata string) ([]byte, error) {
	system := gjson.GetBytes(body, "system")
	identity, _ := json.Marshal(map[string]string{"type": "text", "text": profile.SystemIdentity})
	var blocks []json.RawMessage
	identityCount := 0
	identityPosition := -1
	if system.Exists() && system.Type != gjson.Null {
		if system.Type == gjson.String {
			if system.String() == profile.SystemIdentity {
				identityCount = 1
				identityPosition = 0
			}
			block, _ := json.Marshal(map[string]string{"type": "text", "text": system.String()})
			blocks = append(blocks, block)
		} else if system.IsArray() {
			for i, block := range system.Array() {
				if block.Get("type").String() == "text" && block.Get("text").String() == profile.SystemIdentity {
					identityCount++
					identityPosition = i
				}
				blocks = append(blocks, json.RawMessage(block.Raw))
			}
		} else {
			return nil, compatInvalid("anthropic_compat_request_unsupported", "system must be text or a block array")
		}
	}
	if identityCount > 1 {
		return nil, compatInvalid("anthropic_compat_history_incompatible", "Existing Claude Code identity must occur exactly once at the start of system")
	}
	if identityPosition > 0 {
		managedIdentity := blocks[identityPosition]
		blocks = append(blocks[:identityPosition], blocks[identityPosition+1:]...)
		blocks = append([]json.RawMessage{managedIdentity}, blocks...)
	} else if identityCount == 0 {
		blocks = append([]json.RawMessage{identity}, blocks...)
	}
	raw, _ := json.Marshal(blocks)
	result, err := sjson.SetRawBytes(body, "system", raw)
	if err != nil {
		return nil, err
	}
	if metadataObject := gjson.GetBytes(result, "metadata"); metadataObject.Exists() && metadataObject.Type != gjson.Null && !metadataObject.IsObject() {
		return nil, compatInvalid("anthropic_compat_request_unsupported", "metadata must be an object")
	}
	return sjson.SetBytes(result, "metadata.user_id", metadata)
}

func compatUnsupportedRequest(body []byte, entry string) error {
	compact := false
	gjson.GetBytes(body, "context_management.edits").ForEach(func(_, edit gjson.Result) bool {
		compact = strings.HasPrefix(edit.Get("type").String(), "compact_")
		return !compact
	})
	for _, collection := range []string{"messages", "input"} {
		gjson.GetBytes(body, collection).ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "compaction" {
				compact = true
			}
			item.Get("content").ForEach(func(_, block gjson.Result) bool {
				compact = compact || block.Get("type").String() == "compaction"
				return !compact
			})
			return !compact
		})
	}
	if compact {
		return compatInvalid("anthropic_compat_request_unsupported", "Compatibility profile does not support provider compaction state")
	}
	if entry == "responses" {
		var capabilityErr error
		gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
			opaque := item.Get("encrypted_content").String()
			if opaque != "" && !strings.HasPrefix(opaque, "anthropic-thinking-v1:") {
				capabilityErr = compatInvalid("anthropic_compat_request_unsupported", "Unrecognized provider encrypted_content cannot be converted by this profile")
				return false
			}
			return true
		})
		if capabilityErr != nil {
			return capabilityErr
		}
	}
	for _, field := range []string{"previous_response_id", "conversation", "thread_id", "thread", "continuation", "compact", "compaction", "context_management.compact"} {
		value := gjson.GetBytes(body, field)
		if value.Exists() && value.Type != gjson.Null && value.Raw != "\"\"" {
			return compatInvalid("anthropic_compat_request_unsupported", "Compatibility profile does not support provider stored continuation or compaction state")
		}
	}
	if entry == "chat" {
		return nil
	}
	return nil
}

func (s *GatewayService) newAnthropicCompatPlan(ctx context.Context, c *gin.Context, account *Account, body []byte, model string) (*anthropicCompatPlan, error) {
	if existing := compatPlan(c); existing != nil {
		return existing, nil
	}
	if err := s.guardAnthropicCompatAccount(ctx, account); err != nil {
		return nil, err
	}
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil {
		return nil, compatCandidateError(compatScope(ctx), compatInvalid("anthropic_compat_configuration_conflict", err.Error()))
	}
	if profile == nil {
		return nil, nil
	}
	scope := compatScope(ctx)
	if scope == nil {
		return nil, compatUnavailable("Authenticated compatibility request scope is unavailable")
	}
	if scope.ChatContinuation {
		return nil, compatCandidateError(scope, compatChatContinuationError())
	}
	if scope.Record == nil && scope.Entry == "messages" && compatCompleteNative(body, compatClientHeaders(c), profile) {
		plan := &anthropicCompatPlan{Decision: "already_compatible", Profile: profile, Scope: scope}
		if c != nil {
			c.Set(anthropicCompatPlanKey, plan)
		}
		return plan, nil
	}
	if err := validateAnthropicCompatHistory(scope); err != nil {
		return nil, err
	}
	if !profile.SupportsModel(model) {
		return nil, compatCandidateError(scope, compatInvalid("anthropic_compat_model_unsupported", "Model is outside the compatibility profile capability range"))
	}
	if err := compatUnsupportedRequest(body, scope.Entry); err != nil {
		return nil, err
	}
	if len(scope.Signatures) > 0 && scope.Record == nil {
		return nil, compatInvalid("anthropic_compat_history_incompatible", "Unknown or expired signed history cannot enter compatibility adaptation")
	}
	store, _ := s.cache.(AnthropicCompatProvenanceStore)
	if store == nil || len(store.HMACSeed()) < 32 {
		return nil, compatUnavailable("Compatibility provenance store is unavailable")
	}
	policy := s.evaluateBetaPolicy(ctx, strings.Join(profile.RequiredBetas, ",")+","+compatClientHeaders(c).Get("anthropic-beta"), account, model)
	if policy.blockErr != nil {
		return nil, compatCandidateError(scope, compatInvalid("anthropic_compat_configuration_conflict", "Beta policy blocks this compatibility request"))
	}
	if c != nil {
		filters := policy.filterSet
		if filters == nil {
			filters = map[string]struct{}{}
		}
		c.Set(betaPolicyFilterSetKey, filters)
	}
	metadata, err := compatIdentity(profile, account, scope)
	if err != nil {
		return nil, err
	}
	plan := &anthropicCompatPlan{Active: true, Decision: "adapted", Profile: profile, Scope: scope, Metadata: metadata, Record: anthropicCompatRecord{Version: 1, AccountID: account.ID, CredentialRevision: compatCredentialRevision(store, account), ProfileRevision: profile.Revision, Model: model, Anchor: scope.Anchor}}
	if c != nil {
		c.Set(anthropicCompatPlanKey, plan)
	}
	return plan, nil
}

func compatResolveBeta(plan *anthropicCompatPlan, client, computed string, drop map[string]struct{}) (string, error) {
	if plan == nil || !plan.Active {
		return computed, nil
	}
	seen := make(map[string]bool)
	tokens := make([]string, 0)
	for _, list := range []string{strings.Join(plan.Profile.RequiredBetas, ","), client, computed} {
		for _, raw := range strings.Split(list, ",") {
			token := strings.TrimSpace(raw)
			if token == "" || seen[token] {
				continue
			}
			if _, denied := drop[token]; denied {
				return "", compatCandidateError(plan.Scope, compatInvalid("anthropic_compat_configuration_conflict", "Beta policy blocks a required compatibility or client capability: "+token))
			}
			seen[token] = true
			tokens = append(tokens, token)
		}
	}
	return strings.Join(tokens, ","), nil
}

func compatPrefix(body []byte) string {
	h := sha256.New()
	for _, field := range []string{"system", "tools"} {
		raw := gjson.GetBytes(body, field).Raw
		// Clients may serialize the same object with different whitespace/key
		// order on the next turn. Bind semantic system/tools, preserving arrays,
		// all fields and number values; messages are never decoded here.
		var value any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&value) == nil {
			if canonical, err := json.Marshal(value); err == nil {
				raw = string(canonical)
			}
		}
		_, _ = fmt.Fprintf(h, "%d:", len(raw))
		_, _ = h.Write([]byte(raw))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func finalizeAnthropicCompatPlan(plan *anthropicCompatPlan, body []byte, beta string) error {
	if plan == nil || !plan.Active {
		return nil
	}
	plan.Beta = beta
	plan.Record.Prefix = compatPrefix(body)
	sum := sha256.Sum256([]byte(beta))
	plan.Record.Beta = hex.EncodeToString(sum[:])
	if bound := plan.Scope.Record; bound != nil && *bound != plan.Record {
		return compatInvalid("anthropic_compat_history_incompatible", "Signed history requires the original model, system/tools, beta capabilities and identity")
	}
	return nil
}

func applyAnthropicCompatHeaders(headers http.Header, plan *anthropicCompatPlan) {
	if plan == nil || !plan.Active {
		return
	}
	for name, value := range map[string]string{"User-Agent": plan.Profile.UserAgent, "X-App": "cli", "anthropic-version": plan.Profile.APIVersion, "anthropic-beta": plan.Beta} {
		deleteHeaderAllForms(headers, name)
		setHeaderRaw(headers, name, value)
	}
	if getHeaderRaw(headers, "X-Claude-Code-Session-Id") != "" {
		deleteHeaderAllForms(headers, "X-Claude-Code-Session-Id")
		setHeaderRaw(headers, "X-Claude-Code-Session-Id", ParseMetadataUserID(plan.Metadata).SessionID)
	}
}

// BuildAnthropicCompatDiagnosticRequest shares the production profile, identity,
// body and header functions. Diagnostics use isolated request identities and no history.
func BuildAnthropicCompatDiagnosticRequest(account *Account, body []byte, headers http.Header) ([]byte, http.Header, error) {
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil {
		return nil, nil, err
	}
	out := headers.Clone()
	if profile == nil {
		return body, out, nil
	}
	if !profile.SupportsModel(gjson.GetBytes(body, "model").String()) {
		return nil, nil, compatInvalid("anthropic_compat_model_unsupported", "Diagnostic model is outside the profile capability range")
	}
	values, err := extractAnthropicCompatOpaque(body, "messages")
	if err != nil {
		return nil, nil, err
	}
	if len(values) > 0 {
		return nil, nil, compatInvalid("anthropic_compat_history_incompatible", "Diagnostics must use a fresh conversation")
	}
	scope := &anthropicCompatScope{Entry: "diagnostic", Anchor: uuid.NewString()}
	metadata, err := compatIdentity(profile, account, scope)
	if err != nil {
		return nil, nil, err
	}
	plan := &anthropicCompatPlan{Active: true, Profile: profile, Metadata: metadata}
	plan.Beta, err = compatResolveBeta(plan, headers.Get("anthropic-beta"), "", nil)
	if err != nil {
		return nil, nil, err
	}
	body, err = compatInjectBody(body, profile, metadata)
	if err != nil {
		return nil, nil, err
	}
	applyAnthropicCompatHeaders(out, plan)
	return body, out, nil
}

func writeAnthropicCompatError(c *gin.Context, entry string, err error) {
	status, code, message, local := AnthropicCompatErrorDetails(err)
	if c == nil || c.Writer.Written() || IsAnthropicCompatCandidateError(err) {
		return
	}
	if !local {
		var conversionErr *apicompat.UnsupportedConversionError
		if !errors.As(err, &conversionErr) {
			return
		}
		status, code, message = http.StatusBadRequest, "invalid_request_error", conversionErr.Error()
	}
	switch entry {
	case "responses":
		writeResponsesError(c, status, code, message)
	case "chat":
		writeGatewayCCError(c, status, code, message)
	default:
		c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": code, "message": message}})
	}
}

func projectAnthropicCompatCountBody(body []byte) []byte {
	// This endpoint has an input schema: do not forward metadata or generation options.
	allowed := map[string]bool{"model": true, "messages": true, "system": true, "tools": true, "tool_choice": true, "thinking": true}
	result := body
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		if !allowed[key.String()] {
			if next, err := sjson.DeleteBytes(result, key.String()); err == nil {
				result = next
			}
		}
		return true
	})
	return result
}
