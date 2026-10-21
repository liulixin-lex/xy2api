package resilience

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClassifySpecScenarios(t *testing.T) {
	cases := []struct {
		name       string
		in         Outcome
		extra      []string
		want       Category
		retryAfter time.Duration
	}{
		{
			// Spec: balance message wrapped in a 429.
			name: "balance wrapped in 429",
			in:   Outcome{StatusCode: 429, Message: "Insufficient account balance"},
			want: CategoryBalance,
		},
		{
			// Spec: balance failure inside a stream.
			name: "stream insufficient_quota",
			in:   Outcome{Source: SourceStreamEvent, StatusCode: 200, Code: "insufficient_quota", Message: "You exceeded your current quota"},
			want: CategoryBalance,
		},
		{
			// Spec: busy message inside a stream.
			name: "stream busy",
			in:   Outcome{Source: SourceStreamEvent, StatusCode: 200, Message: "The service is busy. Please retry later."},
			want: CategoryRateLimit,
		},
		{
			// Spec: relay timeout page with retry_after.
			name:       "524 relay page",
			in:         Outcome{StatusCode: 524, Body: []byte(`<html>error code: 524 ... "retry_after":120 </html>`)},
			want:       CategoryTransient,
			retryAfter: 120 * time.Second,
		},
		{
			name:       "502 cloudflare json retry_after 60",
			in:         Outcome{StatusCode: 502, Body: []byte(`{"error":{"message":"Bad gateway"},"retry_after": 60}`)},
			want:       CategoryTransient,
			retryAfter: 60 * time.Second,
		},
		{
			// Spec: new relay wording via extra keywords.
			name:  "extra keyword",
			in:    Outcome{StatusCode: 403, Message: "账户余额已用尽，请充值"},
			extra: []string{"余额已用尽"},
			want:  CategoryBalance,
		},
		{
			name: "extra keyword absent is auth",
			in:   Outcome{StatusCode: 403, Message: "账户余额已用尽，请充值"},
			want: CategoryAuth,
		},
		{
			name: "anthropic credit balance 400",
			in:   Outcome{StatusCode: 400, Type: "invalid_request_error", Message: "Your credit balance is too low to access the Anthropic API."},
			want: CategoryBalance,
		},
		{
			name: "402 default",
			in:   Outcome{StatusCode: 402, Message: "Payment required"},
			want: CategoryBalance,
		},
		{
			name: "402 deactivated workspace is auth",
			in:   Outcome{StatusCode: 402, Body: []byte(`{"detail":{"code":"deactivated_workspace"}}`)},
			want: CategoryAuth,
		},
		{
			name: "image 402",
			in:   Outcome{StatusCode: 402, Message: "Upstream image account has insufficient balance"},
			want: CategoryBalance,
		},
		{
			name: "chinese balance",
			in:   Outcome{StatusCode: 403, Message: "余额不足"},
			want: CategoryBalance,
		},
		{
			name: "401 invalid key",
			in:   Outcome{StatusCode: 401, Message: "invalid api key"},
			want: CategoryAuth,
		},
		{
			name: "plain 403 auth",
			in:   Outcome{StatusCode: 403, Message: "forbidden"},
			want: CategoryAuth,
		},
		{
			name: "403 html block page is transient",
			in:   Outcome{StatusCode: 403, Body: []byte("<!DOCTYPE html><title>Attention Required! | Cloudflare</title>")},
			want: CategoryTransient,
		},
		{
			name:       "429 with retry-after header",
			in:         Outcome{StatusCode: 429, Headers: http.Header{"Retry-After": {"20"}}},
			want:       CategoryRateLimitReset,
			retryAfter: 20 * time.Second,
		},
		{
			name: "429 without reset",
			in:   Outcome{StatusCode: 429, Message: "Upstream rate limit exceeded"},
			want: CategoryRateLimit,
		},
		{
			name:       "429 try again in",
			in:         Outcome{StatusCode: 429, Message: "Rate limit reached. Please try again in 7.5s."},
			want:       CategoryRateLimitReset,
			retryAfter: 7500 * time.Millisecond,
		},
		{
			name:       "usage limit with resets_in_seconds",
			in:         Outcome{StatusCode: 429, Code: "usage_limit_reached", Body: []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`)},
			want:       CategoryRateLimitReset,
			retryAfter: time.Hour,
		},
		{
			name: "gemini per-minute quota is a rate limit, not balance",
			in:   Outcome{StatusCode: 429, Code: "RESOURCE_EXHAUSTED", Message: "Quota exceeded for metric: generate_content_requests per minute"},
			want: CategoryRateLimit,
		},
		{
			name: "529 overloaded",
			in:   Outcome{StatusCode: 529, Type: "overloaded_error", Message: "Overloaded"},
			want: CategoryCapacityShed,
		},
		{
			name: "server_is_overloaded in stream",
			in:   Outcome{Source: SourceStreamEvent, StatusCode: 200, Code: "server_is_overloaded"},
			want: CategoryCapacityShed,
		},
		{
			name: "context length is client error",
			in:   Outcome{Source: SourceStreamEvent, StatusCode: 200, Code: "context_length_exceeded"},
			want: CategoryClientError,
		},
		{
			name: "400 invalid request",
			in:   Outcome{StatusCode: 400, Type: "invalid_request_error", Message: "messages: field required"},
			want: CategoryClientError,
		},
		{
			name: "404 model",
			in:   Outcome{StatusCode: 404, Type: "invalid_request_error", Message: "The model `gpt-x` does not exist"},
			want: CategoryModelUnsupported,
		},
		{
			name: "stream server_error",
			in:   Outcome{Source: SourceStreamEvent, StatusCode: 200, Code: "server_error", Message: "An error occurred"},
			want: CategoryTransient,
		},
		{
			name: "503 plain",
			in:   Outcome{StatusCode: 503},
			want: CategoryTransient,
		},
		{
			name: "usage_not_included is auth",
			in:   Outcome{Source: SourceStreamEvent, StatusCode: 200, Code: "usage_not_included"},
			want: CategoryAuth,
		},
		{
			name: "success",
			in:   Outcome{StatusCode: 200},
			want: CategorySuccess,
		},
		{
			name: "first output timeout",
			in:   Outcome{Source: SourceFirstOutputTimeout},
			want: CategoryFirstOutputTimeout,
		},
		{
			name: "stall",
			in:   Outcome{Source: SourceStall},
			want: CategoryStall,
		},
		{
			name: "client cancel",
			in:   Outcome{Source: SourceClientCancel},
			want: CategoryClientCancel,
		},
		{
			name: "network canceled",
			in:   Outcome{Source: SourceNetwork, Err: context.Canceled},
			want: CategoryClientCancel,
		},
		{
			name: "network reset",
			in:   Outcome{Source: SourceNetwork, Err: errors.New("connection reset by peer")},
			want: CategoryTransient,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in, tc.extra)
			require.Equal(t, tc.want, got.Category, "reason=%s", got.Reason)
			if tc.retryAfter > 0 {
				require.Equal(t, tc.retryAfter, got.RetryAfter)
			}
		})
	}
}

func TestCategoryPolicyTable(t *testing.T) {
	// Spec: category determines failover, penalty and session handling.
	type policy struct{ switchAcct, session, soft, scoped bool }
	want := map[Category]policy{
		CategoryBalance:            {true, false, false, true},
		CategoryAuth:               {true, false, false, true},
		CategoryRateLimitReset:     {true, false, false, true},
		CategoryRateLimit:          {true, true, false, true},
		CategoryCapacityShed:       {true, false, false, false},
		CategoryTransient:          {true, true, true, true},
		CategoryFirstOutputTimeout: {true, true, true, true},
		CategoryStall:              {false, true, true, true},
		CategoryClientCancel:       {false, false, false, false},
		CategoryClientError:        {false, false, false, false},
		CategoryModelUnsupported:   {true, false, false, true},
	}
	for c, p := range want {
		require.Equal(t, p.switchAcct, c.SwitchAccount(), "switch %s", c)
		require.Equal(t, p.session, c.CountsForSession(), "session %s", c)
		require.Equal(t, p.soft, c.SoftCooldown(), "soft %s", c)
		require.Equal(t, p.scoped, c.AccountScoped(), "scoped %s", c)
	}
}

func TestResultHard(t *testing.T) {
	require.True(t, Result{Category: CategoryBalance}.Hard())
	require.True(t, Result{Category: CategoryAuth}.Hard())
	require.False(t, Result{Category: CategoryRateLimitReset, RetryAfter: 300 * time.Second}.Hard())
	require.True(t, Result{Category: CategoryRateLimitReset, RetryAfter: 301 * time.Second}.Hard())
	require.False(t, Result{Category: CategoryTransient}.Hard())
}

func TestRetryAfterFrom(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("Retry-After", now.Add(90*time.Second).UTC().Format(http.TimeFormat))
	d := RetryAfterFrom(h, nil)
	require.InDelta(t, 90, d.Seconds(), 2)

	h = http.Header{}
	h.Set("retry-after-ms", "1500")
	require.Equal(t, 1500*time.Millisecond, RetryAfterFrom(h, nil))

	require.Equal(t, 2*time.Minute, RetryAfterFrom(nil, []byte(`please try again in 2 minutes`)))
	require.Equal(t, time.Duration(0), RetryAfterFrom(nil, []byte(`nothing here`)))
	require.Equal(t, 45*time.Second, RetryAfterFrom(nil, []byte(`retry_after: 45`)))
}

func TestChainedGatewayErrorShapes(t *testing.T) {
	// tasks 1.4: this gateway's own billing answers never enter
	// classification (they are written before any upstream attempt). Seen
	// from a gateway that uses another xy2api as its upstream, they classify
	// by what they mean for that account.
	for _, tc := range []struct {
		status int
		body   string
		want   Category
	}{
		{429, `{"error":{"type":"rate_limit_error","code":"concurrent_balance_reserved","message":"Available balance is reserved by in-flight requests (available $0.0400, reserved $0.0800); please retry shortly"}}`, CategoryRateLimit},
		{403, `{"error":{"type":"insufficient_balance","code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}}`, CategoryBalance},
		{503, `{"error":{"type":"api_error","code":"server_is_overloaded","message":"Upstream service temporarily unavailable"}}`, CategoryCapacityShed},
		{503, `{"error":{"type":"api_error","code":"proxy_upstream_unavailable","message":"Service temporarily unavailable, please retry"}}`, CategoryTransient},
	} {
		r := Classify(Outcome{Source: SourceHTTP, StatusCode: tc.status, Body: []byte(tc.body), Message: tc.body}, nil)
		require.Equal(t, tc.want, r.Category, tc.body)
	}
	require.True(t, Classify(Outcome{Source: SourceHTTP, StatusCode: 400, Body: []byte(`{"error":{"message":"bad"}}`)}, nil).StatusOnly())
	require.False(t, Classify(Outcome{Source: SourceHTTP, StatusCode: 429, Message: "insufficient balance"}, nil).StatusOnly())
}

func TestClassifyRelayWordings(t *testing.T) {
	// tasks 1.5: wordings seen from production relays and the common
	// new-api / one-api answers (scrubbed). Quota used up at a relay is a
	// balance failure (the account is paused), never an auth failure.
	for _, tc := range []struct {
		status int
		msg    string
		want   Category
	}{
		{403, "该令牌额度已用尽 (request id: 2026101612)", CategoryBalance},
		{429, "该令牌额度已用尽", CategoryBalance},
		{403, "token quota is not enough, token remain quota: $0.01, need quota: $0.08", CategoryBalance},
		{403, "user quota is not enough", CategoryBalance},
		{403, "预扣费额度失败, 用户剩余额度: $0.01, 需要预扣费额度: $0.08", CategoryBalance},
		{403, "用户额度不足, 剩余额度: $0.00", CategoryBalance},
		{403, "billing_error: insufficient balance", CategoryBalance},
		{403, "Insufficient account balance", CategoryBalance},
		{429, "Upstream rate limit exceeded, please retry later", CategoryRateLimit},
		{429, "当前分组上游负载已饱和，请稍后再试", CategoryRateLimit},
		{429, "Resource has been exhausted (e.g. check quota).", CategoryRateLimit},
		{503, "当前分组 default 下对于模型 gpt-6.1-sol 无可用渠道", CategoryTransient},
		{403, "Your account has been suspended", CategoryAuth},
	} {
		r := Classify(Outcome{Source: SourceHTTP, StatusCode: tc.status, Message: tc.msg}, nil)
		require.Equal(t, tc.want, r.Category, "%d %s", tc.status, tc.msg)
	}
	r := Classify(Outcome{Source: SourceStreamEvent, StatusCode: 200, Code: "rate_limit_exceeded",
		Message: "Upstream rate limit exceeded, please retry later"}, nil)
	require.Equal(t, CategoryRateLimit, r.Category, "in-stream relay rate limit")
}

func TestGeminiQuotaWordingIsARateLimit(t *testing.T) {
	gemini := []byte(`{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details. For more information on this error, head to: https://ai.google.dev/gemini-api/docs/rate-limits.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure"}]}}`)
	r := Classify(Outcome{Source: SourceHTTP, StatusCode: 429, Message: "You exceeded your current quota, please check your plan and billing details.", Type: "RESOURCE_EXHAUSTED", Body: gemini}, nil)
	require.Contains(t, []Category{CategoryRateLimit, CategoryRateLimitReset}, r.Category, r.Reason)

	// The same sentence from an OpenAI-style relay without a code is a balance failure.
	r = Classify(Outcome{Source: SourceHTTP, StatusCode: 429, Message: "You exceeded your current quota, please check your plan and billing details."}, nil)
	require.Equal(t, CategoryBalance, r.Category)
}

func TestRetryAfterFromCompoundHints(t *testing.T) {
	for in, want := range map[string]time.Duration{
		`{"error":{"message":"Rate limit reached. Please try again in 1m30.5s."}}`: 90500 * time.Millisecond,
		`Please try again in 1h2m`:  62 * time.Minute,
		`Please try again in 250ms`: 250 * time.Millisecond,
		`Please try again in 20s.`:  20 * time.Second,
	} {
		require.Equal(t, want, RetryAfterFrom(nil, []byte(in)), in)
	}
	r := Classify(Outcome{Source: SourceHTTP, StatusCode: 429, Message: "Rate limit reached. Please try again in 1m30s."}, nil)
	require.Equal(t, CategoryRateLimitReset, r.Category)
	require.Equal(t, 90*time.Second, r.RetryAfter)
}

func TestGatewayFirstEventTimeoutIsNoRelay504(t *testing.T) {
	r := Classify(Outcome{Source: SourceHTTP, StatusCode: 504, Type: "first_output_timeout",
		Body: []byte(`{"error":{"type":"first_output_timeout","message":"Upstream produced no output before the deadline"}}`)}, nil)
	require.Equal(t, CategoryFirstOutputTimeout, r.Category)
	require.Equal(t, CategoryTransient, Classify(Outcome{Source: SourceHTTP, StatusCode: 504, Body: []byte("gateway timeout")}, nil).Category)
}

func TestClassifyContentPolicyRefusal(t *testing.T) {
	// R4: an upstream content-safety refusal is a CLIENT_ERROR of the request,
	// never an account failure; account bans, balance and auth answers keep
	// their categories.
	zhipu := []byte(`{"code":"request_check_blocked","message":"Request rejected by preflight safety checks"}`)
	refusals := []Outcome{
		// Zhipu preflight check, code at the top level of the body.
		{StatusCode: 403, Message: "Request rejected by preflight safety checks", Body: zhipu},
		{StatusCode: 403, Body: zhipu},
		{StatusCode: 403, Code: "request_check_blocked", Message: "blocked"},
		// The wording alone.
		{StatusCode: 403, Message: "Request rejected by preflight safety checks"},
		// A chained sub2api-style gateway's own moderation answer.
		{StatusCode: 403, Code: "content_policy_violation", Message: "请求内容违反本分组安全策略（敏感话题）"},
		{StatusCode: 403, Message: "请求内容违反本分组安全策略（敏感话题）"},
		{StatusCode: 403, Message: "输入内容未通过内容安全审核"},
		{StatusCode: 451, Message: "The request violates our content policy"},
		{StatusCode: 400, Message: "系统检测到输入或生成内容可能包含不安全或敏感内容，请您避免输入易产生敏感内容的提示语"},
		{StatusCode: 400, Code: "data_inspection_failed", Message: "Input data may contain inappropriate content."},
		{StatusCode: 400, Type: "invalid_request_error", Message: "Your request was rejected as a result of our safety system."},
		{Source: SourceStreamEvent, StatusCode: 200, Code: "content_filter", Message: "filtered"},
		{Source: SourceStreamEvent, StatusCode: 200, Type: "content_policy_violation", Message: "blocked by our content policy"},
	}
	for _, o := range refusals {
		r := Classify(o, nil)
		require.Equal(t, CategoryClientError, r.Category, "%d %s %s", o.StatusCode, o.Code, o.Message)
		require.True(t, r.ContentPolicy(), "%d %s %s reason=%s", o.StatusCode, o.Code, o.Message, r.Reason)
		require.False(t, r.Category.SwitchAccount())
		require.False(t, r.Category.AccountScoped())
	}

	others := []struct {
		in   Outcome
		want Category
	}{
		{Outcome{StatusCode: 403, Message: "forbidden"}, CategoryAuth},
		{Outcome{StatusCode: 403, Message: "余额不足，请充值"}, CategoryBalance},
		{Outcome{StatusCode: 403, Code: "insufficient_quota", Message: "content policy"}, CategoryBalance},
		{Outcome{StatusCode: 401, Message: "invalid api key, safety check failed"}, CategoryAuth},
		{Outcome{StatusCode: 403, Code: "invalid_api_key", Message: "safety check failed"}, CategoryAuth},
		// Bans for policy violations name the account.
		{Outcome{StatusCode: 403, Message: "Your account has been suspended for content policy violations"}, CategoryAuth},
		{Outcome{StatusCode: 403, Message: "Organization disabled: repeated content policy violations"}, CategoryAuth},
		{Outcome{StatusCode: 403, Message: "该账号因多次违反安全策略已被封禁"}, CategoryAuth},
		// A bare "违反" or "敏感" is no content-safety signal.
		{Outcome{StatusCode: 403, Message: "请求违反访问规则"}, CategoryAuth},
		{Outcome{StatusCode: 403, Message: "IP 敏感，拒绝访问"}, CategoryAuth},
		// An edge block page stays a transient edge failure.
		{Outcome{StatusCode: 403, Body: []byte("<!DOCTYPE html><title>Attention Required! | Cloudflare</title> safety check")}, CategoryTransient},
		// Wording inside a stream or on other statuses is not enough.
		{Outcome{Source: SourceStreamEvent, StatusCode: 200, Message: "blocked by content policy"}, CategoryTransient},
		{Outcome{StatusCode: 429, Message: "content policy check queue is full"}, CategoryRateLimit},
	}
	for _, tc := range others {
		r := Classify(tc.in, nil)
		require.Equal(t, tc.want, r.Category, "%d %s reason=%s", tc.in.StatusCode, tc.in.Message, r.Reason)
		require.False(t, r.ContentPolicy(), tc.in.Message)
	}

	// Parameter errors stay plain client errors.
	r := Classify(Outcome{StatusCode: 400, Message: "参数 model 大小写敏感"}, nil)
	require.Equal(t, CategoryClientError, r.Category)
	require.False(t, r.ContentPolicy())
	r = Classify(Outcome{StatusCode: 400, Type: "invalid_request_error", Message: "messages: field required"}, nil)
	require.Equal(t, CategoryClientError, r.Category)
	require.False(t, r.ContentPolicy())
}

// cloudflare502JSON is the shape of the error answer production saw from a
// relay behind Cloudflare (scrubbed): the origin behind the relay's edge
// failed, and the explanation happens to say "overloaded".
const cloudflare502JSON = `{"type":"https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-502/","title":"Error 502: Bad gateway","status":502,"detail":"The web server reported a bad gateway error. This typically indicates the origin is overloaded or misconfigured.","error_code":502,"error_name":"origin_bad_gateway","error_category":"origin","ray_id":"9a1b2c3d4e5f6789","cloudflare_error":true,"retryable":true,"retry_after":60,"owner_action_required":true}`

func TestEdgeErrorPagesAreTransientNotCapacity(t *testing.T) {
	// Production 2026-10-20: one account received this 502 21 times in four
	// turns. As CAPACITY_SHED it was never cooled down, never counted for the
	// session and never switched within its host; it is the relay failing.
	for _, tc := range []struct {
		name   string
		status int
		msg    string
		body   string
	}{
		{"cloudflare 502 JSON", 502, "The web server reported a bad gateway error. This typically indicates the origin is overloaded or misconfigured.", cloudflare502JSON},
		{"cloudflare 503 JSON", 503, "", `{"title":"Error 503: Service temporarily unavailable","detail":"The origin is overloaded.","cloudflare_error":true,"retry_after":60}`},
		{"origin error category", 502, "", `{"title":"Error 502: Bad gateway","detail":"The origin is overloaded.","error_category":"origin","retry_after":60}`},
		{"cloudflare 502 HTML", 502, "", `<!DOCTYPE html><html><head><title>relay.example | 502: Bad gateway</title></head><body><div id="cf-error-details">The origin is overloaded or misconfigured. "retry_after":60 Cloudflare Ray ID: 9a1b2c3d4e5f6789</div></body></html>`},
		{"cloudflare 521 HTML", 521, "", `<html><head><title>521: Web server is down</title></head><body>The origin web server is overloaded or down. retry_after: 60</body></html>`},
		{"relay 502 JSON", 502, "upstream overloaded, please retry", `{"error":{"message":"upstream overloaded, please retry"},"retry_after":60}`},
	} {
		r := Classify(Outcome{Source: SourceHTTP, StatusCode: tc.status, Message: tc.msg, Body: []byte(tc.body)}, nil)
		require.Equal(t, CategoryTransient, r.Category, "%s: %s", tc.name, r.Reason)
		require.Equal(t, 60*time.Second, r.RetryAfter, tc.name)
		require.True(t, r.Category.AccountScoped() && r.Category.CountsForSession() && r.Category.SoftCooldown(), tc.name)
	}

	// Capacity shedding by the upstream itself stays request scoped.
	for _, tc := range []struct {
		name string
		in   Outcome
	}{
		{"anthropic 529", Outcome{StatusCode: 529, Type: "overloaded_error", Message: "Overloaded",
			Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}},
		{"503 server overloaded", Outcome{StatusCode: 503, Message: "server overloaded, please retry",
			Body: []byte(`{"error":{"message":"server overloaded, please retry"}}`)}},
		{"500 overloaded type", Outcome{StatusCode: 500, Type: "overloaded_error", Message: "Overloaded"}},
		{"429 model overloaded", Outcome{StatusCode: 429, Message: "That model is currently overloaded with other requests."}},
		{"400 model at capacity", Outcome{StatusCode: 400, Message: "Selected model is at capacity. Please try a different model."}},
		{"stream overloaded", Outcome{Source: SourceStreamEvent, StatusCode: 200, Message: "The server is overloaded"}},
	} {
		require.Equal(t, CategoryCapacityShed, Classify(tc.in, nil).Category, tc.name)
	}
}

func TestDecisionV2PreservesExplicitRateLimitTextOn5xx(t *testing.T) {
	cfg := Defaults()
	for _, message := range []string{"Rate limit exceeded", "Too many requests"} {
		for _, status := range []int{400, 429, 500, 502, 503} {
			outcome := Outcome{StatusCode: status, Message: message}
			require.Equal(t, CategoryRateLimit, ClassifyWithSettings(outcome, &cfg).Category, "%d %s", status, message)
			outcome.Headers = http.Header{"Retry-After": []string{"600"}}
			result := ClassifyWithSettings(outcome, &cfg)
			require.Equal(t, CategoryRateLimitReset, result.Category, "%d %s", status, message)
			require.Equal(t, 600*time.Second, result.RetryAfter)
		}
	}
	for _, message := range []string{"The service is busy", "Too many pending requests", "Concurrency limit exceeded for account"} {
		require.Equal(t, CategoryRateLimit, ClassifyWithSettings(Outcome{StatusCode: 429, Message: message}, &cfg).Category)
		require.Equal(t, CategoryTransient, ClassifyWithSettings(Outcome{StatusCode: 502, Message: message}, &cfg).Category)
	}
	cfg.Failure.DecisionV2 = false
	require.Equal(t, CategoryRateLimit, ClassifyWithSettings(Outcome{StatusCode: 502, Message: "The service is busy"}, &cfg).Category)
}
