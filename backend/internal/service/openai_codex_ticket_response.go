package service

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/tidwall/gjson"
)

const codexTicketResponseLimit = 2 << 20

// Validation is deliberately independent from IQ grading. A completed model
// response proves routing only; it does not establish the quality of an answer.
func codexTicketResponseModel(body io.Reader, encoding string) (string, error) {
	if body == nil {
		return "", errors.New("missing response")
	}
	bounded := io.LimitReader(body, codexTicketResponseLimit+1)
	reader := bounded
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
	case "gzip":
		r, e := gzip.NewReader(bounded)
		if e != nil {
			return "", e
		}
		defer func() { _ = r.Close() }()
		reader = r
	case "deflate":
		r, e := zlib.NewReader(bounded)
		if e != nil {
			return "", e
		}
		defer func() { _ = r.Close() }()
		reader = r
	case "br":
		reader = brotli.NewReader(bounded)
	case "zstd":
		r, e := zstd.NewReader(bounded, zstd.WithDecoderMaxMemory(8<<20), zstd.WithDecoderConcurrency(1))
		if e != nil {
			return "", e
		}
		defer r.Close()
		reader = r
	default:
		return "", errors.New("unsupported content encoding")
	}
	raw, e := io.ReadAll(io.LimitReader(reader, codexTicketResponseLimit+1))
	if e != nil || len(raw) > codexTicketResponseLimit {
		return "", errors.New("incomplete or oversized response")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", errors.New("empty response")
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
	if raw[0] == '{' {
		if e := parse(raw, ""); e != nil {
			return "", e
		}
	} else {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		var data []byte
		event := ""
		flush := func() error { e := parse(data, event); data = nil; event = ""; return e }
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if e := flush(); e != nil {
					return "", e
				}
				continue
			}
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(line[6:])
			}
			if !strings.HasPrefix(line, "data:") && !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, "id:") && !strings.HasPrefix(line, "retry:") && !strings.HasPrefix(line, ":") {
				return "", errors.New("invalid SSE line")
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(line[5:], " ")...)
				data = append(data, '\n')
				if len(data) > 1<<20 {
					return "", errors.New("oversized event")
				}
			}
		}
		if scanner.Err() != nil {
			return "", errors.New("invalid response stream")
		}
		if e := flush(); e != nil {
			return "", e
		}
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
