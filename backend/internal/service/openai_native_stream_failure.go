package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

// An attempt's timer cancels only the upstream child. The request can still
// deliver a failure or select a replacement before any upstream identity escaped.
func nativeStreamAttemptReadFailure(ctx context.Context, err error) bool {
	if !NativeStreamDeliveryEnabled(ctx) || ctx.Err() != nil {
		return false
	}
	if ControlledStreamSnapshot(ctx).CancelReason.excludesProviderHealth() {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func nativeStreamReadFailover(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return controlledSchedulingTransportFailure(ctx, fmt.Errorf("first_output_timeout: %w", err))
	}
	return controlledSchedulingTransportFailure(ctx, err)
}

// Local failure delivery has its own scheduler capability. It must not attempt
// to recommit an expired upstream attempt or claim a successful upstream terminal.
func writeNativeResponsesFailure(ctx context.Context, c *gin.Context, responseID string, sequence int64, code, message string) error {
	identity, exists := nativeResponsesDeliveredIdentityFromContext(c)
	if exists {
		responseID, sequence = identity.ID, identity.Sequence+1
	}
	createdAt := identity.CreatedAt
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	if sequence < 0 {
		sequence = 0
	}
	frame := []byte("event: error\ndata: {\"type\":\"error\",\"sequence_number\":" + strconv.FormatInt(sequence, 10) +
		",\"code\":" + strconv.Quote(code) + ",\"message\":" + strconv.Quote(message) + ",\"param\":null}\n\n")
	if responseID != "" {
		frame = []byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":" + strconv.FormatInt(sequence, 10) +
			",\"response\":{\"id\":" + strconv.Quote(responseID) + ",\"object\":\"response\",\"created_at\":" + strconv.FormatInt(createdAt, 10) + ",\"model\":" + strconv.Quote(identity.Model) + ",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":" + strconv.Quote(code) + ",\"message\":" + strconv.Quote(message) + "}}}\n\n")
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	n, err := WriteControlledStreamFailure(ctx, c.Writer, frame)
	if err == nil && n > 0 {
		MarkResponseCommitted(c)
	}
	return err
}

// Heartbeats contain stable gateway headers only. Upstream headers can disclose
// response identity and are applied by the adapter with the first protocol event.
func writeNativeChatHeartbeat(ctx context.Context, c *gin.Context) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	n, err := WriteNativeStreamFrame(ctx, c.Writer, []byte(":\n\n"))
	recordOpenAIStreamKeepaliveBytes(c, n)
	return err
}

func nativeStreamTimeoutCode(err error) (string, string) {
	if errors.Is(err, errNativeSSEUpstreamIdle) {
		return "stream_timeout", "Upstream response stream was idle beyond the configured interval"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "content_timeout", "Upstream produced no content before the deadline"
	}
	return "upstream_error", "Upstream response stream was interrupted"
}

const nativeResponsesDeliveredIdentityKey = "native_responses_delivered_identity"

type nativeChatDeliveredTerminalError struct {
	message string
}

func (e *nativeChatDeliveredTerminalError) Error() string {
	return "upstream chat stream failed: " + e.message
}

// Only a complete upstream Chat error event successfully written and flushed
// may suppress the handler's additional failure event.
func IsNativeChatTerminalError(err error) bool {
	var terminal *nativeChatDeliveredTerminalError
	return errors.As(err, &terminal)
}

type nativeResponsesDeliveredIdentity struct {
	ID        string
	Sequence  int64
	CreatedAt int64
	Model     string
}

// nativeResponsesIdentityUpdate retains only identity fields, never a staged
// protocol payload. This lets the pre-answer spool stay bounded by its own
// configured limit even when it moves to an unlinked file.
type nativeResponsesIdentityUpdate struct {
	id         string
	sequence   int64
	hasSeq     bool
	createdAt  int64
	hasCreated bool
	model      string
}

func nativeResponsesDeliveredIdentityFromContext(c *gin.Context) (nativeResponsesDeliveredIdentity, bool) {
	if c == nil {
		return nativeResponsesDeliveredIdentity{}, false
	}
	raw, ok := c.Get(nativeResponsesDeliveredIdentityKey)
	identity, valid := raw.(nativeResponsesDeliveredIdentity)
	return identity, ok && valid
}

// NativeResponsesFailureIdentity reports only identity already delivered on the
// wire. A frame rejected by a deadline or a failed Flush cannot replace it.
func NativeResponsesFailureIdentity(c *gin.Context) (id string, nextSequence, createdAt int64) {
	identity, ok := nativeResponsesDeliveredIdentityFromContext(c)
	if !ok {
		return "", 0, 0
	}
	return identity.ID, identity.Sequence + 1, identity.CreatedAt
}

func nativeResponsesIdentityUpdateFromFrame(frame []byte) nativeResponsesIdentityUpdate {
	update := nativeResponsesIdentityUpdate{}
	if id := extractOpenAIResponseIDFromJSONBytes(frame); id != "" {
		update.id = id
	}
	if sequence := gjson.GetBytes(frame, "sequence_number"); sequence.Type == gjson.Number {
		update.sequence = sequence.Int()
		update.hasSeq = true
	}
	if created := gjson.GetBytes(frame, "response.created_at").Int(); created > 0 {
		update.createdAt = created
		update.hasCreated = true
	}
	update.model = gjson.GetBytes(frame, "response.model").String()
	return update
}

func recordNativeResponsesDeliveredIdentityUpdate(c *gin.Context, update nativeResponsesIdentityUpdate) {
	if c == nil {
		return
	}
	identity, ok := nativeResponsesDeliveredIdentityFromContext(c)
	if !ok {
		identity.Sequence = -1
	}
	if update.id != "" {
		identity.ID = update.id
	}
	if update.hasSeq && update.sequence > identity.Sequence {
		identity.Sequence = update.sequence
	}
	if update.hasCreated {
		identity.CreatedAt = update.createdAt
	}
	if update.model != "" {
		identity.Model = update.model
	}
	c.Set(nativeResponsesDeliveredIdentityKey, identity)
}

func recordNativeResponsesDeliveredIdentity(c *gin.Context, frame []byte) {
	recordNativeResponsesDeliveredIdentityUpdate(c, nativeResponsesIdentityUpdateFromFrame(frame))
}
