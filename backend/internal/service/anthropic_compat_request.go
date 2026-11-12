package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

type anthropicCompatRequestKey struct{}
type anthropicCompatDispatchModelKey struct{}
type anthropicCompatSessionKey struct{}
type anthropicCompatStickyKey struct{}

// Request-local identity inputs contain no signed history or persisted state.
type anthropicCompatRequest struct {
	UserID, KeyID                                  int64
	Entry, Anchor, RandomAnchor, ClientBeta, Model string
	logged                                         sync.Map
}

func WithAnthropicCompatRequest(ctx context.Context, userID, keyID int64, body []byte, entry, explicitSession string, headers http.Header) context.Context {
	anchor := strings.TrimSpace(explicitSession)
	if anchor == "" {
		anchor = compatMetadataSession(gjson.GetBytes(body, "metadata.user_id").String())
	}
	request := &anthropicCompatRequest{UserID: userID, KeyID: keyID, Entry: entry, Anchor: anchor, RandomAnchor: uuid.NewString(), ClientBeta: headers.Get("anthropic-beta"), Model: gjson.GetBytes(body, "model").String()}
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

func (s *GatewayService) newAnthropicCompatPlan(ctx context.Context, c *gin.Context, account *Account, body []byte, model string) (*anthropicCompatPlan, error) {
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
		headers := http.Header{}
		if c != nil && c.Request != nil {
			headers = c.Request.Header
		}
		ctx = WithAnthropicCompatRequest(ctx, 0, 0, body, "messages", headers.Get("X-Claude-Code-Session-Id"), headers)
		request = compatRequest(ctx)
		if c != nil && c.Request != nil {
			c.Request = c.Request.WithContext(ctx)
		}
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
	entry := "messages"
	if request := compatRequest(ctx); request != nil {
		entry = request.Entry
		if model == "" {
			model = request.Model
		}
		if _, loaded := request.logged.LoadOrStore(fmt.Sprintf("%d:%s", account.ID, decision), true); loaded {
			return
		}
	}
	requestID, _ := ctx.Value(ctxkey.RequestID).(string)
	logger.L().Info("gateway.anthropic_compat_attempt", zap.String("request_id", requestID), zap.Int64("account_id", account.ID), zap.String("entry", entry), zap.String("model", model), zap.String("decision", decision), zap.String("reason", reason))
}

func (s *GatewayService) logAnthropicCompatOutcome(ctx context.Context, c *gin.Context, account *Account, entry string, err error) {
	if account == nil {
		return
	}
	model := ""
	if request := compatRequest(ctx); request != nil {
		model = request.Model
	} else if c != nil {
		model = c.GetString("ops_request_model")
	}
	if err == nil || !errors.Is(err, ErrNoAvailableAccounts) {
		logAnthropicCompatAttempt(ctx, account, model, "adapted", "")
	}
}

// BuildAnthropicCompatDiagnosticRequest keeps the diagnostic on the same
// deterministic envelope, identity and header adapter as live requests.
func BuildAnthropicCompatDiagnosticRequest(account *Account, body []byte, headers http.Header) ([]byte, http.Header, error) {
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil {
		return nil, nil, err
	}
	out := headers.Clone()
	if profile == nil {
		return body, out, nil
	}
	model := gjson.GetBytes(body, "model").String()
	mapped := account.GetMappedModel(model)
	if !account.IsModelSupported(model) || !profile.SupportsModel(mapped) {
		return nil, nil, fmt.Errorf("model %q is not supported by the compatibility account", model)
	}
	if mapped != model {
		body, err = sjson.SetBytes(body, "model", mapped)
		if err != nil {
			return nil, nil, err
		}
	}
	request := &anthropicCompatRequest{UserID: 0, KeyID: 0, Entry: "diagnostic", Anchor: uuid.NewString(), Model: mapped}
	metadata, err := compatIdentity(account, request.UserID, request.KeyID, request.Anchor)
	if err != nil {
		return nil, nil, err
	}
	beta, err := compatResolveBeta(&anthropicCompatPlan{Profile: profile}, headers.Get("anthropic-beta"), "", nil)
	if err != nil {
		return nil, nil, err
	}
	adapted, err := compatInjectBody(body, profile, metadata)
	if err != nil {
		return nil, nil, err
	}
	applyAnthropicCompatHeaders(out, &anthropicCompatPlan{Active: true, Profile: profile, Metadata: metadata, Beta: beta})
	return adapted, out, nil
}

type anthropicCompatDiagnosticBody struct {
	source                           io.ReadCloser
	reader                           *bufio.Reader
	pending                          []byte
	done                             bool
	sawStart, sawStopReason, sawStop bool
	openBlocks                       int
	terminalErr                      error
}

func newAnthropicCompatDiagnosticBody(_ context.Context, source io.ReadCloser) io.ReadCloser {
	return &anthropicCompatDiagnosticBody{source: source, reader: bufio.NewReaderSize(source, 32*1024)}
}

func (b *anthropicCompatDiagnosticBody) Close() error { return b.source.Close() }
func (b *anthropicCompatDiagnosticBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(b.pending) > 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		return n, nil
	}
	if b.terminalErr != nil {
		err := b.terminalErr
		b.terminalErr = nil
		return 0, err
	}
	if b.done {
		return 0, io.EOF
	}
	frame, err := b.readEvent()
	if len(frame) == 0 {
		if errors.Is(err, io.EOF) && b.sawStop {
			b.done = true
			return 0, io.EOF
		}
		b.done = true
		b.terminalErr = io.ErrUnexpectedEOF
		return 0, b.terminalErr
	}
	if err != nil {
		b.done = true
		b.terminalErr = io.ErrUnexpectedEOF
		return 0, b.terminalErr
	}
	normalized, validationErr := b.observeEvent(frame)
	if validationErr != nil {
		b.done = true
		b.terminalErr = validationErr
		return 0, validationErr
	}
	b.pending = normalized
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *anthropicCompatDiagnosticBody) readEvent() ([]byte, error) {
	var event []byte
	lineBytes := 0
	var lineFirst byte
	for {
		fragment, err := b.reader.ReadSlice('\n')
		if len(event)+len(fragment) > 8<<20 {
			return nil, bufio.ErrTooLong
		}
		if lineBytes == 0 && len(fragment) > 0 {
			lineFirst = fragment[0]
		}
		lineBytes += len(fragment)
		event = append(event, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return event, err
		}
		if lineBytes == 1 && lineFirst == '\n' || lineBytes == 2 && lineFirst == '\r' && bytes.HasSuffix(fragment, []byte("\n")) {
			return event, nil
		}
		lineBytes = 0
	}
}

func (b *anthropicCompatDiagnosticBody) observeEvent(event []byte) ([]byte, error) {
	var eventName string
	var data []string
	for _, raw := range strings.Split(string(event), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			data = append(data, value)
		}
	}
	if len(data) == 0 {
		return event, nil
	}
	payload := []byte(strings.Join(data, "\n"))
	if !json.Valid(payload) {
		return nil, fmt.Errorf("invalid Anthropic diagnostic event JSON")
	}
	kind := gjson.GetBytes(payload, "type").String()
	if eventName != "" && kind != "" && eventName != kind {
		return nil, fmt.Errorf("Anthropic diagnostic event name does not match payload")
	}
	if kind == "" {
		kind = eventName
	}
	switch kind {
	case "message_start":
		b.sawStart = true
	case "content_block_start":
		b.openBlocks++
	case "content_block_stop":
		if b.openBlocks == 0 {
			return nil, fmt.Errorf("Anthropic diagnostic block stop has no open block")
		}
		b.openBlocks--
	case "message_delta":
		if gjson.GetBytes(payload, "delta.stop_reason").String() != "" {
			b.sawStopReason = true
		}
	case "message_stop":
		if !b.sawStart || !b.sawStopReason || b.openBlocks != 0 {
			return nil, fmt.Errorf("incomplete Anthropic diagnostic response")
		}
		b.sawStop = true
	case "error":
		return nil, fmt.Errorf("Anthropic diagnostic upstream returned an error event")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return nil, err
	}
	if kind == "" {
		return event, nil
	}
	return []byte("event: " + kind + "\ndata: " + compact.String() + "\n\n"), nil
}
