package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/tidwall/gjson"
)

type nativeBackgroundExecutionContextKey struct{}

// WithNativeBackgroundExecution is installed only by the handler after native
// nonstream background intent and capability have been established.
func WithNativeBackgroundExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeBackgroundExecutionContextKey{}, true)
}

// RetainControlledExecution transfers middleware cleanup to an explicitly owned
// worker. It never changes cancellation/deadline semantics or admits a dispatch.
func RetainControlledExecution(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, scheduling.ErrInvalidControl
	}
	r := controlledRequest(ctx)
	if r == nil {
		return func() {}, nil
	}
	r.mu.Lock()
	if r.closed || r.closeRequested {
		r.mu.Unlock()
		return nil, context.Canceled
	}
	r.retainedExecutions++
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.retainedExecutions--
			closeNow := r.retainedExecutions == 0 && r.closeRequested
			r.mu.Unlock()
			if closeNow {
				r.Close()
			}
		})
	}, nil
}

func acceptControlledNativeBackground(b *controlledResponseBody, body []byte, protocol string) bool {
	if protocol != "responses" || b == nil || b.dispatch == nil || !NativeStreamDeliveryEnabled(b.dispatch.ctx) {
		return false
	}
	enabled, _ := b.dispatch.ctx.Value(nativeBackgroundExecutionContextKey{}).(bool)
	if !enabled || !nativeStreamEventJSONValid(body) {
		return false
	}
	v := gjson.ParseBytes(body)
	status, id := v.Get("status").String(), v.Get("id").String()
	if v.Get("background").Type != gjson.True || !strings.HasPrefix(id, "resp_") || len(id) <= 5 || (status != "queued" && status != "in_progress") || v.Get("error").IsObject() {
		return false
	}
	b.dispatch.mu.Lock()
	defer b.dispatch.mu.Unlock()
	if b.dispatch.backgroundResponseID != "" && b.dispatch.backgroundResponseID != id {
		return false
	}
	b.backgroundAccepted = true
	b.nonstreamValidated = false
	b.nonstreamValidationErr = nil
	b.dispatch.backgroundResponseID = id
	// HTTP EOF proves only the create operation ended, not the generation.
	b.dispatch.transportTerminal = false
	if b.dispatch.firstEvent.IsZero() {
		b.dispatch.firstEvent = time.Now()
	}
	// A queued acknowledgement is protocol progress, never first semantic output.
	return true
}

// FinishNativeBackground settles the original dispatch once, from the actual
// polled terminal of the same response. Retrieval consumes no create attempt.
func FinishNativeBackground(ctx context.Context, response []byte, err error) error {
	if ctx == nil {
		return scheduling.ErrInvalidControl
	}
	r := controlledRequest(ctx)
	if r == nil {
		return err
	}
	r.mu.Lock()
	d := r.currentDispatch
	r.mu.Unlock()
	if d == nil {
		return err
	}
	d.mu.Lock()
	id := d.backgroundResponseID
	d.mu.Unlock()
	if id == "" {
		return err
	}
	if err != nil && len(response) == 0 {
		d.Finish("background_unconfirmed", false, err)
		return err
	}
	if !nativeStreamEventJSONValid(response) || gjson.GetBytes(response, "id").String() != id {
		err = errors.New("native background terminal identity or JSON is invalid")
		d.Finish("background_unconfirmed", false, err)
		return err
	}
	status := gjson.GetBytes(response, "status").String()
	switch status {
	case "completed":
		valid, healthy := controlledNonstreamSuccess(response, "responses")
		if !valid || !healthy {
			err = errors.New("native background completed response is invalid")
		}
	case "failed", "incomplete", "cancelled", "canceled":
		if err == nil {
			err = fmt.Errorf("native background response ended with %s", status)
		}
	default:
		err = errors.New("native background response has no verified terminal")
		d.Finish("background_unconfirmed", false, err)
		return err
	}
	frame, encodeErr := json.Marshal(map[string]any{"type": "response." + status, "response": json.RawMessage(response)})
	if encodeErr != nil {
		d.Finish("background_unconfirmed", false, encodeErr)
		return encodeErr
	}
	d.ObserveFrame(frame)
	d.Finish(status, true, err)
	return err
}

// NativeBackgroundExecutionContext retains the current account lease's own
// administrative/lease cancellation while the accepted response is retrieved.
func NativeBackgroundExecutionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	r := controlledRequest(ctx)
	if r == nil {
		return ctx
	}
	r.mu.Lock()
	d := r.currentDispatch
	r.mu.Unlock()
	if d == nil {
		return ctx
	}
	d.mu.Lock()
	pending := d.backgroundResponseID != ""
	d.mu.Unlock()
	if pending {
		return d.ctx
	}
	return ctx
}
