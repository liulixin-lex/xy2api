package operatoraccess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const HeaderMask = "******"
const ErrorDetailHidden = "upstream error details hidden"

var upstreamURL = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.-]*://[^\\s\\\"'<>\\x60]+")

func ScrubURLs(s string) string { return upstreamURL.ReplaceAllString(s, "[upstream]") }
func Mask(v string) string {
	r := []rune(v)
	switch {
	case len(r) >= 16:
		return string(r[:6]) + "****" + string(r[len(r)-4:])
	case len(r) >= 8:
		return string(r[:2]) + "****" + string(r[len(r)-2:])
	default:
		return "****"
	}
}

func decodeJSON(body []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("operatoraccess: trailing JSON")
	}
	return v, nil
}

// walkObjects visits every object, including objects nested in arrays.
func walkObjects(v any, visit func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		visit(x)
		for _, child := range x {
			walkObjects(child, visit)
		}
	case []any:
		for _, child := range x {
			walkObjects(child, visit)
		}
	}
}
func hasKeys(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}
func maskHeaderValues(v any, mask string) {
	if m, ok := v.(map[string]any); ok {
		for k := range m {
			m[k] = mask
		}
	}
}
func scrubStrings(v any) any {
	switch x := v.(type) {
	case string:
		return ScrubURLs(x)
	case map[string]any:
		for k, v := range x {
			x[k] = scrubStrings(v)
		}
	case []any:
		for i, v := range x {
			x[i] = scrubStrings(v)
		}
	}
	return v
}
func scrubErrorValues(v any, status int) {
	walkObjects(v, func(m map[string]any) {
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				continue
			}
			switch k {
			case "error", "error_message", "last_error", "temp_unschedulable_reason", "response_text":
				if s != "" {
					m[k] = ErrorDetailHidden
				}
			case "message", "reason":
				m[k] = ScrubURLs(s)
			}
		}
	})
	if m, ok := v.(map[string]any); ok && status >= 400 {
		if s, ok := m["message"].(string); ok && s != "" {
			m["message"] = ErrorDetailHidden
		}
	}
}
func ScrubErrorStrings(body []byte, status int) ([]byte, error) {
	v, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	scrubErrorValues(v, status)
	return json.Marshal(v)
}

// RedactEventLine processes one line without buffering the surrounding stream.
func RedactEventLine(line []byte) []byte {
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return []byte(ScrubURLs(string(line)))
	}
	payload := bytes.TrimSpace(line[len("data: "):])
	v, err := decodeJSON(payload)
	m, ok := v.(map[string]any)
	if err != nil || !ok {
		return []byte(ScrubURLs(string(line)))
	}
	scrubStrings(m)
	walkObjects(m, func(obj map[string]any) {
		if v, ok := obj["error"]; ok && v != nil && v != "" {
			obj["error"] = ErrorDetailHidden
		}
	})
	out, err := json.Marshal(m)
	if err != nil {
		return []byte("data: {}")
	}
	suffix := []byte{}
	if bytes.HasSuffix(line, []byte("\r\n")) {
		suffix = []byte("\r\n")
	} else if bytes.HasSuffix(line, []byte("\n")) {
		suffix = []byte("\n")
	}
	return append(append([]byte("data: "), out...), suffix...)
}

// Redact applies only the approved row's transforms. Numeric text is preserved.
func Redact(body []byte, rules []Redaction, s *Scope) ([]byte, error) {
	v, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	address := s.Has(FlagAccountsUpstreamAddress)
	for _, rule := range rules {
		switch rule {
		case "account":
			walkObjects(v, func(m map[string]any) {
				if !hasKeys(m, "platform", "type") {
					return
				}
				_, cred := m["credentials"]
				_, extra := m["extra"]
				_, custom := m["custom_base_url"]
				if !cred && !extra && !custom {
					return
				}
				if creds, ok := m["credentials"].(map[string]any); ok {
					maskHeaderValues(creds["header_overrides"], HeaderMask)
					if !address {
						delete(creds, "base_url")
						delete(creds, "api_base_urls")
					}
				}
				if !address {
					if extra, ok := m["extra"].(map[string]any); ok {
						delete(extra, "custom_base_url")
						delete(extra, "custom_base_url_enabled")
					}
					delete(m, "custom_base_url")
					delete(m, "custom_base_url_enabled")
				}
			})
			if !address {
				// The standard response envelope carries the HTTP error code.
				// Writers with an HTTP status also use ScrubErrorStrings directly.
				status := 0
				if obj, ok := v.(map[string]any); ok {
					if code, ok := obj["code"].(json.Number); ok {
						if n, err := code.Int64(); err == nil && n >= 400 && n <= 599 {
							status = int(n)
						}
					}
				}
				scrubErrorValues(v, status)
			}
		case "api_key":
			walkObjects(v, func(m map[string]any) {
				if key, ok := m["key"].(string); ok {
					if _, ok := m["user_id"].(json.Number); ok {
						m["key"] = Mask(key)
					}
				}
			})
		case "redeem_code", "promo_code":
			if !s.Has(FlagRedeemCodesIssue) {
				walkObjects(v, func(m map[string]any) {
					if code, ok := m["code"].(string); ok {
						m["code"] = Mask(code)
					}
				})
			}
		case "order_code":
			if !s.Has(FlagOrdersRefund) {
				redactOrder(v)
			}
		case "proxy_password":
			walkObjects(v, func(m map[string]any) { delete(m, "password") })
		case "monitor", "monitor_template":
			walkObjects(v, func(m map[string]any) {
				maskHeaderValues(m["extra_headers"], HeaderMask)
				if rule == "monitor" && !address {
					delete(m, "endpoint")
				}
			})
		case "audit_body":
			walkObjects(v, func(m map[string]any) {
				if body, ok := m["request_body"].(string); ok {
					parsed, err := decodeJSON([]byte(body))
					if err != nil {
						m["request_body"] = "<request body hidden>"
					} else {
						redactAuditTree(parsed, address)
						raw, _ := json.Marshal(parsed)
						m["request_body"] = string(raw)
					}
				}
				if !address {
					if extra, ok := m["extra"].(map[string]any); ok {
						if q, ok := extra["query"]; ok {
							extra["query"] = scrubStrings(q)
						}
					}
				}
			})
		case "ops_upstream_url":
			if !address && !s.Can(ModuleOps, LevelView) {
				redactOps(v)
			}
		default:
			return nil, fmt.Errorf("operatoraccess: unknown redaction %q", rule)
		}
	}
	return json.Marshal(v)
}
func redactOrder(v any) {
	walkObjects(v, func(m map[string]any) {
		for _, key := range []string{"recharge_code", "rechargeCode"} {
			if _, ok := m[key]; ok {
				m[key] = "****"
			}
		}
		if detail, ok := m["detail"].(string); ok {
			if parsed, err := decodeJSON([]byte(detail)); err == nil {
				redactOrder(parsed)
				raw, _ := json.Marshal(parsed)
				m["detail"] = string(raw)
			}
		}
	})
}
func redactAuditTree(v any, address bool) {
	walkObjects(v, func(m map[string]any) {
		for k, value := range m {
			header := false
			for _, key := range []string{"header_overrides", "extra_headers", "body_override"} {
				if strings.EqualFold(k, key) {
					header = true
					break
				}
			}
			if header {
				m[k] = maskAuditValues(value)
				continue
			}
			if !address {
				for _, key := range []string{"base_url", "api_base_urls", "custom_base_url", "endpoint", "upstream_url"} {
					if strings.EqualFold(k, key) {
						m[k] = "***"
						break
					}
				}
			}
		}
	})
}
func maskAuditValues(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			x[k] = maskAuditValues(v)
		}
		return x
	case []any:
		for i, v := range x {
			x[i] = maskAuditValues(v)
		}
		return x
	default:
		return "***"
	}
}
func redactOps(v any) {
	walkObjects(v, func(m map[string]any) {
		delete(m, "upstream_url")
		if raw, ok := m["upstream_errors"].(string); ok {
			parsed, err := decodeJSON([]byte(raw))
			if err != nil {
				m["upstream_errors"] = "[]"
			} else {
				redactOps(parsed)
				b, _ := json.Marshal(parsed)
				m["upstream_errors"] = string(b)
			}
		}
	})
}
