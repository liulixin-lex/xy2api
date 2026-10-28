package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
)

type newAPIStatus struct {
	Success *bool `json:"success"`
	Data    struct {
		QuotaPerUnit      float64 `json:"quota_per_unit"`
		DisplayType       string  `json:"quota_display_type"`
		DisplayInCurrency *bool   `json:"display_in_currency"`
		USDExchangeRate   float64 `json:"usd_exchange_rate"`
	} `json:"data"`
}
type newAPIBillingSubscription struct {
	HardLimitUSD *float64 `json:"hard_limit_usd"`
}
type newAPIBillingUsage struct {
	TotalUsage *float64 `json:"total_usage"`
}
type newAPIWallet struct {
	Success *bool `json:"success"`
	Data    struct {
		Quota *float64 `json:"quota"`
	} `json:"data"`
}
type newAPITokenUsage struct {
	Code json.RawMessage `json:"code"`
	Data struct {
		Object         string   `json:"object"`
		TotalAvailable *float64 `json:"total_available"`
		UnlimitedQuota *bool    `json:"unlimited_quota"`
		ExpiresAt      int64    `json:"expires_at"`
	} `json:"data"`
}

func parseNewAPIStatus(b []byte) (*UpstreamBalanceSite, bool) {
	var v newAPIStatus
	if json.Unmarshal(b, &v) != nil || v.Success == nil || !*v.Success || !isFinite(v.Data.QuotaPerUnit) || v.Data.QuotaPerUnit <= 0 {
		return nil, false
	}
	typ := strings.ToUpper(strings.TrimSpace(v.Data.DisplayType))
	if typ == "" && v.Data.DisplayInCurrency != nil && !*v.Data.DisplayInCurrency {
		typ = "TOKENS"
	}
	if typ == "" {
		typ = "USD"
	}
	return &UpstreamBalanceSite{Version: 1, QuotaPerUnit: v.Data.QuotaPerUnit, DisplayType: typ, USDExchangeRate: v.Data.USDExchangeRate, FetchedAt: time.Now().UTC()}, true
}

func (s *UpstreamBalanceService) collectNewAPI(ctx context.Context, a *Account, base string, st *UpstreamBalanceState, memo *upstreamBalanceRoundMemo) ubOutcome {
	origin, err := upstreamBalanceOrigin(base)
	if err != nil {
		return ubOutcome{reason: "upstream_error", kind: "newapi"}
	}
	site, out := s.newAPISite(ctx, a, origin, memo)
	if out != nil {
		return *out
	}
	subURL, _ := upstreamBalanceV1URL(base, "/v1/dashboard/billing/subscription", nil)
	resp, b, e := s.newAPIRequest(ctx, a, subURL, "Bearer "+a.GetCredential("api_key"), "", upstreamBalanceMaxBodyBytes, memo)
	if out := s.newAPIBillingFailure(ctx, a, origin, st, memo, resp, b, e); out != nil {
		return *out
	}
	var sub newAPIBillingSubscription
	if json.Unmarshal(b, &sub) != nil || sub.HardLimitUSD == nil || !isFinite(*sub.HardLimitUSD) {
		return ubOutcome{reason: "upstream_error", kind: "newapi"}
	}
	usageURL, _ := upstreamBalanceV1URL(base, "/v1/dashboard/billing/usage", nil)
	resp, b, e = s.newAPIRequest(ctx, a, usageURL, "Bearer "+a.GetCredential("api_key"), "", upstreamBalanceMaxBodyBytes, memo)
	if out := s.newAPIBillingFailure(ctx, a, origin, st, memo, resp, b, e); out != nil {
		return *out
	}
	var usage newAPIBillingUsage
	// Both billing responses accepted the key, even if the value or wallet
	// cannot produce a usable balance below.
	st.AuthStreak = 0
	if json.Unmarshal(b, &usage) != nil || usage.TotalUsage == nil || !isFinite(*usage.TotalUsage) {
		return ubOutcome{reason: "upstream_error", kind: "newapi"}
	}
	key := *sub.HardLimitUSD - *usage.TotalUsage/100
	if !isFinite(key) {
		return ubOutcome{reason: "upstream_error", kind: "newapi"}
	}
	if *sub.HardLimitUSD == 1e8 {
		switch site.DisplayType {
		case "TOKENS":
			return s.withWallet(ctx, a, origin, site, st, memo, ubOutcome{reason: "no_data", kind: "newapi"})
		case "USD", "CNY":
			return s.withWallet(ctx, a, origin, site, st, memo, ubOutcome{success: true, unlimited: true, kind: "newapi"})
		}
	}
	key, ok := newAPIToUSD(key, site)
	if !ok {
		return ubOutcome{reason: "no_data", kind: "newapi"}
	}
	return s.withWallet(ctx, a, origin, site, st, memo, ubOutcome{success: true, amount: &key, kind: "newapi"})
}
func (s *UpstreamBalanceService) withWallet(ctx context.Context, a *Account, origin string, site *UpstreamBalanceSite, st *UpstreamBalanceState, memo *upstreamBalanceRoundMemo, key ubOutcome) ubOutcome {
	configured := strings.TrimSpace(a.GetCredential("newapi_access_token"))
	token := normalizeNewAPIToken(configured)
	uid := strings.TrimSpace(a.GetCredential("newapi_user_id"))
	if configured == "" {
		return key
	}
	if !usableNewAPIToken(token, uid) {
		st.TokenFailures = 3
		retry := s.now().Add(30 * time.Minute)
		st.TokenRetryAt = &retry
		if key.success {
			key.reason = "token_invalid"
		}
		return key
	}
	if st.TokenFailures >= 3 && st.TokenRetryAt != nil && s.now().Before(*st.TokenRetryAt) {
		if key.success {
			key.reason = "token_invalid"
		}
		return key
	}
	resp, b, e := s.newAPIRequest(ctx, a, origin+"/api/user/self", token, uid, upstreamBalanceMaxBodyBytes, memo)
	if out, ok := newAPIRequestOutcome(e); ok {
		return out
	}
	if e != nil || resp == nil {
		return ubOutcome{reason: "upstream_error", kind: "newapi", status: statusOf(resp)}
	}
	if resp.StatusCode >= 500 || (resp.StatusCode >= 300 && resp.StatusCode < 400) ||
		((resp.StatusCode == 401 || resp.StatusCode == 403) && !isJSONBody(b)) {
		return ubOutcome{reason: "upstream_error", kind: "newapi", status: resp.StatusCode}
	}
	var w newAPIWallet
	if json.Unmarshal(b, &w) != nil {
		return ubOutcome{reason: "upstream_error", kind: "newapi", status: resp.StatusCode}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 || w.Success == nil || !*w.Success || newAPIAuthRejected(b) {
		st.TokenFailures++
		if st.TokenFailures >= 3 {
			t := s.now().Add(30 * time.Minute)
			st.TokenRetryAt = &t
			if key.success {
				key.reason = "token_invalid"
			}
			return key
		}
		return ubOutcome{reason: "token_invalid", kind: "newapi"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || w.Data.Quota == nil {
		return ubOutcome{reason: "upstream_error", kind: "newapi", status: resp.StatusCode}
	}
	amount := *w.Data.Quota / site.QuotaPerUnit
	if !isFinite(amount) {
		return ubOutcome{reason: "upstream_error", kind: "newapi"}
	}
	st.TokenFailures = 0
	st.TokenRetryAt = nil
	if key.unlimited || !key.success {
		return ubOutcome{success: true, amount: &amount, basis: "wallet", kind: "newapi"}
	}
	if key.amount != nil && amount <= *key.amount {
		return ubOutcome{success: true, amount: &amount, basis: "wallet", kind: "newapi"}
	}
	key.basis = "key"
	return key
}
func normalizeNewAPIToken(token string) string {
	token = strings.TrimSpace(token)
	if strings.EqualFold(token, "Bearer") {
		return ""
	}
	if len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	return token
}
func usableNewAPIToken(t, u string) bool {
	return t != "" && strings.IndexFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0 && (u == "" || newAPIUserIDPattern.MatchString(u))
}
func newAPIToUSD(v float64, s *UpstreamBalanceSite) (float64, bool) {
	if !isFinite(v) {
		return 0, false
	}
	switch s.DisplayType {
	case "USD":
		return v, true
	case "CNY":
		if s.USDExchangeRate <= 0 || !isFinite(s.USDExchangeRate) || !isFinite(v/s.USDExchangeRate) {
			return 0, false
		}
		return v / s.USDExchangeRate, true
	case "TOKENS":
		if s.QuotaPerUnit <= 0 || !isFinite(s.QuotaPerUnit) || !isFinite(v/s.QuotaPerUnit) {
			return 0, false
		}
		return v / s.QuotaPerUnit, true
	}
	return 0, false
}
func statusOf(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}
func (s *UpstreamBalanceService) rateLimitedNewAPI(origin string, r *http.Response, memo *upstreamBalanceRoundMemo) ubOutcome {
	now := s.now()
	d := parseRetryAfter(r.Header, now)
	if d <= 0 {
		d = 5 * time.Minute
	}
	deadline := now.Add(d)
	memo.setCooldown(origin, deadline)
	writeCtx, cancel := context.WithTimeout(context.Background(), upstreamBalanceStoreTimeout)
	defer cancel()
	if err := s.store.SaveCooldown(writeCtx, origin, deadline); err != nil {
		memo.failStore(writeCtx)
		return ubOutcome{reason: "redis_unavailable", kind: "newapi", status: r.StatusCode}
	}
	return ubOutcome{reason: "rate_limited", kind: "newapi", status: r.StatusCode, retryAfter: d, retryNotBefore: &deadline}
}
func (s *UpstreamBalanceService) ubRequestWithToken(ctx context.Context, a *Account, u, t, uid string, max int) (*http.Response, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, upstreamBalanceRequestTimeout)
	defer cancel()
	r, e := http.NewRequestWithContext(requestCtx, http.MethodGet, u, nil)
	if e != nil {
		return nil, nil, e
	}
	a.ApplyHeaderOverrides(r.Header)
	for name := range r.Header {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "New-Api-User") {
			delete(r.Header, name)
		}
	}
	r.Header.Set("Accept", "application/json")
	if uid == "" {
		r.Header.Del("New-Api-User")
		r.Header.Set("Authorization", "Bearer "+t)
	} else {
		r.Header.Set("Authorization", t)
		r.Header.Set("New-Api-User", uid)
	}
	r = r.WithContext(WithHTTPUpstreamSingleAttempt(WithHTTPUpstreamRedirectsDisabled(r.Context())))
	p := ""
	if a.ProxyID != nil {
		if a.Proxy == nil || a.Proxy.ID != *a.ProxyID {
			return nil, nil, context.Canceled
		}
		p = a.Proxy.URL()
	}
	r = r.WithContext(WithHTTPUpstreamProfile(r.Context(), HTTPUpstreamProfileDefault))
	var profile *tlsfingerprint.Profile
	if a.Platform == PlatformOpenAI {
		r = r.WithContext(WithHTTPUpstreamProfile(r.Context(), HTTPUpstreamProfileOpenAI))
	}
	if s.accountTestService.tlsFPProfileService != nil {
		profile = s.accountTestService.tlsFPProfileService.ResolveTLSProfile(a)
	}
	resp, e := s.accountTestService.httpUpstream.DoWithTLS(r, p, a.ID, a.Concurrency, profile)
	if e != nil {
		return nil, nil, e
	}
	b, e := readUBBody(resp, max)
	return resp, b, e
}

// newAPIRequest checks the shared deadline immediately before every HTTP call,
// including the nested CT call, and publishes every 429 even if body reading failed.
func (s *UpstreamBalanceService) newAPIRequest(ctx context.Context, a *Account, rawURL, auth, userID string, max int, memo *upstreamBalanceRoundMemo) (*http.Response, []byte, error) {
	origin := mustOrigin(rawURL)
	if out := s.newAPICooldown(ctx, origin, memo); out != nil {
		return nil, nil, &newAPIOutcomeError{out: *out}
	}
	var resp *http.Response
	var body []byte
	var err error
	if strings.HasSuffix(rawURL, "/api/user/self") {
		if !memo.takeReservation(a.ID, origin) {
			return nil, nil, &newAPIOutcomeError{out: ubOutcome{reason: "budget_deferred", kind: "newapi"}}
		}
		resp, body, err = s.ubRequestWithToken(ctx, a, rawURL, auth, userID, max)
	} else {
		resp, body, err = s.ubRequestWithBudget(ctx, a, rawURL, auth, max, memo)
	}
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		return resp, body, &newAPIOutcomeError{out: s.rateLimitedNewAPI(origin, resp, memo)}
	}
	if errors.Is(err, errUpstreamBalanceBudgetExceeded) {
		return resp, body, &newAPIOutcomeError{out: ubOutcome{reason: "budget_deferred", kind: "newapi"}}
	}
	return resp, body, err
}

type newAPIOutcomeError struct{ out ubOutcome }

func (*newAPIOutcomeError) Error() string { return "new-api request deferred" }
func newAPIRequestOutcome(err error) (ubOutcome, bool) {
	var outcome *newAPIOutcomeError
	if errors.As(err, &outcome) {
		return outcome.out, true
	}
	return ubOutcome{}, false
}

func (s *UpstreamBalanceService) newAPICooldown(ctx context.Context, origin string, memo *upstreamBalanceRoundMemo) *ubOutcome {
	until, err := s.store.GetCooldown(ctx, origin)
	if err != nil {
		reason := "upstream_error"
		if memo.failStore(ctx) {
			reason = "redis_unavailable"
		}
		return &ubOutcome{reason: reason, kind: "newapi"}
	}
	if until != nil && s.now().Before(*until) {
		memo.setCooldown(origin, *until)
		return &ubOutcome{reason: "rate_limited", kind: "newapi", retryAfter: until.Sub(s.now()), retryNotBefore: until}
	}
	if memo.cooldownActive(origin, s.now()) {
		memo.mu.Lock()
		deadline := memo.cooldowns[origin]
		memo.mu.Unlock()
		return &ubOutcome{reason: "rate_limited", kind: "newapi", retryAfter: deadline.Sub(s.now()), retryNotBefore: &deadline}
	}
	return nil
}

func (s *UpstreamBalanceService) newAPIBillingFailure(ctx context.Context, a *Account, origin string, st *UpstreamBalanceState, memo *upstreamBalanceRoundMemo, resp *http.Response, body []byte, err error) *ubOutcome {
	if out, ok := newAPIRequestOutcome(err); ok {
		return &out
	}
	if err != nil || resp == nil {
		return &ubOutcome{reason: "upstream_error", kind: "newapi", status: statusOf(resp)}
	}
	status := resp.StatusCode
	if status == 401 || status == 403 {
		if !isJSONBody(body) {
			return &ubOutcome{reason: "upstream_error", kind: "newapi", status: status}
		}
		if status == 401 {
			out := s.disambiguate401(ctx, a, origin, st, memo)
			if out.status == 0 {
				out.status = status
			}
			return &out
		}
		return &ubOutcome{reason: "auth_failed", kind: "newapi", status: status}
	}
	if status < 200 || status >= 300 {
		return &ubOutcome{reason: "upstream_error", kind: "newapi", status: status, deleteKind: status == 404 || status == 405}
	}
	if newAPIAuthRejected(body) {
		return &ubOutcome{reason: "auth_failed", kind: "newapi", status: status}
	}
	// Do not reset the streak until both billing envelopes have been accepted.
	st.AuthEpisode, st.AuthEpisodeAt = "", nil
	return nil
}

func newAPIAuthRejected(body []byte) bool {
	var envelope struct {
		Success *bool           `json:"success"`
		Code    json.RawMessage `json:"code"`
	}
	return json.Unmarshal(body, &envelope) == nil && ((envelope.Success != nil && !*envelope.Success) || string(envelope.Code) == "false")
}

type newAPISiteResult struct {
	site *UpstreamBalanceSite
	out  *ubOutcome
}

func (s *UpstreamBalanceService) newAPISite(ctx context.Context, a *Account, origin string, memo *upstreamBalanceRoundMemo) (*UpstreamBalanceSite, *ubOutcome) {
	result, err, _ := s.siteFlight.Do(origin, func() (any, error) {
		site, err := s.store.GetSite(ctx, origin)
		if err != nil {
			reason := "upstream_error"
			if memo.failStore(ctx) {
				reason = "redis_unavailable"
			}
			return newAPISiteResult{out: &ubOutcome{reason: reason, kind: "newapi"}}, nil
		}
		if site != nil {
			return newAPISiteResult{site: site}, nil
		}
		if !memo.reserveMore(a.ID, origin, 1) {
			return newAPISiteResult{out: &ubOutcome{reason: "budget_deferred", kind: "newapi"}}, nil
		}
		resp, body, err := s.newAPIRequest(ctx, a, origin+"/api/status", "", "", upstreamBalanceStatusMaxBodyBytes, memo)
		if out, ok := newAPIRequestOutcome(err); ok {
			return newAPISiteResult{out: &out}, nil
		}
		if err != nil || resp == nil {
			return newAPISiteResult{out: &ubOutcome{reason: "upstream_error", kind: "newapi", status: statusOf(resp)}}, nil
		}
		status := resp.StatusCode
		if status < 200 || status >= 300 {
			return newAPISiteResult{out: &ubOutcome{reason: "upstream_error", kind: "newapi", status: status, deleteKind: status == 404 || status == 405, notNewAPI: status >= 400 && status < 500 && status != http.StatusUnauthorized && status != http.StatusForbidden}}, nil
		}
		site, ok := parseNewAPIStatus(body)
		if !ok {
			return newAPISiteResult{out: &ubOutcome{reason: "upstream_error", kind: "newapi", status: status, deleteKind: true, notNewAPI: true}}, nil
		}
		site.FetchedAt = s.now().UTC()
		if err := s.store.SaveSite(ctx, origin, site, 30*time.Minute); err != nil {
			reason := "upstream_error"
			if memo.failStore(ctx) {
				reason = "redis_unavailable"
			}
			return newAPISiteResult{out: &ubOutcome{reason: reason, kind: "newapi"}}, nil
		}
		return newAPISiteResult{site: site}, nil
	})
	if err != nil {
		return nil, &ubOutcome{reason: "upstream_error", kind: "newapi"}
	}
	v, ok := result.(newAPISiteResult)
	if !ok {
		return nil, &ubOutcome{reason: "upstream_error", kind: "newapi"}
	}
	if v.out != nil && v.out.reason == "redis_unavailable" {
		memo.failStore(ctx)
	}
	return v.site, v.out
}
func (s *UpstreamBalanceService) disambiguate401(ctx context.Context, a *Account, origin string, st *UpstreamBalanceState, memo *upstreamBalanceRoundMemo) ubOutcome {
	if out := s.newAPICooldown(ctx, origin, memo); out != nil {
		return *out
	}
	now := s.now()
	if st.AuthEpisode != "" && st.AuthEpisodeAt != nil && !now.Before(*st.AuthEpisodeAt) && now.Sub(*st.AuthEpisodeAt) < time.Hour {
		if st.AuthEpisode == "key_exhausted" {
			z := 0.0
			return ubOutcome{success: true, amount: &z, reason: "key_exhausted", kind: "newapi"}
		}
		return ubOutcome{reason: "auth_failed", kind: "newapi"}
	}
	st.AuthEpisode, st.AuthEpisodeAt = "", nil
	if !memo.reserveMore(a.ID, origin, 1) {
		return ubOutcome{reason: "auth_failed", kind: "newapi"}
	}
	locked, lockErr := s.store.TryLockOrigin(ctx, origin, s.instanceID, 75*time.Second)
	if lockErr != nil || !locked {
		memo.releaseOne(a.ID)
		if lockErr != nil {
			reason := "upstream_error"
			if memo.failStore(ctx) {
				reason = "redis_unavailable"
			}
			return ubOutcome{reason: reason, kind: "newapi"}
		}
		return ubOutcome{reason: "auth_failed", kind: "newapi"}
	}
	resp, b, err := s.newAPIRequest(ctx, a, origin+"/api/usage/token/", "Bearer "+a.GetCredential("api_key"), "", upstreamBalanceMaxBodyBytes, memo)
	if out, ok := newAPIRequestOutcome(err); ok {
		if out.reason == "budget_deferred" {
			return ubOutcome{reason: "auth_failed", kind: "newapi"}
		}
		return out
	}
	if err != nil || resp == nil || resp.StatusCode >= 500 {
		return ubOutcome{reason: "auth_failed", kind: "newapi", status: statusOf(resp)}
	}
	if (resp.StatusCode == 401 || resp.StatusCode == 403) && !isJSONBody(b) {
		return ubOutcome{reason: "upstream_error", kind: "newapi", status: resp.StatusCode}
	}
	var v newAPITokenUsage
	decoded := json.Unmarshal(b, &v) == nil
	valid := resp.StatusCode == 200 && decoded && string(v.Code) == "true" && v.Data.Object == "token_usage" && !newAPIAuthRejected(b)
	if valid && v.Data.TotalAvailable != nil && isFinite(*v.Data.TotalAvailable) && (((v.Data.UnlimitedQuota == nil || !*v.Data.UnlimitedQuota) && *v.Data.TotalAvailable <= 0) || (v.Data.ExpiresAt > 0 && v.Data.ExpiresAt <= s.now().Unix())) {
		now = s.now()
		st.AuthEpisode, st.AuthEpisodeAt = "key_exhausted", &now
		z := 0.0
		return ubOutcome{success: true, amount: &z, reason: "key_exhausted", kind: "newapi"}
	}
	if (valid && v.Data.TotalAvailable != nil) || (resp.StatusCode == 200 && decoded && (string(v.Code) != "true" || newAPIAuthRejected(b))) || resp.StatusCode == 401 || resp.StatusCode == 403 {
		now = s.now()
		st.AuthEpisode, st.AuthEpisodeAt = "auth_failed", &now
	}
	return ubOutcome{reason: "auth_failed", kind: "newapi", status: resp.StatusCode}
}
