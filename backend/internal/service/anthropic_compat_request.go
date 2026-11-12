package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type anthropicCompatRequestKey struct{}
type anthropicCompatDispatchModelKey struct{}
type anthropicCompatSessionKey struct{}
type anthropicCompatStickyKey struct{}

// Request-local identity inputs contain no signed history or persisted state.
type anthropicCompatRequest struct {
	UserID, KeyID                                             int64
	Entry, Anchor, RandomAnchor, ClientBeta, Model, RequestID string
	logged                                                    sync.Map
}

func WithAnthropicCompatRequest(ctx context.Context, userID, keyID int64, body []byte, entry, explicitSession string, headers http.Header, requestID string) context.Context {
	anchor := strings.TrimSpace(explicitSession)
	if anchor == "" {
		anchor = compatMetadataSession(gjson.GetBytes(body, "metadata.user_id").String())
	}
	request := &anthropicCompatRequest{UserID: userID, KeyID: keyID, Entry: entry, RequestID: requestID, Anchor: anchor, RandomAnchor: uuid.NewString(), ClientBeta: headers.Get("anthropic-beta"), Model: gjson.GetBytes(body, "model").String()}
	return context.WithValue(ctx, anthropicCompatRequestKey{}, request)
}

func compatMetadataSession(raw string) string {
	var value struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(raw), &value) == nil {
		return strings.TrimSpace(value.SessionID)
	}
	if i := strings.LastIndex(raw, "_session_"); i >= 0 {
		session := raw[i+len("_session_"):]
		if _, err := uuid.Parse(session); err == nil {
			return session
		}
	}
	return ""
}

func WithAnthropicCompatSessionHash(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, anthropicCompatSessionKey{}, hash)
}

func WithAnthropicCompatDispatchModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, anthropicCompatDispatchModelKey{}, model)
}

func anthropicCompatDispatchModel(ctx context.Context, model string) string {
	if value, ok := ctx.Value(anthropicCompatDispatchModelKey{}).(string); ok && value != "" {
		return value
	}
	return model
}

func compatRequest(ctx context.Context) *anthropicCompatRequest {
	value, _ := ctx.Value(anthropicCompatRequestKey{}).(*anthropicCompatRequest)
	return value
}

func compatSessionAnchor(ctx context.Context, request *anthropicCompatRequest) string {
	if request.Anchor != "" {
		return request.Anchor
	}
	if hash, _ := ctx.Value(anthropicCompatSessionKey{}).(string); hash != "" {
		return hash
	}
	return request.RandomAnchor
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

func compatUUID(digest string) string {
	b, _ := hex.DecodeString(digest[:32])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	id, _ := uuid.FromBytes(b)
	return id.String()
}

func compatIdentity(account *Account, userID, keyID int64, anchor string) (string, error) {
	seed, err := hex.DecodeString(account.AnthropicCompatIdentitySeed())
	if err != nil || len(seed) != 32 {
		return "", ErrNoAvailableAccounts
	}
	user, key := strconv.FormatInt(userID, 10), strconv.FormatInt(keyID, 10)
	id := strconv.FormatInt(account.ID, 10)
	device := compatDigest(seed, "device", user, key)
	accountID := compatUUID(compatDigest(seed, "account", user, key, id))
	session := compatUUID(compatDigest(seed, "session", user, key, id, anchor))
	return "user_" + device + "_account_" + accountID + "_session_" + session, nil
}

type anthropicCompatPlan struct {
	Active         bool
	Profile        *AnthropicCompatProfile
	Metadata, Beta string
}

func (s *GatewayService) newAnthropicCompatPlan(ctx context.Context, c *gin.Context, account *Account, model string) (*anthropicCompatPlan, error) {
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil {
		logAnthropicCompatAttempt(ctx, account, model, "ineligible", "configuration_conflict")
		return nil, ErrNoAvailableAccounts
	}
	if profile == nil {
		return nil, nil
	}
	request := compatRequest(ctx)
	if request == nil {
		logAnthropicCompatAttempt(ctx, account, model, "ineligible", "request_context_missing")
		return nil, ErrNoAvailableAccounts
	}
	requestedModel := anthropicCompatDispatchModel(ctx, request.Model)
	if !profile.SupportsModel(model) || requestedModel != "" && (!account.IsModelSupported(requestedModel) || !profile.SupportsModel(account.GetMappedModel(requestedModel))) {
		logAnthropicCompatAttempt(ctx, account, model, "ineligible", "model_unsupported")
		return nil, ErrNoAvailableAccounts
	}
	// Selection already checks mapping and admission. Recheck mutable configuration
	// after hydration, using the mapped model that will actually go upstream.
	if reason := s.anthropicCompatReadiness(ctx, account, profile, model, request.ClientBeta); reason != "" {
		logAnthropicCompatAttempt(ctx, account, model, "ineligible", reason)
		return nil, ErrNoAvailableAccounts
	}
	if c != nil {
		filters := s.evaluateBetaPolicy(ctx, "", account, model).filterSet
		if filters == nil {
			filters = map[string]struct{}{}
		}
		c.Set(betaPolicyFilterSetKey, filters)
	}
	metadata, err := compatIdentity(account, request.UserID, request.KeyID, compatSessionAnchor(ctx, request))
	if err != nil {
		return nil, err
	}
	return &anthropicCompatPlan{Active: true, Profile: profile, Metadata: metadata}, nil
}

func compatResolveBeta(plan *anthropicCompatPlan, client, computed string, drop map[string]struct{}) (string, error) {
	if plan == nil {
		return computed, nil
	}
	for _, required := range plan.Profile.RequiredBetas {
		if _, denied := drop[required]; denied {
			return "", ErrNoAvailableAccounts
		}
	}
	seen := map[string]bool{}
	var tokens []string
	for _, list := range []string{strings.Join(plan.Profile.RequiredBetas, ","), client, computed} {
		for _, raw := range strings.Split(list, ",") {
			token := strings.TrimSpace(raw)
			if _, denied := drop[token]; denied {
				continue
			}
			if token != "" && !seen[token] {
				seen[token] = true
				tokens = append(tokens, token)
			}
		}
	}
	return strings.Join(tokens, ","), nil
}

func applyAnthropicCompatHeaders(headers http.Header, plan *anthropicCompatPlan) {
	if plan == nil {
		return
	}
	for name, value := range map[string]string{"User-Agent": plan.Profile.UserAgent, "X-App": "cli", "anthropic-version": plan.Profile.APIVersion, "anthropic-beta": plan.Beta, "X-Claude-Code-Session-Id": compatMetadataSession(plan.Metadata)} {
		deleteHeaderAllForms(headers, name)
		setHeaderRaw(headers, name, value)
	}
}

func logAnthropicCompatAttempt(ctx context.Context, account *Account, model, decision, reason string) {
	if account == nil || account.GetExtraString(AnthropicCompatProfileExtraKey) != AnthropicCompatProfileClaudeCode {
		return
	}
	entry, requestID := "messages", ""
	if request := compatRequest(ctx); request != nil {
		entry, requestID = request.Entry, request.RequestID
		if model == "" {
			model = request.Model
		}
		if _, loaded := request.logged.LoadOrStore(fmt.Sprintf("%d:%s", account.ID, decision), true); loaded {
			return
		}
	}
	logger.L().Info("gateway.anthropic_compat_attempt", zap.String("request_id", requestID), zap.Int64("account_id", account.ID), zap.String("entry", entry), zap.String("model", model), zap.String("decision", decision), zap.String("reason", reason))
}

// beginAnthropicForwardAttempt clears account-local state on every forwarding
// attempt, then validates compatibility before any passthrough early return.
func beginAnthropicForwardAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte) error {
	if c != nil {
		c.Set(toolNameRewriteKey, nil)
		c.Set(betaPolicyFilterSetKey, nil)
	}
	if account == nil {
		return nil
	}
	mode := account.GetExtraString(AnthropicCompatProfileExtraKey)
	if mode == "" || mode == AnthropicCompatProfileOff {
		return nil
	}
	if compatRequest(ctx) == nil {
		logAnthropicCompatAttempt(ctx, account, gjson.GetBytes(body, "model").String(), "ineligible", "request_context_missing")
		return ErrNoAvailableAccounts
	}
	if _, err := ResolveAnthropicCompatProfile(account); err != nil {
		logAnthropicCompatAttempt(ctx, account, gjson.GetBytes(body, "model").String(), "ineligible", "configuration_conflict")
		return ErrNoAvailableAccounts
	}
	return nil
}
