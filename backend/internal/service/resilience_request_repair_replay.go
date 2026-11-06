package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const openAIRequestRepairContextKey = "xy_resilience_request_repair"

var openAIEncryptedItemPrefixPattern = regexp.MustCompile(`(?i)\bfor item\s+([a-z0-9]+)_`)

// openAIRequestRepair carries one account attempt's request-local repair
// state. body is replaced at the start of every forwarding loop iteration;
// it is never written in place by the signal constructor.
type openAIRequestRepair struct {
	accountID               int64
	body                    []byte
	encryptedTried          bool
	passthrough             bool
	m3                      *openAIM3Attempt
	rejectedFieldRetryState *openAIResponsesRejectedFieldRetryState
}

func armOpenAIRequestRepair(c *gin.Context, account *Account, rejectedFieldRetryState *openAIResponsesRejectedFieldRetryState) *openAIRequestRepair {
	if c == nil || c.Request == nil {
		return nil
	}
	c.Set(openAIRequestRepairContextKey, (*openAIRequestRepair)(nil))
	if account == nil {
		return nil
	}
	repair := &openAIRequestRepair{
		accountID:               account.ID,
		rejectedFieldRetryState: rejectedFieldRetryState,
	}
	c.Set(openAIRequestRepairContextKey, repair)
	return repair
}

func (r *openAIRequestRepair) setBody(body []byte) {
	if r != nil {
		r.body = body
	}
}

func openAIRequestRepairFrom(c *gin.Context) *openAIRequestRepair {
	if c == nil {
		return nil
	}
	v, _ := c.Get(openAIRequestRepairContextKey)
	r, _ := v.(*openAIRequestRepair)
	return r
}

type openAIRequestRepairSignal struct {
	kind string
	next []byte
}

func (e *openAIRequestRepairSignal) Error() string {
	if e == nil {
		return "upstream request repair required"
	}
	return "upstream request repair required: " + e.kind
}

func openAIEncryptedContentRepairBody(body []byte, message string) ([]byte, bool, error) {
	if len(body) == 0 || strings.Contains(strings.ToLower(message), "encrypted function output") {
		return nil, false, nil
	}
	for _, match := range openAIEncryptedItemPrefixPattern.FindAllStringSubmatch(message, -1) {
		prefix := strings.ToLower(match[1])
		if prefix != "rs" && prefix != "cmp" {
			return nil, false, nil
		}
	}
	var decoded map[string]any
	err := decodeOpenAIJSONUseNumber(body, &decoded)
	if err != nil {
		return nil, false, err
	}
	if !trimOpenAIEncryptedReasoningItems(decoded) {
		return nil, false, nil
	}
	next, err := marshalOpenAIUpstreamJSON(decoded)
	if err != nil {
		return nil, false, err
	}
	if bytes.Equal(next, body) {
		return nil, false, nil
	}
	return next, true, nil
}

func openAIRequestRepairErrorObject(payload []byte) []byte {
	for _, path := range []string{"response.error", "error"} {
		raw := strings.TrimSpace(gjson.GetBytes(payload, path).Raw)
		if raw != "" && gjson.Valid(raw) && gjson.Parse(raw).IsObject() {
			return []byte(`{"error":` + raw + `}`)
		}
	}
	return nil
}

// newOpenAIRequestRepairSignal prepares a same-account replay for an in-band
// rejection. It performs all local validation before changing attempt state.
func newOpenAIRequestRepairSignal(ctx context.Context, c *gin.Context, account *Account, payload []byte, message string, wrappedStatus int) error {
	if wrappedStatus != 0 && wrappedStatus != http.StatusBadRequest {
		return nil
	}
	if wrappedStatus == 0 {
		_, _, malformedError := openAIWrappedUpstreamErrorStatus("error", payload)
		_, _, malformedFailed := openAIWrappedUpstreamErrorStatus("response.failed", payload)
		if malformedError || malformedFailed {
			return nil
		}
	}
	r := openAIRequestRepairFrom(c)
	if r == nil || account == nil || r.accountID != account.ID || len(r.body) == 0 {
		return nil
	}
	if !r.encryptedTried && (isOpenAIEncryptedContentRejectionPayload(payload) || resilience.IsEncryptedContentRejection("", message)) {
		next, changed, err := openAIEncryptedContentRepairBody(r.body, message)
		if err == nil && changed && resilience.StateFrom(ctx).Budget().ConsumeInPlaceRetry(account.ID, false, time.Now()) {
			return &openAIRequestRepairSignal{kind: resilience.ReasonEncryptedContent, next: next}
		}
	}

	_, m3Trigger := openAIM3Trigger(payload, message, wrappedStatus)
	m3Related := r.m3 != nil && (m3Trigger || len(r.m3.state.replaced) > 0)
	// Passthrough's baseline only repairs HTTP fields. In-band field repair
	// is enabled solely for M3 signals or a request already derived by M3.
	if r.rejectedFieldRetryState != nil && (!r.passthrough || m3Related) {
		errorObject := openAIRequestRepairErrorObject(payload)
		if len(errorObject) > 0 {
			next, reason, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, r.body, errorObject)
			if err == nil && changed && r.rejectedFieldRetryState.Allow(next) && resilience.StateFrom(ctx).Budget().ConsumeInPlaceRetry(account.ID, false, time.Now()) {
				return &openAIRequestRepairSignal{kind: reason, next: next}
			}
		}
	}
	status := openAIStreamFailedEventSemanticStatus(payload, message)
	if wrappedStatus != 0 {
		status = 0
	}
	return r.m3.afterLocalRepairs(ctx, r.body, payload, message, status, wrappedStatus)
}

func (s *OpenAIGatewayService) openAIRequestRepairAfterSignal(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, err error) ([]byte, string, bool) {
	var signal *openAIRequestRepairSignal
	if account == nil || !errors.As(err, &signal) || signal == nil || len(signal.next) == 0 {
		return nil, "", false
	}
	r := openAIRequestRepairFrom(c)
	if r == nil || r.accountID != account.ID {
		return nil, "", false
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if signal.kind == resilience.ReasonEncryptedContent {
		r.encryptedTried = true
		r.rejectedFieldRetryState.remember(signal.next)
	}
	logger.FromContext(ctx).Info("resilience.request_repair_replay",
		zap.Int64("account_id", account.ID),
		zap.String("kind", signal.kind),
		zap.String("form", "inband"),
	)
	return signal.next, signal.kind, true
}
