//go:build unit

package operatoraccess

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func redactTest(t *testing.T, body string, rules []Redaction, flags ...Flag) map[string]any {
	t.Helper()
	sc := &Scope{Perms: Permissions{Flags: map[Flag]bool{}}}
	for _, f := range flags {
		sc.Perms.Flags[f] = true
	}
	raw, err := Redact([]byte(body), rules, sc)
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))
	return obj
}

const testAccount = `{"platform":"openai","type":"apikey","credentials":{"base_url":"https://relay.example","api_base_urls":["https://relay.example/v1"],"api_key":"stored-key","header_overrides":{"X-Relay-Key":"stored-header"}},"extra":{"custom_base_url":"https://custom.example","custom_base_url_enabled":true,"other":1},"custom_base_url":"https://custom.example","custom_base_url_enabled":true}`

func TestRedactAccountStripsAddressWithoutFlag(t *testing.T) {
	obj := redactTest(t, testAccount, []Redaction{"account"})
	cred := obj["credentials"].(map[string]any)
	require.NotContains(t, cred, "base_url")
	require.NotContains(t, cred, "api_base_urls")
	require.Equal(t, "stored-key", cred["api_key"])
	extra := obj["extra"].(map[string]any)
	require.NotContains(t, extra, "custom_base_url")
	require.NotContains(t, extra, "custom_base_url_enabled")
	require.Equal(t, float64(1), extra["other"])
	obj = redactTest(t, testAccount, []Redaction{"account"}, FlagAccountsUpstreamAddress)
	require.Equal(t, "https://relay.example", obj["credentials"].(map[string]any)["base_url"])
}
func TestRedactAccountDeletesTopLevelCustomBaseURL(t *testing.T) {
	for _, body := range []string{testAccount, `{"platform":"openai","type":"apikey","custom_base_url":"https://relay.example","custom_base_url_enabled":true}`} {
		obj := redactTest(t, body, []Redaction{"account"})
		require.NotContains(t, obj, "custom_base_url")
		require.NotContains(t, obj, "custom_base_url_enabled")
	}
	obj := redactTest(t, `{"custom_base_url":"ordinary non-account"}`, []Redaction{"account"})
	require.Equal(t, "ordinary non-account", obj["custom_base_url"])
}
func TestRedactAccountMasksHeaderOverridesEvenWithFlag(t *testing.T) {
	for _, flags := range [][]Flag{nil, {FlagAccountsUpstreamAddress}} {
		obj := redactTest(t, testAccount, []Redaction{"account"}, flags...)
		require.Equal(t, HeaderMask, obj["credentials"].(map[string]any)["header_overrides"].(map[string]any)["X-Relay-Key"])
	}
}
func TestRedactAccountScrubsURLsAtAnyDepthAnd5xx(t *testing.T) {
	obj4xx := redactTest(t, `{"code":400,"message":"lookup relay.example: no such host"}`, []Redaction{"account"})
	require.Equal(t, ErrorDetailHidden, obj4xx["message"])

	body := `{"data":[{"error_message":"lookup relay.example: no such host","message":"visit https://relay.example/x","reason":"https://relay.example/x"}],"error":"dial tcp 192.0.2.3:443 refused","reason":"UPSTREAM_ERROR"}`
	obj := redactTest(t, body, []Redaction{"account"})
	row := obj["data"].([]any)[0].(map[string]any)
	require.Equal(t, ErrorDetailHidden, row["error_message"])
	require.Equal(t, "visit [upstream]", row["message"])
	require.Equal(t, "[upstream]", row["reason"])
	require.Equal(t, "UPSTREAM_ERROR", obj["reason"])
	raw, err := ScrubErrorStrings([]byte(`{"code":503,"message":"TLS relay.example","data":{"base_url":"https://relay.example"}}`), 503)
	require.NoError(t, err)
	require.Contains(t, string(raw), ErrorDetailHidden)
	require.Contains(t, string(raw), "https://relay.example")
	obj = redactTest(t, body, []Redaction{"account"}, FlagAccountsUpstreamAddress)
	require.Equal(t, "dial tcp 192.0.2.3:443 refused", obj["error"])
}
func TestScrubErrorStringsHidesErrorDetail(t *testing.T) {
	body := `{"error":"tls relay.example","error_message":"relay.example","nested":[{"last_error":"192.0.2.1:443","temp_unschedulable_reason":"refused","response_text":"cert.example","message":"visit https://relay.example/path","reason":"UPSTREAM_ERROR"}],"message":"top relay.example"}`
	for _, status := range []int{200, 400, 503} {
		raw, err := ScrubErrorStrings([]byte(body), status)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "cert.example")
		require.NotContains(t, string(raw), "192.0.2.1")
		require.NotContains(t, string(raw), "tls relay.example")
		require.Contains(t, string(raw), "UPSTREAM_ERROR")
		var obj map[string]any
		require.NoError(t, json.Unmarshal(raw, &obj))
		if status >= 400 {
			require.Equal(t, ErrorDetailHidden, obj["message"])
		} else {
			require.Equal(t, "top relay.example", obj["message"])
		}
	}
	_, err := ScrubErrorStrings([]byte("invalid"), 500)
	require.Error(t, err)
}
func TestRedactEventLine(t *testing.T) {
	for _, detail := range []string{"lookup relay.example: no such host", "x509: certificate valid for cert.example", "dial tcp 192.0.2.3:443 refused", "https://relay.example/path"} {
		payload, _ := json.Marshal(map[string]any{"error": detail, "message": "https://other.example/x", "number": 123})
		out := RedactEventLine(append(append([]byte("data: "), payload...), '\n'))
		require.NotContains(t, string(out), detail)
		require.Contains(t, string(out), ErrorDetailHidden)
		require.Contains(t, string(out), "[upstream]")
		require.True(t, strings.HasSuffix(string(out), "\n"))
	}
	for _, line := range []string{"data: bad https://relay.example/x", "event: https://relay.example/x", "data: [\"https://relay.example\"]"} {
		out := RedactEventLine([]byte(line))
		require.NotContains(t, string(out), "relay.example")
		require.Contains(t, string(out), "[upstream]")
	}
	require.Equal(t, "data: [DONE]", string(RedactEventLine([]byte("data: [DONE]"))))
	require.NotPanics(t, func() { RedactEventLine([]byte(`data: {"error":{"message":"https://relay.example"}}`)) })
}
func TestRedactMonitorMasksEveryHeaderValue(t *testing.T) {
	body := `{"endpoint":"https://relay.example","extra_headers":{"Authorization":"secret","X-Relay-Admission":"secret","nested":{"x":"secret"}}}`
	for _, rule := range []Redaction{"monitor", "monitor_template"} {
		for _, flags := range [][]Flag{nil, {FlagAccountsUpstreamAddress}} {
			obj := redactTest(t, body, []Redaction{rule}, flags...)
			for _, value := range obj["extra_headers"].(map[string]any) {
				require.Equal(t, HeaderMask, value)
			}
			if rule == "monitor" && len(flags) == 0 {
				require.NotContains(t, obj, "endpoint")
			} else {
				require.Equal(t, "https://relay.example", obj["endpoint"])
			}
		}
	}
}
func TestRedactAPIKeyRedeemPromoAndOrderCodes(t *testing.T) {
	obj := redactTest(t, `{"key":"abcdefghijklmnop","user_id":4,"other":{"key":"not-an-apikey"},"password":"secret","rows":[{"code":"ABCDEFGH"}],"recharge_code":"recharge","detail":"{\"rechargeCode\":\"another\"}"}`, []Redaction{"api_key", "redeem_code", "promo_code", "order_code", "proxy_password"})
	require.Equal(t, "abcdef****mnop", obj["key"])
	require.Equal(t, "not-an-apikey", obj["other"].(map[string]any)["key"])
	require.NotContains(t, obj, "password")
	require.Equal(t, "****", obj["recharge_code"])
	require.JSONEq(t, `{"rechargeCode":"****"}`, obj["detail"].(string))
	require.Equal(t, "AB****GH", obj["rows"].([]any)[0].(map[string]any)["code"])
	obj = redactTest(t, `{"code":"ABCDEFGH","recharge_code":"keep"}`, []Redaction{"redeem_code", "promo_code", "order_code"}, FlagRedeemCodesIssue, FlagOrdersRefund)
	require.Equal(t, "ABCDEFGH", obj["code"])
	require.Equal(t, "keep", obj["recharge_code"])
}
func TestRedactAuditBodyScrubsAddresses(t *testing.T) {
	body := `{"base_url":"https://relay.example","nested":[{"custom_base_url":"https://relay.example","api_base_urls":["https://relay.example"]}],"endpoint":"https://relay.example","upstream_url":"https://relay.example"}`
	raw, _ := json.Marshal(map[string]any{"request_body": body, "extra": map[string]any{"query": "endpoint=https://relay.example/path"}})
	obj := redactTest(t, string(raw), []Redaction{"audit_body"})
	require.NotContains(t, obj["request_body"], "relay.example")
	require.NotContains(t, obj["extra"].(map[string]any)["query"], "relay.example")
	obj = redactTest(t, string(raw), []Redaction{"audit_body"}, FlagAccountsUpstreamAddress)
	require.Contains(t, obj["request_body"], "relay.example")
}
func TestRedactAuditBodyMasksHeaderMapsAlways(t *testing.T) {
	body := `{"extra_headers":{"X-Relay":"secret"},"Extra_Headers":{"Alias":"secret"},"HEADER_OVERRIDES":{"Header":"secret"},"nested":[{"Body_Override":{"x":"secret"},"Base_URL":"https://relay.example","Custom_Base_URL":"https://relay.example"}]}`
	raw, _ := json.Marshal(map[string]any{"request_body": body})
	for _, flags := range [][]Flag{nil, {FlagAccountsUpstreamAddress}} {
		obj := redactTest(t, string(raw), []Redaction{"audit_body"}, flags...)
		require.NotContains(t, obj["request_body"], "secret")
		require.Contains(t, obj["request_body"], "***")
		if len(flags) == 0 {
			require.NotContains(t, obj["request_body"], "relay.example")
		}
	}
}
func TestRedactAuditBodyHidesUnparseableBody(t *testing.T) {
	for _, body := range []string{"invalid secret", "{\"extra_headers\":...<truncated>", "{} {}"} {
		raw, _ := json.Marshal(map[string]any{"request_body": body})
		for _, flags := range [][]Flag{nil, {FlagAccountsUpstreamAddress}} {
			obj := redactTest(t, string(raw), []Redaction{"audit_body"}, flags...)
			require.Equal(t, "<request body hidden>", obj["request_body"])
		}
	}
}
func TestRedactOpsUpstreamURLOnlyForUsageCallers(t *testing.T) {
	body := []byte(`{"upstream_url":"https://relay.example","upstream_errors":"[{\"upstream_url\":\"https://relay.example\",\"status\":503}]"}`)
	for _, sc := range []*Scope{nil, {Perms: Permissions{Modules: map[Module]Level{ModuleOps: LevelView}}}, {Perms: Permissions{Flags: map[Flag]bool{FlagAccountsUpstreamAddress: true}}}} {
		raw, err := Redact(body, []Redaction{"ops_upstream_url"}, sc)
		require.NoError(t, err)
		if sc == nil {
			require.NotContains(t, string(raw), "upstream_url")
		} else {
			require.Contains(t, string(raw), "upstream_url")
		}
	}
}
func TestRedactKeepsNumbersExact(t *testing.T) {
	raw, err := Redact([]byte(`{"id":9223372036854775807,"decimal":123.456789012345678901,"exp":1.234e+90}`), nil, nil)
	require.NoError(t, err)
	require.Contains(t, string(raw), "9223372036854775807")
	require.Contains(t, string(raw), "123.456789012345678901")
	require.Contains(t, string(raw), "1.234e+90")
	_, err = Redact([]byte("bad"), nil, nil)
	require.Error(t, err)
}
func TestMask(t *testing.T) {
	for input, want := range map[string]string{"": "****", "1234567": "****", "12345678": "12****78", "123456789012345": "12****45", "1234567890123456": "123456****3456", "密钥密钥密钥密钥": "密钥****密钥"} {
		require.Equal(t, want, Mask(input))
	}
}
