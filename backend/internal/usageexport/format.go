package usageexport

import (
	"encoding/json"
	"fmt"
	"github.com/shopspring/decimal"
	"strconv"
	"strings"
	"sync"
	"time"
)

var csvHeaders = []string{"Time", "API Key Name", "Model", "Reasoning Effort", "Inbound Endpoint", "IP Address", "Type", "Billing Mode", "Input Tokens", "Output Tokens", "Cache Read Tokens", "Cache Creation Tokens", "Rate Multiplier", "Billed Cost", "Original Cost", "First Token (ms)", "Duration (ms)"}
var xlsxHeaders = []string{"Time", "User", "API Key", "Account", "Requested", "Sent upstream", "Upstream response", "Response model mismatch", "Requested reasoning effort", "Reasoning Effort", "Group", "Inbound Endpoint", "Upstream Endpoint", "Type", "Input Tokens", "Output Tokens", "Cache Read Tokens", "Cache Creation Tokens", "Input Cost", "Output Cost", "Cache Read Cost", "Cache Creation Cost", "Rate", "Account rate", "Original", "User billed", "Account billed", "First Token", "Duration", "Request ID", "Upstream ID", "User-Agent", "IP"}
var xlsxHeadersZH = []string{"时间", "用户", "API 密钥", "账户", "请求", "发往上游", "上游响应", "上游响应模型不一致", "请求推理强度", "推理强度", "分组", "入站端点", "上游端点", "类型", "输入 Token", "输出 Token", "缓存读取 Token", "缓存创建 Token", "输入费用", "输出费用", "缓存读取费用", "缓存创建费用", "倍率", "账号倍率", "原始", "用户扣费", "账号计费", "首 Token", "耗时", "请求ID", "上游ID", "User-Agent", "IP"}

func Headers(o Options) []string {
	if o.Format == "csv" {
		return csvHeaders
	}
	if strings.HasPrefix(o.Language, "zh") {
		return xlsxHeadersZH
	}
	return xlsxHeaders
}
func numericColumn(o Options, i int) bool {
	if o.Format == "csv" {
		return i >= 8
	}
	return i >= 14 && i <= 28
}

type Record map[string]json.RawMessage

func (r Record) s(k string) string {
	b := r[k]
	if len(b) == 0 || string(b) == "null" {
		return ""
	}
	if b[0] == '"' {
		var s string
		_ = json.Unmarshal(b, &s)
		return s
	}
	return string(b)
}
func (r Record) n(k string) decimal.Decimal {
	v, err := decimal.NewFromString(r.s(k))
	if err != nil {
		return decimal.Zero
	}
	return v
}
func effort(s string) string {
	s = strings.TrimSpace(s)
	normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(s))
	if value, ok := map[string]string{"": "-", "none": "-", "minimal": "-", "low": "Low", "medium": "Medium", "high": "High", "xhigh": "XHigh", "extrahigh": "XHigh", "max": "Max"}[normalized]; ok {
		return value
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var exportLocations sync.Map

func exportLocation(name string) (*time.Location, error) {
	if v, ok := exportLocations.Load(name); ok {
		if loc, ok := v.(*time.Location); ok {
			return loc, nil
		}
	}
	v, err := time.LoadLocation(name)
	if err == nil {
		exportLocations.Store(name, v)
	}
	return v, err
}
func Project(r Record, o Options) ([]string, error) {
	loc, err := exportLocation(o.Timezone)
	if err != nil {
		return nil, err
	}
	stamp, err := time.Parse(time.RFC3339Nano, r.s("created_at"))
	if err != nil {
		return nil, fmt.Errorf("export timestamp: %w", err)
	}
	when := stamp.In(loc).Format(time.RFC3339Nano)
	model := strings.TrimSpace(r.s("requested_model"))
	if model == "" {
		model = r.s("model")
	}
	requested := strings.TrimSpace(r.s("requested_reasoning_effort"))
	if requested == "" {
		requested = r.s("reasoning_effort")
	}
	typ := r.s("request_type")
	if typ == "" || typ == "0" {
		if r.s("openai_ws_mode") == "true" {
			typ = "3"
		} else if r.s("stream") == "true" {
			typ = "2"
		} else {
			typ = "1"
		}
	}
	kind := map[string]string{"1": "Sync", "2": "Stream", "3": "WS", "4": "Cyber", "5": "Live"}[typ]
	if kind == "" {
		kind = "Unknown"
	}
	mode := r.s("billing_mode")
	if mode == "" && r.n("image_count").GreaterThan(decimal.Zero) {
		mode = "image"
	}
	billing := map[string]string{"image": "Image", "video": "Video", "per_request": "Per Request"}[mode]
	if billing == "" {
		billing = "Token"
	}
	zh := strings.HasPrefix(o.Language, "zh")
	if zh {
		billing = map[string]string{"Image": "按次(图片)", "Video": "按次(视频)", "Per Request": "按次", "Token": "按量"}[billing]
	}
	if o.Format == "csv" {
		return []string{when, r.s("key_name"), model, effort(requested), r.s("inbound_endpoint"), r.s("ip_address"), kind, billing, r.s("input_tokens"), r.s("output_tokens"), r.s("cache_read_tokens"), r.s("cache_creation_tokens"), r.s("rate_multiplier"), r.n("actual_cost").StringFixed(8), r.n("total_cost").StringFixed(8), r.s("first_token_ms"), r.s("duration_ms")}, nil
	}
	upstream := r.s("upstream_model")
	if upstream == "" {
		upstream = model
	}
	mismatch := r.s("upstream_model_mismatch")
	if mismatch != "" {
		if mismatch == "true" {
			mismatch = "Yes"
			if zh {
				mismatch = "是"
			}
		} else {
			mismatch = "No"
			if zh {
				mismatch = "否"
			}
		}
	}
	if zh {
		if typ == "1" {
			kind = "同步"
		}
		if typ == "2" {
			kind = "流式"
		}
		if typ == "4" {
			kind = "安全策略"
		}
		if kind == "Unknown" {
			kind = "未知"
		}
	}
	multiplier := r.n("account_rate_multiplier")
	if r.s("account_rate_multiplier") == "" {
		multiplier = decimal.NewFromInt(1)
	}
	accountCost := r.n("account_stats_cost")
	if r.s("account_stats_cost") == "" {
		accountCost = r.n("total_cost")
	}
	rate := "1.00"
	if r.s("rate_multiplier") != "" {
		rate = fourSignificant(r.n("rate_multiplier"))
	}
	return []string{when, r.s("user_email"), r.s("key_name"), r.s("account_name"), model, upstream, r.s("upstream_response_model"), mismatch, effort(requested), effort(r.s("reasoning_effort")), r.s("group_name"), r.s("inbound_endpoint"), r.s("upstream_endpoint"), kind, r.s("input_tokens"), r.s("output_tokens"), r.s("cache_read_tokens"), r.s("cache_creation_tokens"), r.n("input_cost").StringFixed(6), r.n("output_cost").StringFixed(6), r.n("cache_read_cost").StringFixed(6), r.n("cache_creation_cost").StringFixed(6), rate, fourSignificant(multiplier), r.n("total_cost").StringFixed(6), r.n("actual_cost").StringFixed(6), accountCost.Mul(multiplier).StringFixed(6), r.s("first_token_ms"), r.s("duration_ms"), r.s("request_id"), r.s("upstream_request_id"), r.s("user_agent"), r.s("ip_address")}, nil
}

// Match the existing administrator export's four significant digit rate strings.
func fourSignificant(n decimal.Decimal) string {
	if n.IsZero() {
		return "0.000"
	}
	exponent := int32(len(n.Abs().Coefficient().String())) + n.Exponent() - 1
	rounded := n.Round(3 - exponent)
	exponent = int32(len(rounded.Abs().Coefficient().String())) + rounded.Exponent() - 1
	if exponent >= 4 || exponent < -6 {
		suffix := strconv.Itoa(int(exponent))
		if exponent >= 0 {
			suffix = "+" + suffix
		}
		return rounded.Shift(-exponent).StringFixed(3) + "e" + suffix
	}
	return rounded.StringFixed(3 - exponent)
}
