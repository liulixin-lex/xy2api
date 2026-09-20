package service

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/statestream"
	"github.com/tidwall/gjson"
)

const codexTicketResponseLimit = 2 << 20

// Validation is deliberately independent from IQ grading. A completed model
// response proves routing only; it does not establish the quality of an answer.
func codexTicketResponseModel(body io.Reader, encoding string) (string, error) {
	if body == nil {
		return "", errors.New("missing response")
	}
	actual := ""
	completed := false
	terminalID := ""
	parse := func(data []byte, event string) error {
		data = bytes.TrimSpace(data)
		if len(data) == 0 {
			return nil
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			if !completed {
				return errors.New("missing completed response")
			}
			return nil
		}
		if e := iqcheck.ValidateResponseEvent(data); e != nil {
			return errors.New("invalid response event")
		}
		root := gjson.ParseBytes(data)
		typ := root.Get("type").String()
		if typ != "" && event != "" && typ != event {
			return errors.New("conflicting event type")
		}
		if typ == "" {
			typ = event
		}
		if typ == "error" || typ == "response.failed" || typ == "response.incomplete" || (root.Get("error").Exists() && root.Get("error").Type != gjson.Null) {
			return classifyCodexTicketResponseFailure(root)
		}
		response := root
		if typ == "response.completed" || typ == "response.done" {
			response = root.Get("response")
		} else if typ != "" || root.Get("object").String() != "response" {
			return nil
		}
		if response.Get("error").Exists() && response.Get("error").Type != gjson.Null {
			return classifyCodexTicketResponseFailure(root)
		}
		status := response.Get("status").String()
		if status == "failed" || status == "incomplete" {
			return classifyCodexTicketResponseFailure(root)
		}
		if status != "completed" && (typ != "response.completed" || status != "") {
			return errors.New("incomplete response")
		}
		model := response.Get("model")
		id := response.Get("id").String()
		if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
			return errors.New("missing response model")
		}
		if completed && (actual != model.String() || terminalID != id) {
			return errors.New("conflicting completion")
		}
		completed = true
		actual = model.String()
		terminalID = id
		return nil
	}
	if e := statestream.Walk(body, encoding, parse); e != nil {
		return "", e
	}

	if !completed {
		return "", errors.New("response did not complete")
	}
	return actual, nil
}
func validateCodexTicketCompletedModel(body io.Reader, model string) error {
	actual, e := codexTicketResponseModel(body, "")
	if e != nil {
		return e
	}
	if actual != model {
		return errors.New("returned model differs from requested model")
	}
	return nil
}

// Codes are allowlisted; upstream messages and unknown codes never reach diagnostics.
type codexTicketResponseFailure struct {
	reason, code string
	account      bool
}

func (e *codexTicketResponseFailure) Error() string { return e.reason }
func classifyCodexTicketResponseFailure(root gjson.Result) error {
	code := root.Get("response.error.code").String()
	if code == "" {
		code = root.Get("error.code").String()
	}
	if code == "" {
		code = root.Get("code").String()
	}
	f := &codexTicketResponseFailure{reason: "response_failed"}
	switch code {
	case "rate_limit_exceeded", "insufficient_quota", "usage_limit_reached":
		f.reason, f.code, f.account = "rate_limited", code, true
	case "invalid_api_key", "authentication_error", "permission_denied", "account_deactivated":
		f.reason, f.code, f.account = "auth_rejected", code, true
	case "server_is_overloaded", "slow_down":
		f.reason, f.code = "model_capacity", code
	}
	return f
}
