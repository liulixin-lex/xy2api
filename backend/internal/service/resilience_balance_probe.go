package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const (
	balanceProbeInterval = 15 * time.Second
	// balanceProbeLockTTL must outlive the holder's work (the probe deadline
	// plus persisting the result), so a lock is never released by an
	// instance that no longer owns it.
	balanceProbeLockTTL      = 90 * time.Second
	balanceProbePersistLimit = 10 * time.Second
	balanceProbeHTTPTimeout  = 15 * time.Second
	balanceProbeGenTimeout   = 45 * time.Second
	balanceProbeMaxBodyBytes = 256 << 10
	// balancePositiveThreshold is the smallest balance that counts as funded.
	balancePositiveThreshold = 0.01
)

// Probe sources recorded in the balance record.
const (
	balanceProbeSourceUsage      = "sub2api_usage"
	balanceProbeSourceNewAPI     = "newapi_billing"
	balanceProbeSourceGeneration = "generation"
	balanceProbeSourceAdmin      = "admin"
)

// ResilienceRepository is the SQL side of the resilience layer.
type ResilienceRepository interface {
	// ListBalanceTrackedAccountIDs returns accounts with a non-OK balance
	// record or a balance pause.
	ListBalanceTrackedAccountIDs(ctx context.Context) ([]int64, error)
	// OutputTokenP99ByModel returns the P99 output tokens per requested
	// model since the given time, for models with at least minSamples
	// requests.
	OutputTokenP99ByModel(ctx context.Context, since time.Time, minSamples int) (map[string]int, error)
	// CountBalanceStates returns the number of accounts per balance state.
	CountBalanceStates(ctx context.Context) (map[string]int64, error)
}

// balanceProbeDeps are the collaborators of the balance probe loop.
type balanceProbeDeps struct {
	repo        ResilienceRepository
	accounts    AccountRepository
	accountTest *AccountTestService
	// probe runs one balance probe; tests replace it.
	probe func(ctx context.Context, account *Account) (resilience.ProbeVerdict, string, string, *balanceProbeAuthFailure)
}

// ConfigureBalanceProbe attaches the probe collaborators and registers the
// loop; it must be called before Start.
func (r *ResilienceService) ConfigureBalanceProbe(repo ResilienceRepository, accounts AccountRepository, accountTest *AccountTestService) {
	if r == nil || repo == nil || accounts == nil {
		return
	}
	r.probe = &balanceProbeDeps{repo: repo, accounts: accounts, accountTest: accountTest}
	r.probe.probe = r.probeBalance
	r.AddBackgroundLoop(r.balanceProbeLoop)
}

func (r *ResilienceService) balanceProbeLoop(ctx context.Context) {
	t := time.NewTicker(balanceProbeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if resilience.Current().BalanceOn() {
				r.runBalanceProbes(ctx)
			}
		}
	}
}

// runBalanceProbes advances every tracked account: due probes, ended
// RECOVERING windows, administrator overrides and long-unrecovered alerts.
func (r *ResilienceService) runBalanceProbes(ctx context.Context) {
	listCtx, cancel := context.WithTimeout(ctx, resilienceLoadTimeout)
	ids, err := r.probe.repo.ListBalanceTrackedAccountIDs(listCtx)
	cancel()
	if err != nil {
		slog.Warn("resilience.balance_list_failed", "error", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		r.advanceBalanceAccount(ctx, id, false)
	}
}

// RecheckBalance runs a probe immediately (administrator "topped up"). It
// returns the resulting record.
func (r *ResilienceService) RecheckBalance(ctx context.Context, accountID int64) (resilience.BalanceRecord, error) {
	if r == nil || r.probe == nil {
		return resilience.BalanceRecord{}, fmt.Errorf("balance probe is not configured")
	}
	r.advanceBalanceAccount(ctx, accountID, true)
	account, err := r.probe.accounts.GetByID(ctx, accountID)
	if err != nil {
		return resilience.BalanceRecord{}, err
	}
	return resilience.BalanceRecordFrom(account.Extra), nil
}

func (r *ResilienceService) advanceBalanceAccount(ctx context.Context, accountID int64, force bool) {
	cfg := resilience.Current()
	stateCtx, cancel := context.WithTimeout(ctx, balanceProbeGenTimeout+balanceProbeHTTPTimeout)
	defer cancel()
	account, err := r.probe.accounts.GetByID(stateCtx, accountID)
	if err != nil || account == nil {
		return
	}
	rec := resilience.BalanceRecordFrom(account.Extra)
	now := time.Now()
	pausedByBalance := account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) &&
		strings.HasPrefix(account.TempUnschedulableReason, "balance:")
	pausedByOther := account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) && !pausedByBalance

	switch rec.State {
	case resilience.BalanceOK:
		if pausedByBalance {
			// A stale balance pause without a record (manual edit): lift it.
			r.rateLimit.clearBalanceExclusion(stateCtx, account)
		}
		return
	case resilience.BalanceRecovering:
		if !now.Before(rec.RecoveringUntil) {
			after := resilience.BalanceRecord{State: resilience.BalanceOK, Since: now, LastProbeAt: rec.LastProbeAt, ProbeSource: rec.ProbeSource}
			r.rateLimit.persistBalanceRecord(stateCtx, account, rec, after, "recovering_window")
		} else {
			r.setRecovering(stateCtx, account.ID, rec.RecoveringUntil)
		}
		return
	}

	// EXHAUSTED or SUSPECT.
	// An administrator recovering the account clears the pause; a pause that
	// merely ran out (for example a longer 403 cooldown that kept the balance
	// pause from being written) keeps its timestamp and is re-applied below.
	cleared := account.TempUnschedulableUntil == nil
	if !force && cleared && now.Before(rec.ExclusionUntil(cfg)) && rec.State == resilience.BalanceExhausted {
		// The pause was lifted while the exclusion should still hold: an
		// administrator recovered the account. Respect it.
		after := resilience.BalanceRecord{State: resilience.BalanceOK, Since: now, LastProbeAt: rec.LastProbeAt, ProbeSource: balanceProbeSourceAdmin}
		r.rateLimit.persistBalanceRecord(stateCtx, account, rec, after, "admin_override")
		return
	}
	if rec.State == resilience.BalanceExhausted && !rec.Notified && cfg.Balance.NotifyAfterHours > 0 &&
		now.Sub(rec.Since) >= time.Duration(cfg.Balance.NotifyAfterHours)*time.Hour {
		slog.Warn("resilience.balance_unrecovered",
			"account_id", account.ID, "account_name", account.Name, "platform", account.Platform,
			"since", rec.Since, "level", rec.Level, "evidence", truncateString(rec.LastEvidence, 300))
		rec.Notified = true
		if err := r.probe.accounts.UpdateExtra(stateCtx, account.ID, rec.Extra()); err != nil {
			slog.Warn("resilience.balance_persist_failed", "account_id", account.ID, "error", err)
		}
	}
	if !force && !rec.Due(now) {
		// Keep the pause aligned with the schedule (it only ever extends).
		if rec.State == resilience.BalanceExhausted && !pausedByOther {
			_ = r.probe.accounts.SetTempUnschedulable(stateCtx, account.ID, rec.ExclusionUntil(cfg), rec.TempUnschedReason())
		}
		return
	}
	if r.store != nil {
		locked, err := r.store.TryLockBalanceProbe(stateCtx, account.ID, balanceProbeLockTTL)
		if err != nil || !locked {
			return
		}
		defer func() { _ = r.store.UnlockBalanceProbe(context.WithoutCancel(stateCtx), account.ID) }()
	}
	verdict, source, evidence, authFailure := r.probe.probe(stateCtx, account)
	// The probes may use the whole deadline; the result is written with its
	// own short one.
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(stateCtx), balanceProbePersistLimit)
	defer cancelPersist()
	if authFailure != nil {
		// The credentials are invalid: follow the authentication path.
		slog.Warn("resilience.balance_probe_auth_failure", "account_id", account.ID, "source", source)
		r.rateLimit.HandleUpstreamError(persistCtx, account, authFailure.status, http.Header{}, authFailure.body)
		return
	}
	after := resilience.OnProbe(rec, cfg, verdict, source, evidence, time.Now())
	r.rateLimit.persistBalanceRecord(persistCtx, account, rec, after, "probe:"+source)
}

type balanceProbeAuthFailure struct {
	status int
	body   []byte
}

// probeBalance tries the probes from cheapest to most expensive (design §3.2).
func (r *ResilienceService) probeBalance(ctx context.Context, account *Account) (resilience.ProbeVerdict, string, string, *balanceProbeAuthFailure) {
	if v, evidence, ok := r.probeSub2APIUsage(ctx, account); ok {
		return v, balanceProbeSourceUsage, evidence, nil
	}
	if v, evidence, ok := r.probeNewAPIBilling(ctx, account); ok {
		return v, balanceProbeSourceNewAPI, evidence, nil
	}
	return r.probeGeneration(ctx, account)
}

// probeBaseURL returns the relay base URL of an API-key account, or "" when
// the account talks to an official API (which has no relay billing endpoint).
func (r *ResilienceService) probeBaseURL(account *Account) string {
	if r.probe == nil || r.probe.accountTest == nil || r.probe.accountTest.httpUpstream == nil {
		return ""
	}
	if account.GetCredential("api_key") == "" {
		return ""
	}
	base := strings.TrimSpace(account.GetCredential("base_url"))
	if base == "" || upstreamBillingProbeTargetIsOfficialAPI(base) {
		return ""
	}
	normalized, err := r.probe.accountTest.validateUpstreamBaseURL(base)
	if err != nil {
		return ""
	}
	return normalized
}

func (r *ResilienceService) probeGET(ctx context.Context, account *Account, url string) (int, []byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, balanceProbeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, bytes.NewReader(nil))
	if err != nil {
		return 0, nil, err
	}
	reqCtx := WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileDefault)
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(reqCtx))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	account.ApplyHeaderOverrides(req.Header)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	var tlsProfile *tlsfingerprint.Profile
	if ts := r.probe.accountTest.tlsFPProfileService; ts != nil {
		tlsProfile = ts.ResolveTLSProfile(account)
	}
	resp, err := r.probe.accountTest.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, tlsProfile)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, balanceProbeMaxBodyBytes))
	return resp.StatusCode, body, nil
}

// probeSub2APIUsage reads GET {base}/v1/usage of a sub2api-style relay. It
// costs no inference. ok=false means the endpoint gave no conclusive answer.
func (r *ResilienceService) probeSub2APIUsage(ctx context.Context, account *Account) (resilience.ProbeVerdict, string, bool) {
	base := r.probeBaseURL(account)
	if base == "" {
		return resilience.ProbeUnknown, "", false
	}
	status, body, err := r.probeGET(ctx, account, buildOpenAIEndpointURL(base, "/v1/usage"))
	if err != nil || status != http.StatusOK || !gjson.ValidBytes(body) {
		return resilience.ProbeUnknown, "", false
	}
	return verdictFromUsageBody(body)
}

// verdictFromUsageBody interprets a sub2api /v1/usage response.
func verdictFromUsageBody(body []byte) (resilience.ProbeVerdict, string, bool) {
	parsed := gjson.ParseBytes(body)
	if strings.EqualFold(parsed.Get("status").String(), StatusAPIKeyQuotaExhausted) {
		return resilience.ProbeNegative, "usage status quota_exhausted", true
	}
	for _, field := range []string{"remaining", "balance", "quota.remaining"} {
		v := parsed.Get(field)
		if v.Exists() && (v.Type == gjson.Number || v.Type == gjson.String) {
			amount := v.Float()
			// A subscription without any limit reports remaining = -1.
			if field == "remaining" && amount == -1 && strings.EqualFold(parsed.Get("mode").String(), "unrestricted") {
				return resilience.ProbePositive, "usage unrestricted subscription without limits", true
			}
			evidence := fmt.Sprintf("usage %s=%.4f", field, amount)
			if amount > balancePositiveThreshold {
				return resilience.ProbePositive, evidence, true
			}
			return resilience.ProbeNegative, evidence, true
		}
	}
	return resilience.ProbeUnknown, "", false
}

// probeNewAPIBilling reads the new-api / one-api billing endpoints. Their
// semantics must be verified per relay before go-live (tasks 12.6); any
// response that does not parse is inconclusive.
func (r *ResilienceService) probeNewAPIBilling(ctx context.Context, account *Account) (resilience.ProbeVerdict, string, bool) {
	base := r.probeBaseURL(account)
	if base == "" {
		return resilience.ProbeUnknown, "", false
	}
	status, body, err := r.probeGET(ctx, account, buildOpenAIEndpointURL(base, "/v1/dashboard/billing/subscription"))
	if err != nil || status != http.StatusOK || !gjson.ValidBytes(body) {
		return resilience.ProbeUnknown, "", false
	}
	limit := gjson.GetBytes(body, "hard_limit_usd")
	if !limit.Exists() || limit.Type != gjson.Number {
		return resilience.ProbeUnknown, "", false
	}
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -99).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")
	status, usageBody, err := r.probeGET(ctx, account, buildOpenAIEndpointURL(base, "/v1/dashboard/billing/usage")+"?start_date="+start+"&end_date="+end)
	if err != nil || status != http.StatusOK || !gjson.ValidBytes(usageBody) {
		return resilience.ProbeUnknown, "", false
	}
	used := gjson.GetBytes(usageBody, "total_usage")
	if !used.Exists() || used.Type != gjson.Number {
		return resilience.ProbeUnknown, "", false
	}
	return verdictFromNewAPI(limit.Float(), used.Float())
}

// verdictFromNewAPI: hard_limit_usd is the key's total quota in USD and
// total_usage is in US cents.
func verdictFromNewAPI(hardLimitUSD, totalUsageCents float64) (resilience.ProbeVerdict, string, bool) {
	remaining := hardLimitUSD - totalUsageCents/100
	evidence := fmt.Sprintf("newapi hard_limit_usd=%.4f total_usage_cents=%.2f remaining=%.4f", hardLimitUSD, totalUsageCents, remaining)
	if hardLimitUSD <= 0 {
		return resilience.ProbeUnknown, "", false
	}
	if remaining > balancePositiveThreshold {
		return resilience.ProbePositive, evidence, true
	}
	return resilience.ProbeNegative, evidence, true
}

// probeGeneration runs the account test (one tiny streamed generation)
// within the daily budget.
func (r *ResilienceService) probeGeneration(ctx context.Context, account *Account) (resilience.ProbeVerdict, string, string, *balanceProbeAuthFailure) {
	cfg := resilience.Current()
	if r.probe == nil || r.probe.accountTest == nil || cfg.Balance.GenerationProbeDailyBudget <= 0 {
		return resilience.ProbeUnknown, balanceProbeSourceGeneration, "generation probe unavailable", nil
	}
	if r.store != nil {
		n, err := r.store.IncrGenerationProbe(ctx, account.ID, time.Now().UTC().Format("20060102"))
		if err != nil || n > int64(cfg.Balance.GenerationProbeDailyBudget) {
			return resilience.ProbeUnknown, balanceProbeSourceGeneration, "daily generation probe budget used", nil
		}
	}
	genCtx, cancel := context.WithTimeout(ctx, balanceProbeGenTimeout)
	defer cancel()
	result, err := r.probe.accountTest.RunTestBackground(genCtx, account.ID, "")
	if err != nil || result == nil {
		return resilience.ProbeUnknown, balanceProbeSourceGeneration, "generation probe failed to run", nil
	}
	if result.Status == "success" {
		return resilience.ProbePositive, balanceProbeSourceGeneration, "generation succeeded", nil
	}
	msg := result.ErrorMessage
	res := resilience.Classify(resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: http.StatusBadRequest, Message: msg}, cfg.Failure.BalanceKeywordsExtra)
	switch {
	case res.Category == resilience.CategoryBalance:
		return resilience.ProbeNegative, balanceProbeSourceGeneration, truncateString(msg, 300), nil
	case looksLikeInvalidCredentials(msg):
		return resilience.ProbeUnknown, balanceProbeSourceGeneration, truncateString(msg, 300),
			&balanceProbeAuthFailure{status: http.StatusUnauthorized, body: []byte(fmt.Sprintf(`{"error":{"message":%q}}`, truncateString(msg, 300)))}
	}
	return resilience.ProbeUnknown, balanceProbeSourceGeneration, truncateString(msg, 300), nil
}

func looksLikeInvalidCredentials(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "401") && (strings.Contains(m, "invalid") || strings.Contains(m, "unauthorized") || strings.Contains(m, "revoked"))
}
