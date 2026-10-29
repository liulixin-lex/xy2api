package operatoraccess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

const HeaderMask = "******"
const ErrorDetailHidden = "upstream error details hidden"

var upstreamURL = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.-]*://[^\\s\\\"'<>\\x60]+")
var userinfoURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/?#\s"<>\x60]*@`)

var machineCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// StripURLUserinfo removes credentials from every URL embedded in a string.
func StripURLUserinfo(s string) string {
	return userinfoURL.ReplaceAllStringFunc(s, func(raw string) string {
		start := strings.Index(raw, "://") + 3
		return raw[:start] + "***@"
	})
}
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
				if s != "" && !machineCode.MatchString(s) {
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
		for _, key := range []string{"error", "error_message", "last_error", "temp_unschedulable_reason", "response_text"} {
			if v, ok := obj[key]; ok && v != nil && v != "" {
				if code, ok := v.(string); !ok || !machineCode.MatchString(code) {
					obj[key] = ErrorDetailHidden
				}
			}
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
					stripAddressUserinfo(creds, "base_url", "api_base_urls")
					if !address {
						delete(creds, "base_url")
						delete(creds, "api_base_urls")
					}
				}

				if extra, ok := m["extra"].(map[string]any); ok {
					stripAddressUserinfo(extra, "custom_base_url")
				}
				stripAddressUserinfo(m, "custom_base_url")
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
		case "support_ticket_author":
			walkObjects(v, func(m map[string]any) {
				user, ok := m["user"].(map[string]any)
				if !ok {
					return
				}
				for key := range user {
					switch key {
					case "id", "email", "username", "status", "deleted":
					default:
						delete(user, key)
					}
				}
			})
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
				if body, ok := m["body_override"]; ok && hasJSONValue(body) {
					m["body_override"] = HeaderMask
				}
				if rule == "monitor" {
					stripAddressUserinfo(m, "endpoint")
					if !address {
						delete(m, "endpoint")
					}
				}
			})
			if rule == "monitor" && !address {
				redactMonitorQuotaErrors(dataValue(v))
			}
		case "audit_body":
			walkObjects(v, func(m map[string]any) {
				if _, body := m["request_body"]; !body && m["path"] == nil {
					return
				}
				method, _ := m["method"].(string)
				path, _ := m["path"].(string)
				row, found := Lookup(Key(method, path))
				extra, _ := m["extra"].(map[string]any)
				if !strings.HasPrefix(path, "/api/v1/admin/") || !found || row.Kind == KindSuperOnly {
					m["request_body"] = "<request body hidden>"
					delete(extra, "query")
					return
				}
				hidesCodes := row.Fields == "redeem_search" || row.Fields == "promo_search" || Key(method, path) == "GET /redeem-codes/export"
				for _, redaction := range row.Redact {
					if redaction == "redeem_code" || redaction == "promo_code" {
						hidesCodes = true
					}
				}
				if hidesCodes && !s.Has(FlagRedeemCodesIssue) {
					if extra != nil {
						if _, ok := extra["query"]; ok {
							extra["query"] = "<query hidden>"
						}
					}
				}
				body, _ := m["request_body"].(string)
				parsed, err := decodeJSON([]byte(body))
				if err != nil {
					m["request_body"] = "<request body hidden>"
				} else {
					parsed = redactAuditTree(parsed, address)
					raw, _ := json.Marshal(parsed)
					m["request_body"] = string(raw)
				}
				if q, ok := extra["query"].(string); ok && hideAuditQuery(q, address) {
					extra["query"] = "<query hidden>"
				}
			})
		case "monitor_message":
			if !address {
				data := dataValue(v)
				walkObjects(data, func(m map[string]any) {
					if msg, ok := m["message"].(string); ok && msg != "" {
						if m["status"] == "error" {
							m["message"] = ErrorDetailHidden
						} else {
							m["message"] = ScrubURLs(msg)
						}
					}
				})
				redactMonitorQuotaErrors(data)
			}
		case "ops_upstream_url":
			v = transformStrings(v, StripURLUserinfo)
			redactOpsUserinfoJSON(v)
			if !address && !s.Can(ModuleOps, LevelView) {
				redactOpsURLs(v)
				redactOps(dataValue(v), false)
			}
		case "monitor_v2_errors":
			if !s.Can(ModuleOps, LevelView) && !address {
				walkObjects(dataValue(v), func(m map[string]any) {
					if details, ok := m["details"].([]any); ok {
						for _, item := range details {
							if detail, ok := item.(map[string]any); ok {
								if message, ok := detail["message"].(string); ok && message != "" {
									detail["message"] = ErrorDetailHidden
								}
							}
						}
					}
				})
			}
		case "risk_config_url":
			walkObjects(v, func(m map[string]any) {
				stripAddressUserinfo(m, "base_url")
				if !address {
					delete(m, "base_url")
				}
			})
		default:
			return nil, fmt.Errorf("operatoraccess: unknown redaction %q", rule)
		}
	}
	return json.Marshal(v)
}

func hasJSONValue(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case string:
		return value != ""
	case map[string]any:
		return len(value) > 0
	case []any:
		return len(value) > 0
	default:
		return true
	}
}

func stripAddressUserinfo(m map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			m[key] = transformStrings(value, StripURLUserinfo)
		}
	}
}

func redactOpsUserinfoJSON(v any) {
	walkObjects(v, func(m map[string]any) {
		if raw, ok := m["upstream_errors"].(string); ok {
			parsed, err := decodeJSON([]byte(raw))
			if err == nil {
				parsed = transformStrings(parsed, StripURLUserinfo)
				encoded, _ := json.Marshal(parsed)
				m["upstream_errors"] = string(encoded)
			}
		}
	})
}

func redactMonitorQuotaErrors(data any) {
	walkObjects(data, func(m map[string]any) {
		if _, source := m["source"]; !source {
			if _, success := m["success"]; !success {
				return
			}
		}
		if detail, ok := m["error"].(string); ok && detail != "" {
			m["error"] = ErrorDetailHidden
		}
	})
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
func redactAuditTree(v any, address bool) any {
	walkObjects(v, func(m map[string]any) {
		for k, value := range m {
			if strings.EqualFold(k, "rt") || strings.EqualFold(k, "proxy_url") {
				m[k] = "***"
				continue
			}
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
	return transformStrings(v, func(s string) string {
		s = StripURLUserinfo(s)
		if !address {
			s = ScrubURLs(s)
		}
		return s
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
func dataValue(v any) any {
	if m, ok := v.(map[string]any); ok {
		return m["data"]
	}
	return nil
}
func transformStrings(v any, f func(string) string) any {
	switch x := v.(type) {
	case string:
		return f(x)
	case map[string]any:
		for k, v := range x {
			x[k] = transformStrings(v, f)
		}
	case []any:
		for i, v := range x {
			x[i] = transformStrings(v, f)
		}
	}
	return v
}
func auditAddressKey(k string) bool {
	for _, name := range []string{"base_url", "api_base_urls", "custom_base_url", "endpoint", "upstream_url"} {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}
func hideAuditQuery(raw string, address bool) bool {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return true
	}
	for k, values := range q {
		if !address && auditAddressKey(k) {
			return true
		}
		for _, value := range values {
			if i := strings.Index(value, "://"); i >= 0 {
				if !address || strings.Contains(value[i+3:], "@") {
					return true
				}
			}
		}
	}
	return false
}
func redactOps(v any, event bool) {
	walkObjects(v, func(m map[string]any) {
		delete(m, "upstream_url")
		_, kind := m["kind"]
		_, status := m["upstream_status_code"]
		keys := []string{"upstream_error_message", "upstream_error_detail", "upstream_response_body"}
		if event || kind || status {
			keys = append(keys, "message", "detail")
		}
		for _, key := range keys {
			if s, ok := m[key].(string); ok && s != "" {
				m[key] = ErrorDetailHidden
			}
		}
		if raw, ok := m["upstream_errors"].(string); ok {
			parsed, err := decodeJSON([]byte(raw))
			if err != nil {
				m["upstream_errors"] = "[]"
			} else {
				redactOps(parsed, true)
				b, _ := json.Marshal(parsed)
				m["upstream_errors"] = string(b)
			}
		}
	})
}

// URL removal also covers legacy non-envelope response shapes. Message replacement
// remains limited to data and serialized upstream events.
func redactOpsURLs(v any) {
	walkObjects(v, func(m map[string]any) {
		delete(m, "upstream_url")
		if raw, ok := m["upstream_errors"].(string); ok {
			parsed, err := decodeJSON([]byte(raw))
			if err != nil {
				m["upstream_errors"] = "[]"
			} else {
				redactOps(parsed, true)
				encoded, _ := json.Marshal(parsed)
				m["upstream_errors"] = string(encoded)
			}
		}
	})
}
