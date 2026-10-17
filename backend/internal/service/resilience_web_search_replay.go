package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// API-key relays in front of the ChatGPT Codex backend reject a request whose
// input replays hosted web_search_call items without declaring web_search,
// exactly as OAuth accounts did before the declaration fix (#7927, see
// openai_oauth_web_search_history.go). Declaring the tool for every relay is
// not safe: some relays do not know the hosted tool type and would answer
// 400. So the gateway learns it per account (xy2api gateway resilience):
//
//   - when an API-key OpenAI account rejects such a request before any
//     output, the request is replayed once on the same account with the
//     declaration the OAuth fix adds, if the request budget allows another
//     attempt;
//   - a successful replay makes the account get the declaration up front
//     for openAIWebSearchDeclarationTTL;
//   - when the declared request is rejected too, the request ends on this
//     account: switching to a relay of the same kind fails the same way. The
//     failure counts for the session and the failed-turn cap (design §4.1,
//     §9.7) and the client sees the upstream's reason.
//
// With the resilience switch off the 0.3.0 behaviour applies.

// openAIWebSearchHistoryRejectionPhrases are the upstream wordings of the
// rejection, matched case-insensitively. Add new wordings of the same
// rejection here.
var openAIWebSearchHistoryRejectionPhrases = []string{
	"response protection is unavailable",
}

// isOpenAIWebSearchHistoryRejection reports the upstream rejection of a
// request that replays web_search_call history without declaring the tool.
func isOpenAIWebSearchHistoryRejection(message string, body []byte) bool {
	texts := []string{message}
	if len(body) > 0 {
		texts = append(texts,
			gjson.GetBytes(body, "error.message").String(),
			gjson.GetBytes(body, "response.error.message").String(),
			gjson.GetBytes(body, "message").String(),
		)
	}
	for _, text := range texts {
		text = strings.ToLower(text)
		for _, phrase := range openAIWebSearchHistoryRejectionPhrases {
			if text != "" && strings.Contains(text, phrase) {
				return true
			}
		}
	}
	return false
}

const (
	// openAIWebSearchDeclarationTTL is how long an account keeps getting the
	// declaration up front after a successful replay.
	openAIWebSearchDeclarationTTL = 6 * time.Hour
	// openAIWebSearchDeclarationMax bounds the remembered accounts.
	openAIWebSearchDeclarationMax = 4096
)

// openAIWebSearchDeclarations remembers, per process, the accounts that
// need the declaration.
type openAIWebSearchDeclarations struct {
	mu    sync.Mutex
	until map[int64]time.Time
}

var openAIWebSearchDeclared = &openAIWebSearchDeclarations{}

// needed reports whether the account is remembered at now.
func (m *openAIWebSearchDeclarations) needed(accountID int64, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.until[accountID]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(m.until, accountID)
	return false
}

// remember records the account until now plus the TTL. Expired entries are
// dropped first; when the map is still full the entry closest to expiry
// makes room.
func (m *openAIWebSearchDeclarations) remember(accountID int64, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.until == nil {
		m.until = map[int64]time.Time{}
	}
	var oldestID int64
	var oldest time.Time
	for id, until := range m.until {
		if !now.Before(until) {
			delete(m.until, id)
			continue
		}
		if oldest.IsZero() || until.Before(oldest) {
			oldestID, oldest = id, until
		}
	}
	if _, ok := m.until[accountID]; !ok && len(m.until) >= openAIWebSearchDeclarationMax {
		delete(m.until, oldestID)
	}
	m.until[accountID] = now.Add(openAIWebSearchDeclarationTTL)
}

// openAIWebSearchReplayContextKey carries the state of the current attempt
// to the stream readers.
const openAIWebSearchReplayContextKey = "xy_resilience_web_search_replay"

// openAIWebSearchReplay is the web_search history state of one forwarding
// attempt on an API-key account.
type openAIWebSearchReplay struct {
	accountID     int64
	responsesLite bool
	passthrough   bool
	// declared: the body sent carries the declaration (remembered account
	// or replay); replayed: it was added after a rejection.
	declared bool
	replayed bool
}

// armOpenAIWebSearchReplay prepares an attempt. It returns nil and the body
// unchanged unless the account is an API-key OpenAI account and the input
// replays web_search_call history without a web_search declaration; for a
// remembered account the returned body carries the declaration.
func armOpenAIWebSearchReplay(c *gin.Context, account *Account, body []byte, passthrough bool) (*openAIWebSearchReplay, []byte) {
	if c == nil || c.Request == nil {
		return nil, body
	}
	if _, set := c.Get(openAIWebSearchReplayContextKey); set {
		c.Set(openAIWebSearchReplayContextKey, (*openAIWebSearchReplay)(nil))
	}
	if account == nil || !account.IsOpenAIApiKey() || !resilience.Current().Enabled ||
		isOpenAIResponsesCompactPath(c) || !bytes.Contains(body, []byte(openAIWebSearchCallItemType)) {
		return nil, body
	}
	lite := isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) || isOpenAIResponsesLiteWebSocketPayload(body)
	declaredBody, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody(body, lite)
	if err != nil || !changed {
		return nil, body
	}
	r := &openAIWebSearchReplay{accountID: account.ID, responsesLite: lite, passthrough: passthrough}
	c.Set(openAIWebSearchReplayContextKey, r)
	if !openAIWebSearchDeclared.needed(account.ID, time.Now()) {
		return r, body
	}
	r.declared = true
	logger.FromContext(c.Request.Context()).Debug("resilience.web_search_declared",
		zap.Int64("account_id", account.ID))
	return r, declaredBody
}

// declaredUpFront reports that arm added the declaration for a remembered
// account.
func (r *openAIWebSearchReplay) declaredUpFront() bool {
	return r != nil && r.declared && !r.replayed
}

// succeeded remembers the account after its corrected replay went through.
func (r *openAIWebSearchReplay) succeeded() {
	if r == nil || !r.replayed {
		return
	}
	openAIWebSearchDeclared.remember(r.accountID, time.Now())
	logger.L().Info("resilience.web_search_declaration_learned", zap.Int64("account_id", r.accountID))
}

func openAIWebSearchReplayFrom(c *gin.Context) *openAIWebSearchReplay {
	if c == nil {
		return nil
	}
	v, _ := c.Get(openAIWebSearchReplayContextKey)
	r, _ := v.(*openAIWebSearchReplay)
	return r
}

// openAIWebSearchReplaySignal ends a stream that the upstream rejected
// before any output, so the forwarder can decide instead of relaying the
// error event.
type openAIWebSearchReplaySignal struct {
	payload []byte
	message string
}

func (e *openAIWebSearchReplaySignal) Error() string {
	return "upstream rejected web_search history: " + e.message
}

// newOpenAIWebSearchReplaySignal returns the signal for a failed event seen
// before output when the attempt is armed for the account and the event is
// the web_search history rejection, nil otherwise.
func newOpenAIWebSearchReplaySignal(c *gin.Context, account *Account, payload []byte, message string) error {
	r := openAIWebSearchReplayFrom(c)
	if r == nil || account == nil || r.accountID != account.ID || !isOpenAIWebSearchHistoryRejection(message, payload) {
		return nil
	}
	return &openAIWebSearchReplaySignal{
		payload: append([]byte(nil), payload...),
		message: sanitizeUpstreamErrorMessage(strings.TrimSpace(message)),
	}
}

// openAIWebSearchReplayAfterHTTPError decides on an upstream HTTP error
// answer: it returns the body of the corrected replay, or the final error,
// or neither when the answer is not the web_search history rejection.
func (s *OpenAIGatewayService) openAIWebSearchReplayAfterHTTPError(ctx context.Context, c *gin.Context, account *Account, r *openAIWebSearchReplay, sent []byte, resp *http.Response, respBody []byte, upstreamMsg string) ([]byte, error) {
	if r == nil || account == nil || resp == nil || r.accountID != account.ID || !isOpenAIWebSearchHistoryRejection(upstreamMsg, respBody) {
		return nil, nil
	}
	return s.openAIWebSearchReplayDecide(ctx, c, account, r, sent, resp.StatusCode, resp.Header.Get("x-request-id"), respBody, upstreamMsg)
}

// openAIWebSearchReplayAfterSignal is openAIWebSearchReplayAfterHTTPError
// for a stream that ended with the signal. The failed response is closed
// once decided.
func (s *OpenAIGatewayService) openAIWebSearchReplayAfterSignal(ctx context.Context, c *gin.Context, account *Account, r *openAIWebSearchReplay, sent []byte, resp *http.Response, err error) ([]byte, error) {
	var signal *openAIWebSearchReplaySignal
	if account == nil || !errors.As(err, &signal) || signal == nil {
		return nil, nil
	}
	if r == nil || r.accountID != account.ID {
		// The signal only comes from an attempt armed for this account;
		// end the request rather than lose the failure.
		r = &openAIWebSearchReplay{accountID: account.ID, declared: true}
	}
	requestID := ""
	if resp != nil {
		requestID = resp.Header.Get("x-request-id")
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	return s.openAIWebSearchReplayDecide(ctx, c, account, r, sent, http.StatusOK, requestID, signal.payload, signal.message)
}

func (s *OpenAIGatewayService) openAIWebSearchReplayDecide(ctx context.Context, c *gin.Context, account *Account, r *openAIWebSearchReplay, sent []byte, status int, requestID string, payload []byte, message string) ([]byte, error) {
	if !r.declared {
		next, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody(sent, r.responsesLite)
		if err == nil && changed && resilience.StateFrom(ctx).Budget().ConsumeInPlaceRetry(account.ID, false, time.Now()) {
			r.declared, r.replayed = true, true
			logger.FromContext(ctx).Info("resilience.web_search_replay",
				zap.Int64("account_id", account.ID),
				zap.Int("upstream_status", status),
				zap.Bool("passthrough", r.passthrough),
			)
			return next, nil
		}
	}
	return nil, s.openAIWebSearchHistoryRejected(ctx, c, account, r, status, requestID, payload, message)
}

// OpenAIWebSearchHistoryRejectedReason marks a request whose web_search_call
// history the account rejects even with the tool declared, or when the
// request budget leaves no room for the corrected replay.
const OpenAIWebSearchHistoryRejectedReason = GatewayFailureReason("openai_web_search_history_rejected")

// openAIWebSearchHistoryRejected builds the final error: no further account
// is tried and the account is not penalised (it serves other requests), but
// the session counts the failure so a conversation stuck on the account
// moves away from it.
func (s *OpenAIGatewayService) openAIWebSearchHistoryRejected(ctx context.Context, c *gin.Context, account *Account, r *openAIWebSearchReplay, status int, requestID string, payload []byte, message string) *UpstreamFailoverError {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = openAIWebSearchHistoryRejectionPhrases[0]
	}
	clientMessage := "Upstream rejected the web_search history in this request: " + message
	body, _ := json.Marshal(gin.H{"error": gin.H{"type": "upstream_error", "message": clientMessage}})
	failoverErr := &UpstreamFailoverError{
		StatusCode:        http.StatusBadGateway,
		ResponseBody:      body,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            OpenAIWebSearchHistoryRejectedReason,
		NextAccountAction: NextAccountStop,
		ClientStatusCode:  http.StatusBadGateway,
		ClientMessage:     clientMessage,
	}
	detail := ""
	if s != nil && s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(string(payload), maxBytes)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:              opsUpstreamProxyID(account),
		ProxyName:            opsUpstreamProxyName(account),
		Platform:             account.Platform,
		AccountID:            account.ID,
		AccountName:          account.Name,
		UpstreamStatusCode:   status,
		UpstreamRequestID:    strings.TrimSpace(requestID),
		Passthrough:          r.passthrough,
		Kind:                 "http_error",
		Reason:               string(OpenAIWebSearchHistoryRejectedReason),
		Message:              message,
		Detail:               detail,
		UpstreamResponseBody: detail,
	})
	// The upstream reports an internal error and the client gets a 502: the
	// session counts it as the transient failure it is reported as.
	resilience.StateFrom(ctx).SessionFailure(ctx, account.ID, resilience.CategoryTransient)
	logger.FromContext(ctx).Warn("resilience.web_search_history_rejected",
		zap.Int64("account_id", account.ID),
		zap.Int("upstream_status", status),
		zap.Bool("replayed", r.replayed),
		zap.Bool("passthrough", r.passthrough),
	)
	return failoverErr
}
