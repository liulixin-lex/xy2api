package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type sub2apiUsage struct {
	Mode      *string  `json:"mode"`
	IsValid   *bool    `json:"isValid"`
	Status    string   `json:"status"`
	Unit      string   `json:"unit"`
	Remaining *float64 `json:"remaining"`
	Balance   *float64 `json:"balance"`
	Quota     *struct {
		Remaining *float64 `json:"remaining"`
	} `json:"quota"`
	RateLimits []struct {
		Remaining *float64 `json:"remaining"`
	} `json:"rate_limits"`
	Subscription *struct {
		Daily   *float64 `json:"daily_limit_usd"`
		Weekly  *float64 `json:"weekly_limit_usd"`
		Monthly *float64 `json:"monthly_limit_usd"`
	} `json:"subscription"`
}

func parseSub2APIUsage(body []byte) (*ubOutcome, error) {
	var v sub2apiUsage
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.Mode == nil || (*v.Mode != "quota_limited" && *v.Mode != "unrestricted") || v.IsValid == nil {
		return nil, nil
	}
	if !*v.IsValid {
		return &ubOutcome{reason: "auth_failed", kind: "sub2api"}, nil
	}
	if v.Unit != "" && !strings.EqualFold(v.Unit, "USD") {
		return &ubOutcome{reason: "no_data", kind: "sub2api"}, nil
	}
	if *v.Mode == "quota_limited" {
		var vals []float64
		if v.Quota != nil && v.Quota.Remaining != nil {
			vals = append(vals, *v.Quota.Remaining)
		}
		for _, r := range v.RateLimits {
			if r.Remaining != nil {
				vals = append(vals, *r.Remaining)
			}
		}
		if len(vals) == 0 {
			return &ubOutcome{reason: "no_data", kind: "sub2api"}, nil
		}
		m := vals[0]
		for _, x := range vals[1:] {
			if x < m {
				m = x
			}
		}
		if !isFinite(m) {
			return &ubOutcome{reason: "upstream_error", kind: "sub2api"}, nil
		}
		if v.Status == "quota_exhausted" || v.Status == "expired" {
			m = 0
			return &ubOutcome{success: true, amount: &m, basis: "key", reason: "key_exhausted", kind: "sub2api"}, nil
		}
		return &ubOutcome{success: true, amount: &m, basis: "key", kind: "sub2api"}, nil
	}
	if v.Balance != nil {
		return &ubOutcome{success: true, amount: v.Balance, basis: "wallet", kind: "sub2api"}, nil
	}
	if v.Remaining != nil {
		if *v.Remaining == -1 {
			return &ubOutcome{success: true, unlimited: true, basis: "subscription", kind: "sub2api"}, nil
		}
		return &ubOutcome{success: true, amount: v.Remaining, basis: "subscription", kind: "sub2api"}, nil
	}
	if v.Subscription != nil {
		if (v.Subscription.Daily == nil || *v.Subscription.Daily <= 0) && (v.Subscription.Weekly == nil || *v.Subscription.Weekly <= 0) && (v.Subscription.Monthly == nil || *v.Subscription.Monthly <= 0) {
			return &ubOutcome{success: true, unlimited: true, basis: "subscription", kind: "sub2api"}, nil
		}
		return &ubOutcome{reason: "no_data", kind: "sub2api"}, nil
	}
	return &ubOutcome{reason: "no_data", kind: "sub2api"}, nil
}
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (s *UpstreamBalanceService) collectSub2API(ctx context.Context, a *Account, base string, st *UpstreamBalanceState, memo *upstreamBalanceRoundMemo) ubOutcome {
	d := s.now().UTC().Format("2006-01-02")
	u, e := upstreamBalanceV1URL(base, "/v1/usage", url.Values{"days": {"1"}, "start_date": {d}, "end_date": {d}})
	if e != nil {
		return ubOutcome{reason: "upstream_error", kind: "sub2api"}
	}
	started := s.now()
	resp, b, e := s.ubRequestWithBudget(ctx, a, u, "Bearer "+a.GetCredential("api_key"), upstreamBalanceMaxBodyBytes, memo)
	if e != nil {
		if errors.Is(e, errUpstreamBalanceBudgetExceeded) {
			return ubOutcome{reason: "budget_deferred", kind: "sub2api"}
		}
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			return ubOutcome{reason: "rate_limited", kind: "sub2api", status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header, s.now())}
		}
		return ubOutcome{reason: "upstream_error", kind: "sub2api"}
	}
	if resp == nil {
		return ubOutcome{reason: "upstream_error", kind: "sub2api"}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return ubOutcome{reason: "rate_limited", kind: "sub2api", status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header, s.now())}
	}
	if resp.StatusCode == http.StatusForbidden && isJSONBody(b) && isSub2APIUngrouped(b) {
		return ubOutcome{reason: "key_ungrouped", kind: "sub2api", status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if isJSONBody(b) {
			return ubOutcome{reason: "auth_failed", status: resp.StatusCode}
		}
		return ubOutcome{reason: "upstream_error", status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return ubOutcome{reason: "unidentified", kind: "unsupported", status: resp.StatusCode}
	}
	if resp.StatusCode >= 500 {
		return ubOutcome{reason: "upstream_error", status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ubOutcome{reason: "unidentified", kind: "unsupported", status: resp.StatusCode}
	}
	o, e := parseSub2APIUsage(b)
	if e != nil {
		return ubOutcome{reason: "unidentified", kind: "unsupported", status: resp.StatusCode}
	}
	if o == nil {
		return ubOutcome{reason: "unidentified", kind: "unsupported", status: resp.StatusCode}
	}
	o.status = resp.StatusCode
	requestDuration := s.now().Sub(started)
	if requestDuration > 3*time.Second {
		st.Slow = true
		st.FastStreak = 0
	} else if st.Slow {
		if requestDuration < time.Second {
			st.FastStreak++
			if st.FastStreak >= 3 {
				st.Slow = false
				st.FastStreak = 0
			}
		} else {
			st.FastStreak = 0
		}
	}
	return *o
}

func isJSONBody(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func isSub2APIUngrouped(body []byte) bool {
	var v struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &v) == nil && v.Error.Type == "permission_error" && strings.Contains(v.Error.Message, "not assigned to any group")
}

func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := time.ParseDuration(v + "s"); err == nil {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return 0
}
