package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const openAIRequestRepairContextKey = "xy_resilience_request_repair"

// openAIRequestRepair carries one account attempt's request-local repair
// state. body is replaced at the start of every forwarding loop iteration;
// it is never written in place by the signal constructor.
type openAIRequestRepair struct {
	accountID               int64
	body                    []byte
	credentialVersion       string
	encryptedTried          bool
	passthrough             bool
	m3                      *openAIM3Attempt
	rejectedFieldRetryState *openAIResponsesRejectedFieldRetryState
	rejectedFieldEvent      *OpsUpstreamErrorEvent
	rejectedFieldSequence   uint64
}

func armOpenAIRequestRepair(c *gin.Context, account *Account, rejectedFieldRetryState *openAIResponsesRejectedFieldRetryState) *openAIRequestRepair {
	if c == nil || c.Request == nil {
		return nil
	}
	previous := openAIRequestRepairFrom(c)
	c.Set(openAIRequestRepairContextKey, (*openAIRequestRepair)(nil))
	if account == nil {
		return nil
	}
	repair := &openAIRequestRepair{
		accountID:               account.ID,
		rejectedFieldRetryState: rejectedFieldRetryState,
	}
	if previous != nil && previous.accountID == account.ID {
		repair.rejectedFieldEvent, repair.rejectedFieldSequence = previous.rejectedFieldEvent, previous.rejectedFieldSequence
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
	kind                 string
	next                 []byte
	encryptedItem        httpRejectedEncryptedItem
	rejectedFieldPayload []byte
}

func (e *openAIRequestRepairSignal) Error() string {
	if e == nil {
		return "upstream request repair required"
	}
	return "upstream request repair required: " + e.kind
}

func openAIEncryptedContentRepairBody(body, payload []byte) (httpRejectedEncryptedItem, error) {
	return selectHTTPRejectedEncryptedItem(body, payload)
}

// Normalization can remove an input id while the provider still names that id
// from the ciphertext. Preserve the original identity only when its ciphertext
// and type map to exactly one id-less wire item. Indices always refer to wire.
func openAIRequestEncryptedContentRepairBody(c *gin.Context, body, payload []byte) (httpRejectedEncryptedItem, error) {
	selected, err := openAIEncryptedContentRepairBody(body, payload)
	if err != nil || selected.Changed || selected.ItemIndex >= 0 || c == nil {
		return selected, err
	}
	for _, field := range []string{"code", "param", "message"} {
		if httpEncryptedIndexPattern.MatchString(openAIStreamErrorField(payload, field).String()) {
			return selected, nil
		}
	}
	original, _ := c.Get(openAIStateRejectionBodyKey)
	originalBody, _ := original.([]byte)
	located, err := selectHTTPRejectedEncryptedItem(originalBody, payload)
	if err != nil || !located.Changed || located.ItemID == "" || located.Digest == "" {
		return selected, err
	}
	named := openAIStreamErrorField(payload, "item_id").String() == located.ItemID
	for _, field := range []string{"code", "param", "message"} {
		for _, match := range httpEncryptedItemPattern.FindAllStringSubmatch(openAIStreamErrorField(payload, field).String(), -1) {
			if firstNonEmpty(match[1], match[2]) == located.ItemID {
				named = true
			}
		}
	}
	if !named {
		return selected, nil
	}
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() || !codexCollabUniqueObject(root) {
		return selected, nil
	}
	input := root.Get("input")
	items := input.Array()
	if input.IsObject() {
		items = []gjson.Result{input}
	} else if !input.IsArray() {
		return selected, nil
	}
	index := -1
	for i, item := range items {
		cipher := item.Get("encrypted_content")
		if cipher.Type != gjson.String || openAIEncryptedContentDigest(cipher.String()) != located.Digest {
			continue
		}
		if index >= 0 || item.Get("id").String() != "" || item.Get("type").String() != located.ItemType {
			return selected, nil
		}
		index = i
	}
	if index < 0 {
		return selected, nil
	}
	return repairHTTPEncryptedItem(body, input, items[index], index)
}

func openAIRequestRepairErrorObject(payload []byte) []byte {
	if object := openAIStreamErrorObject(payload); object.IsObject() {
		return []byte(`{"error":` + object.Raw + `}`)
	}
	top := openAITopLevelStreamError(payload)
	if !top.IsObject() {
		return nil
	}
	fields := map[string]any{}
	for _, key := range []string{"code", "message", "param", "status", "status_code"} {
		value := top.Get(key)
		if value.Exists() {
			fields[key] = value.Value()
		}
	}
	if len(fields) == 0 {
		return nil
	}
	body, err := marshalOpenAIUpstreamJSON(map[string]any{"error": fields})
	if err != nil {
		return nil
	}
	return body
}

// newOpenAIRequestRepairSignal prepares a same-account replay for an in-band
// rejection. It performs all local validation before changing attempt state.
func newOpenAIRequestRepairSignal(ctx context.Context, c *gin.Context, account *Account, payload []byte, message string, wrappedStatus int) error {
	r := openAIRequestRepairFrom(c)
	if r == nil || account == nil || r.accountID != account.ID || len(r.body) == 0 {
		return nil
	}
	encryptedRejection := isHTTPReasoningEncryptedRejection(r.body, payload, message)
	if wrappedStatus != 0 && wrappedStatus != http.StatusBadRequest && (wrappedStatus != http.StatusBadGateway || !encryptedRejection) {
		return nil
	}
	if wrappedStatus == 0 {
		_, _, malformedError := openAIWrappedUpstreamErrorStatus("error", payload)
		_, _, malformedFailed := openAIWrappedUpstreamErrorStatus("response.failed", payload)
		if malformedError || malformedFailed {
			return nil
		}
	}

	if repairErr := newHTTPEncryptedContentRepairSignal(ctx, c, account, r.body, payload, message, wrappedStatus); repairErr != nil {
		return repairErr
	}

	_, m3Trigger := openAIM3Trigger(payload, message, wrappedStatus)
	m3Related := r.m3 != nil && (m3Trigger || len(r.m3.state.replaced) > 0)
	// Passthrough's baseline only repairs HTTP fields. In-band field repair
	// is enabled solely for M3 signals or a request already derived by M3.
	if r.rejectedFieldRetryState != nil && (!r.passthrough || m3Related) {
		if signal := newOpenAIRejectedFieldRepairSignal(ctx, r, payload); signal != nil {
			return signal
		}
	}
	status := openAIStreamFailedEventSemanticStatus(payload, message)
	if wrappedStatus != 0 {
		status = 0
	}
	return r.m3.afterLocalRepairs(ctx, r.body, payload, message, status, wrappedStatus)
}

// newOpenAIRejectedFieldRepairSignal is shared by native Responses and Chat
// pre-output failures. Callers validate the output boundary and wrapper status.
func newOpenAIRejectedFieldRepairSignal(ctx context.Context, r *openAIRequestRepair, payload []byte) error {
	if r == nil || r.rejectedFieldRetryState == nil || len(r.body) == 0 {
		return nil
	}
	errorObject := openAIRequestRepairErrorObject(payload)
	if len(errorObject) > 0 {
		next, reason, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, r.body, errorObject)
		if err == nil && changed && r.rejectedFieldRetryState.Allow(next) && resilience.StateFrom(ctx).Budget().ConsumeInPlaceRetry(r.accountID, false, time.Now()) {
			recordOpenAIRejectedOutputLimitOmission(ctx, r.body, next)
			return &openAIRequestRepairSignal{kind: reason, next: next, rejectedFieldPayload: errorObject}
		}
	}
	return nil
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
	if len(signal.rejectedFieldPayload) > 0 {
		refusal := &http.Response{StatusCode: http.StatusBadRequest}
		if resp != nil {
			refusal.Header = resp.Header
		}
		r.rejectedFieldEvent = recordOpenAIResponsesRejectedFieldRetry(c, account, refusal, signal.rejectedFieldPayload)
		r.rejectedFieldSequence = resilience.StateFrom(ctx).OpsUpstreamEventSequence()
	}
	if signal.kind == resilience.ReasonEncryptedContent {
		r.encryptedTried = true
		r.rejectedFieldRetryState.remember(signal.next)
		if lineage := s.beginOpenAIM3(c, r.body); lineage != nil {
			s.markHTTPInvalidEncryptedContent(lineage.groupID, lineage.session, account.ID, r.credentialVersion, signal.encryptedItem)
		}
	}
	logger.FromContext(ctx).Info("resilience.request_repair_replay",
		zap.Int64("account_id", account.ID),
		zap.String("kind", signal.kind),
		zap.String("form", "inband"),
	)
	return signal.next, signal.kind, true
}

// A prepared repair is not a resend until this account crossed transport.
func noteOpenAIRequestRepairResent(ctx context.Context, c *gin.Context, account *Account) {
	r := openAIRequestRepairFrom(c)
	if r == nil || account == nil || r.accountID != account.ID || r.rejectedFieldEvent == nil {
		return
	}
	if boundary, sent := resilience.StateFrom(ctx).OpsUpstreamSendBoundary(account.ID); sent && r.rejectedFieldSequence > 0 && boundary >= r.rejectedFieldSequence {
		r.rejectedFieldEvent.Resent = true
	}
	r.rejectedFieldEvent = nil
}
