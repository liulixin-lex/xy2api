package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

const OpenAIStateRepairReason GatewayFailureReason = "openai_state_repair"
const openAIM3ContextKey = "xy_openai_m3"
const openAIM3Placeholder = `{"type":"input_text","text":"[encrypted content omitted]"}`

var openAIM3ItemPattern = regexp.MustCompile(`(?i)\bfor item\s+(amsg_[a-z0-9_-]+)`)

type openAIM3SlotID struct {
	openAIBadEncryptedSlot
	part          int
	anonymousItem int // only used when ref is absent; never persisted in Bad
}
type openAIM3Slot struct {
	id        openAIM3SlotID
	path      string
	itemID    string
	itemIndex int
}
type openAIM3Account struct {
	replaced map[openAIM3SlotID]struct{}
	pending  map[openAIBadEncryptedSlot]struct{}
	replayed bool
}
type openAIM3Request struct {
	groupID  int64
	session  string
	accounts map[int64]*openAIM3Account
}
type openAIM3Attempt struct {
	service *OpenAIGatewayService
	request *openAIM3Request
	account *Account
	state   *openAIM3Account
}

// Capture the ingress session before normalization or any per-account derivation.
func (s *OpenAIGatewayService) beginOpenAIM3(c *gin.Context, body []byte) *openAIM3Request {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(openAIM3ContextKey); ok {
		if r, ok := v.(*openAIM3Request); ok {
			return r
		}
	}
	r := &openAIM3Request{session: s.openAIWSLineageSessionHashFromContext(c, body), accounts: make(map[int64]*openAIM3Account)}
	r.groupID = getOpenAIGroupIDFromContext(c)
	c.Set(openAIM3ContextKey, r)
	return r
}
func (s *OpenAIGatewayService) armOpenAIM3(c *gin.Context, account *Account, body []byte) *openAIM3Attempt {
	r := s.beginOpenAIM3(c, body)
	if r == nil || account == nil || account.Platform != PlatformOpenAI {
		return nil
	}
	// These are the same gates as normalizeDeepSeekResponsesRequestBody;
	// its media lifting may stringify fco.output and cannot preserve M3 slots.
	if account.UsesNativeCNResponses() || shouldAliasDeepSeekResponsesInputImages(account) {
		return nil
	}
	a := r.accounts[account.ID]
	if a == nil {
		a = &openAIM3Account{replaced: make(map[openAIM3SlotID]struct{}), pending: make(map[openAIBadEncryptedSlot]struct{})}
		r.accounts[account.ID] = a
	}
	return &openAIM3Attempt{service: s, request: r, account: account, state: a}
}

// Only inspect protocol containers. In particular arguments and string output remain opaque.
func openAIM3Slots(body []byte) ([]openAIM3Slot, map[string]int, error) {
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() || !codexCollabUniqueObject(root) {
		return nil, nil, errors.New("ambiguous request object")
	}
	input := root.Get("input")
	if !input.Exists() || input.Type == gjson.String {
		return nil, map[string]int{}, nil
	}
	if !input.IsArray() {
		return nil, nil, errors.New("invalid input container")
	}
	var slots []openAIM3Slot
	ids := make(map[string]int)
	for i, item := range input.Array() {
		if !item.IsObject() || !codexCollabUniqueObject(item) {
			return nil, nil, errors.New("ambiguous input item")
		}
		if id := item.Get("id"); id.Type == gjson.String {
			ids[id.String()]++
		}
		var kind, parts, refKey string
		switch item.Get("type").String() {
		case openAIM3AgentMessage:
			kind, parts, refKey = openAIM3AgentMessage, "content", "id"
		case openAIM3FunctionOutput:
			kind, parts, refKey = openAIM3FunctionOutput, "output", "call_id"
		default:
			continue
		}
		ref := item.Get(refKey)
		if ref.Exists() && ref.Type != gjson.String {
			return nil, nil, errors.New("invalid item reference")
		}
		// gjson strings may retain the entire request. Bound and detach refs
		// before adding them to request state or the shared Bad store.
		var refValue string
		if len(ref.String()) <= openAIM3MaxRefBytes {
			refValue = strings.Clone(ref.String())
		}
		container := item.Get(parts)
		if kind == "function_call_output" && container.Type == gjson.String {
			continue
		}
		if !container.IsArray() {
			return nil, nil, errors.New("invalid encrypted part container")
		}
		for j, part := range container.Array() {
			if !part.IsObject() || !codexCollabUniqueObject(part) {
				return nil, nil, errors.New("ambiguous content part")
			}
			if part.Get("type").String() != "encrypted_content" {
				continue
			}
			if !codexCollabUnambiguousJSON(part) {
				return nil, nil, errors.New("ambiguous encrypted slot")
			}
			var value any
			if err := decodeOpenAIJSONUseNumber([]byte(part.Raw), &value); err != nil {
				return nil, nil, err
			}
			var normalized bytes.Buffer
			enc := json.NewEncoder(&normalized)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(value); err != nil {
				return nil, nil, err
			}
			sum := sha256.Sum256(bytes.TrimSuffix(normalized.Bytes(), []byte("\n")))
			id := openAIM3SlotID{openAIBadEncryptedSlot: openAIBadEncryptedSlot{kind, refValue, hex.EncodeToString(sum[:])}, part: j, anonymousItem: -1}
			if id.ref == "" {
				id.anonymousItem = i
			}
			slots = append(slots, openAIM3Slot{id: id, path: "input." + strconv.Itoa(i) + "." + parts + "." + strconv.Itoa(j), itemID: item.Get("id").String(), itemIndex: i})
		}
	}
	refItems := make(map[openAIBadEncryptedSlot]map[int]struct{})
	for _, slot := range slots {
		if slot.id.ref == "" {
			continue
		}
		ref := openAIBadEncryptedSlot{kind: slot.id.kind, ref: slot.id.ref}
		if refItems[ref] == nil {
			refItems[ref] = make(map[int]struct{})
		}
		refItems[ref][slot.itemIndex] = struct{}{}
	}
	for _, items := range refItems {
		if len(items) > 1 {
			return nil, nil, errors.New("duplicate encrypted item reference")
		}
	}
	return slots, ids, nil
}
func (a *openAIM3Attempt) failure(family resilience.StateFamily, itemID, cause string) *UpstreamFailoverError {
	// Upstream messages may echo ciphertext. Only classified reasons and an
	// item ID uniquely verified against this request may reach clients or ops.
	message := "The upstream rejected the derived encrypted-state request"
	switch family {
	case resilience.StateConversationInvalid:
		message = "Encrypted content is invalid for this conversation"
	case resilience.StateFunctionOutput:
		message = "Encrypted function output content could not be decrypted"
	case resilience.StateAgentMessage:
		message = "Encrypted content could not be verified"
		if itemID != "" {
			message += " for item " + itemID
		}
	}
	return annotateUpstreamFailoverAccount(&UpstreamFailoverError{StatusCode: 400, Reason: OpenAIStateRepairReason, Scope: GatewayFailureScopeRequest, NextAccountAction: NextAccountRetry, ClientStatusCode: 502, ClientMessage: message, SafeToFailoverAfterWrite: true, M3Cause: cause}, a.account)
}
func (a *openAIM3Attempt) event(ctx context.Context, name string, count int) {
	logger.FromContext(ctx).Warn(name, zap.Int64("group_id", a.request.groupID), zap.Int64("account_id", a.account.ID), zap.Int("count", count))
}
func (a *openAIM3Attempt) persist(ctx context.Context, slots []openAIBadEncryptedSlot) {
	if len(slots) == 0 {
		return
	}
	if store := a.service.getOpenAIWSStateStore(); store != nil {
		n := store.MarkSessionBadEncryptedSlots(a.request.groupID, a.request.session, a.account.ID, slots, a.service.openAIWSSessionStickyTTL())
		if n > 0 {
			a.event(ctx, "resilience.m3_learned", n)
		}
	}
}

// Apply to the sole outbound buffer immediately before the existing builder.
func (a *openAIM3Attempt) derive(ctx context.Context, body []byte) ([]byte, error) {
	if a == nil {
		return body, nil
	}
	var bad map[openAIBadEncryptedSlot]struct{}
	if store := a.service.getOpenAIWSStateStore(); store != nil {
		bad = store.GetSessionBadEncryptedSlots(a.request.groupID, a.request.session, a.account.ID)
	}
	slots, _, err := openAIM3Slots(body)
	if err != nil {
		if len(bad) == 0 && len(a.state.replaced) == 0 {
			return body, nil
		}
		return nil, a.failure(resilience.StateNone, "", "shape")
	}
	count := 0
	for _, slot := range slots {
		_, replaced := a.state.replaced[slot.id]
		_, learned := bad[slot.id.openAIBadEncryptedSlot]
		if !replaced && (slot.id.ref == "" || !learned) {
			continue
		}
		body, err = sjson.SetRawBytes(body, slot.path, []byte(openAIM3Placeholder))
		if err != nil {
			return nil, a.failure(resilience.StateNone, "", "shape")
		}
		a.state.replaced[slot.id] = struct{}{}
		count++
	}
	if count > 0 {
		a.event(ctx, "resilience.m3_replaced", count)
	}
	return body, nil
}
func openAIM3Trigger(payload []byte, message string, wrappedStatus int) (resilience.StateFamily, bool) {
	if wrappedStatus != 0 {
		return resilience.StateNone, wrappedStatus == 400
	}
	family := openAIStateRejection(payload, message).Family
	return family, family == resilience.StateConversationInvalid || family == resilience.StateFunctionOutput || family == resilience.StateAgentMessage
}
func (a *openAIM3Attempt) learn(ctx context.Context, body, payload []byte, message string, status, wrappedStatus int) error {
	if a == nil {
		return nil
	}
	family, trigger := openAIM3Trigger(payload, message, wrappedStatus)
	if !trigger {
		return nil
	}
	slots, ids, err := openAIM3Slots(body)
	if err != nil {
		return a.failure(family, "", "shape")
	}
	itemID := ""
	if family == resilience.StateAgentMessage {
		matches := openAIM3ItemPattern.FindAllStringSubmatch(message, -1)
		if len(matches) != 1 || ids[matches[0][1]] != 1 {
			return a.failure(family, "", "item_not_unique")
		}
		itemID = matches[0][1]
	}
	var selected []openAIM3Slot
	for _, slot := range slots {
		if itemID != "" && slot.itemID != itemID {
			continue
		}
		if _, done := a.state.replaced[slot.id]; !done {
			selected = append(selected, slot)
		}
	}
	if len(selected) == 0 {
		return a.failure(family, itemID, "no_growth")
	}
	var evidence []openAIBadEncryptedSlot
	for _, slot := range selected {
		if slot.id.ref != "" {
			evidence = append(evidence, slot.id.openAIBadEncryptedSlot)
		}
	}
	if itemID != "" {
		a.persist(ctx, evidence)
	}
	if ctx.Err() != nil || !resilience.StateFrom(ctx).Budget().ConsumeInPlaceRetry(a.account.ID, false, time.Now(), resilience.ReasonEncryptedContent) {
		return a.failure(family, itemID, "budget")
	}
	next := body
	for _, slot := range selected {
		next, err = sjson.SetRawBytes(next, slot.path, []byte(openAIM3Placeholder))
		if err != nil {
			return a.failure(family, itemID, "shape")
		}
	}
	// Only this request's newly selected identities authorize a replay. Bad is not consulted here.
	for _, slot := range selected {
		a.state.replaced[slot.id] = struct{}{}
	}
	if itemID == "" {
		for _, slot := range evidence {
			a.state.pending[slot] = struct{}{}
		}
	}
	a.state.replayed = true
	a.event(ctx, "resilience.m3_replaced", len(selected))
	return &openAIRequestRepairSignal{kind: string(OpenAIStateRepairReason), next: next}
}
func (a *openAIM3Attempt) succeeded(ctx context.Context) {
	if a == nil || !a.state.replayed {
		return
	}
	slots := make([]openAIBadEncryptedSlot, 0, len(a.state.pending))
	for slot := range a.state.pending {
		slots = append(slots, slot)
	}
	a.persist(ctx, slots)
	clear(a.state.pending)
	a.state.replayed = false
	a.event(ctx, "resilience.m3_replay_ok", 1)
}

// A nil reader error is not acceptance evidence. Reuse the reader's semantic
// output decision; clean EOF with output is checked separately by the reader.
func openAIM3TerminalAccepted(terminal string, semanticOutput bool) bool {
	switch terminal {
	case "response.completed", "response.done", "[DONE]":
		return true
	case "response.incomplete", "response.cancelled", "response.canceled":
		return semanticOutput
	default:
		return false
	}
}

func openAIM3AggregateAccepted(body, terminal string) bool {
	semanticOutput := false
	forEachOpenAISSEFrame(body, func(event string, payload []byte) {
		if !openAIStreamEventTypeIsTerminal(event) {
			semanticOutput = semanticOutput || resilience.ResponsesEventOutputDisposition(event, payload).SemanticOutput
		}
	})
	return openAIM3TerminalAccepted(terminal, semanticOutput) || terminal == "" && semanticOutput
}

func openAIM3JSONAccepted(body []byte) bool {
	root := gjson.ParseBytes(body)
	return gjson.ValidBytes(body) && root.IsObject() && root.Get("object").String() == "response" &&
		root.Get("status").String() == "completed" && root.Get("output").IsArray() &&
		root.Get("error").Type == gjson.Null
}

func openAIM3RewrittenShapeRejected(status int, payload []byte, message string) bool {
	return (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) &&
		!openAIStreamRequestRejected(payload, message)
}

func (a *openAIM3Attempt) afterLocalRepairs(ctx context.Context, body, payload []byte, message string, status, wrappedStatus int) error {
	if a == nil {
		return nil
	}
	if wrappedStatus != 0 && wrappedStatus != http.StatusBadRequest {
		return nil
	}
	if signal := a.learn(ctx, body, payload, message, status, wrappedStatus); signal != nil {
		return signal
	}
	if len(a.state.replaced) > 0 && openAIM3RewrittenShapeRejected(status, payload, message) {
		return a.failure(resilience.StateNone, "", "rewritten_4xx")
	}
	return nil
}
func (s *OpenAIGatewayService) interceptOpenAIM3Failover(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, webSearch *openAIWebSearchReplay, err error) ([]byte, error, bool) {
	var failure *UpstreamFailoverError
	if !errors.As(err, &failure) || failure.Reason == OpenAIStateRepairReason {
		return nil, err, false
	}
	message := firstNonEmpty(failure.ClientMessage, extractOpenAISSEErrorMessage(failure.ResponseBody), extractUpstreamErrorMessage(failure.ResponseBody))
	if failure.WrappedStatus != 0 && failure.WrappedStatus != http.StatusBadRequest {
		return nil, err, false
	}
	r := openAIRequestRepairFrom(c)
	if r == nil || r.m3 == nil {
		return nil, err, false
	}
	if failure.WrappedStatus == http.StatusBadRequest {
		if compactErr := newOpenAICompactFallbackSignal(c, failure.ResponseBody, message, failure.WrappedStatus); compactErr != nil {
			return nil, compactErr, false
		}
	}
	if failure.WrappedStatus == http.StatusBadRequest || failure.StatusCode == http.StatusBadRequest {
		if isOpenAIWebSearchHistoryRejection(message, failure.ResponseBody) {
			webSearchErr := &openAIWebSearchReplaySignal{payload: failure.ResponseBody, message: message, wrappedStatus: failure.WrappedStatus}
			if next, replayErr := s.openAIWebSearchReplayAfterSignal(ctx, c, account, webSearch, r.body, resp, webSearchErr); replayErr != nil {
				if next, _, ok := s.openAIRequestRepairAfterSignal(ctx, c, account, resp, replayErr); ok {
					return next, nil, true
				}
				return nil, replayErr, false
			} else if next != nil {
				return next, nil, true
			}
		}
		if repairErr := newOpenAIRequestRepairSignal(ctx, c, account, failure.ResponseBody, message, failure.WrappedStatus); repairErr != nil {
			if next, _, ok := s.openAIRequestRepairAfterSignal(ctx, c, account, resp, repairErr); ok {
				return next, nil, true
			}
			return nil, repairErr, false
		}
	}
	repaired := r.m3.afterLocalRepairs(ctx, r.body, failure.ResponseBody, message, failure.StatusCode, failure.WrappedStatus)
	if repaired == nil {
		return nil, err, false
	}
	if next, _, ok := s.openAIRequestRepairAfterSignal(ctx, c, account, resp, repaired); ok {
		return next, nil, true
	}
	return nil, repaired, false
}

// A local repair gets its bounded opportunity first. If it cannot repair a
// rewritten request, stop before its legacy terminal path can penalise the
// session or expose a non-retryable 4xx.
func openAIM3AfterLocalRepairExhausted(ctx context.Context, c *gin.Context, payload []byte, message string, status, wrappedStatus int) error {
	r := openAIRequestRepairFrom(c)
	if r == nil || r.m3 == nil {
		return nil
	}
	return r.m3.afterLocalRepairs(ctx, r.body, payload, message, status, wrappedStatus)
}

func resetOpenAIRepairAttempt(ctx context.Context, account *Account, model string, stream bool, body []byte, replay bool) time.Time {
	attemptStart := time.Now()
	state := resilience.StateFrom(ctx)
	old := state.AttemptProfile()
	state.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, Model: model, AttemptStart: attemptStart, Warm: replay || (old.AccountID == account.ID && old.Warm), Stream: stream, BodyBytes: len(body)})
	return attemptStart
}

// Preserve rejection evidence before generic stream constructors can apply
// account side effects or replace the payload. Only an armed Forward consumes it.
func deferOpenAIM3ToForward(c *gin.Context, account *Account, payload []byte, message string, status, wrappedStatus int) *UpstreamFailoverError {
	r := openAIRequestRepairFrom(c)
	if r == nil || r.m3 == nil || account == nil || r.accountID != account.ID || IsResponseCommitted(c) {
		return nil
	}
	if wrappedStatus != 0 && wrappedStatus != http.StatusBadRequest {
		return nil
	}
	if wrappedStatus == 0 {
		for _, event := range []string{"error", "response.failed"} {
			if _, _, malformed := openAIWrappedUpstreamErrorStatus(event, payload); malformed {
				return nil
			}
		}
	}
	_, trigger := openAIM3Trigger(payload, message, wrappedStatus)
	if !trigger && (len(r.m3.state.replaced) == 0 || !openAIM3RewrittenShapeRejected(status, payload, message)) {
		return nil
	}
	return annotateUpstreamFailoverAccount(&UpstreamFailoverError{
		StatusCode: status, WrappedStatus: wrappedStatus,
		ResponseBody: append([]byte(nil), payload...), ClientMessage: message,
		SafeToFailoverAfterWrite: true,
	}, account)
}
