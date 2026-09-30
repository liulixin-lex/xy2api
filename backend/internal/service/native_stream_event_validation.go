package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

var errNativeStreamEventJSON = errors.New("invalid or ambiguous upstream protocol event JSON")

// All protocol consumers must agree on identity, type, sequence and terminal
// state. Reject duplicate keys, including escaped aliases, before gjson's
// first-key and encoding/json's last-key rules can disagree. Iterating raw
// values avoids allocating/copying long text and image strings.
func nativeStreamEventJSONValid(frame []byte) bool {
	if !gjson.ValidBytes(frame) {
		return false
	}
	return nativeStreamEventJSONUniqueKeys(gjson.ParseBytes(frame))
}

// SSE scanners already own immutable strings. Reuse them instead of copying
// an entire text/image event through []byte and ParseBytes just for validation.
func nativeStreamEventJSONStringValid(frame string) bool {
	if !gjson.Valid(frame) {
		return false
	}
	return nativeStreamEventJSONUniqueKeys(gjson.Parse(frame))
}

func nativeStreamEventJSONUniqueKeys(root gjson.Result) bool {
	if !root.IsObject() {
		return false
	}
	queue := []gjson.Result{root}
	for len(queue) > 0 {
		value := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		unique := true
		if value.IsObject() {
			seen := make(map[string]struct{})
			value.ForEach(func(key, item gjson.Result) bool {
				name := key.String()
				if _, exists := seen[name]; exists {
					unique = false
					return false
				}
				seen[name] = struct{}{}
				if item.IsObject() || item.IsArray() {
					queue = append(queue, item)
				}
				return true
			})
		} else if value.IsArray() {
			value.ForEach(func(_, item gjson.Result) bool {
				if item.IsObject() || item.IsArray() {
					queue = append(queue, item)
				}
				return true
			})
		}
		if !unique {
			return false
		}
	}
	return true
}

// IsNativeResponsesTerminalError identifies only a verified native terminal
// whose complete frame has already been delivered by the adapter. A bare DONE,
// contradictory status or failed downstream write never receives this marker.
func IsNativeResponsesTerminalError(err error) bool {
	var terminal *nativeResponsesDeliveredTerminalError
	return errors.As(err, &terminal) && terminal.delivered
}

type nativeResponsesDeliveredTerminalError struct {
	status    string
	delivered bool
}

func (e *nativeResponsesDeliveredTerminalError) Error() string {
	return "upstream response ended with " + e.status
}

// nativeResponsesTerminalEvent preserves a validated non-success terminal
// independently of whether the downstream received its complete frame.
func nativeResponsesTerminalEvent(err error) string {
	var terminal *nativeResponsesDeliveredTerminalError
	if errors.As(err, &terminal) {
		return "response." + terminal.status
	}
	return ""
}

func markNativeResponsesTerminalDelivered(err error, delivered bool) error {
	var terminal *nativeResponsesDeliveredTerminalError
	if delivered && errors.As(err, &terminal) {
		return &nativeResponsesDeliveredTerminalError{status: terminal.status, delivered: true}
	}
	return err
}

func nativeResponsesTerminalError(frame []byte, eventType string) error {
	if bytes.Equal(bytes.TrimSpace(frame), []byte("[DONE]")) {
		return fmt.Errorf("response terminal was not verified: %w", io.ErrUnexpectedEOF)
	}
	switch eventType {
	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "response.completed", "response.done":
	default:
		return nil
	}
	response := gjson.GetBytes(frame, "response")
	status := strings.TrimSpace(response.Get("status").String())
	switch eventType {
	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		expected := strings.TrimPrefix(eventType, "response.")
		equivalent := status == expected || ((status == "cancelled" || status == "canceled") && (expected == "cancelled" || expected == "canceled"))
		if !response.IsObject() || !equivalent {
			return fmt.Errorf("unverified upstream terminal %s with status %s", eventType, status)
		}
		return &nativeResponsesDeliveredTerminalError{status: status}
	case "response.completed", "response.done":
		if status != "" && status != "completed" {
			return fmt.Errorf("inconsistent upstream terminal status: %s", status)
		}
	}
	return nil
}
