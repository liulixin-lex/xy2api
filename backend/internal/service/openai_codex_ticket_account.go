package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	apperrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const codexAccountTicketConfigKey = "codex_ticket_config"
const codexTicketMaxAttempts = 8
const codexTicketRetryCooldown = 5 * time.Minute

const (
	codexTicketPlanPro  = "pro"
	codexTicketPlanTeam = "team"
)

// This is a manual account setting, never inferred from subscription metadata.
func codexTicketTargetLength(plan string) int {
	switch plan {
	case "", codexTicketPlanPro:
		return 292
	case codexTicketPlanTeam:
		return 332
	default:
		return 0
	}
}

// This key is server managed and is never accepted through general account edits.
type codexAccountTicketConfig struct {
	Models        []string `json:"models,omitempty"`
	MissingPolicy string   `json:"missing_policy,omitempty"`
	TicketPlan    string   `json:"ticket_plan"`
	Enabled       bool     `json:"enabled"`
	Model         string   `json:"model"`
	ProxyURL      string   `json:"proxy_url,omitempty"` // Legacy data only; harvesting always uses the global pool.
	Revision      string   `json:"revision"`
}

type CodexAccountTicketUpdate struct {
	Models        []string `json:"models,omitempty"`
	MissingPolicy string   `json:"missing_policy,omitempty"`
	TicketPlan    string   `json:"ticket_plan"`
	Enabled       bool     `json:"enabled"`
	ProxyURL      string   `json:"proxy_url"`
	Model         string   `json:"model"`
	ClearProxy    bool     `json:"clear_proxy"`
}

type CodexAccountTicketStatus struct {
	Counters             map[string]int64           `json:"counters"`
	Models               []string                   `json:"models,omitempty"`
	Tickets              []CodexAccountTicketStatus `json:"tickets,omitempty"`
	MissingPolicy        string                     `json:"missing_policy"`
	Protection           string                     `json:"protection"`
	ModelVerified        bool                       `json:"model_verified"`
	IQStatus             string                     `json:"iq_status"`
	IQRetest             string                     `json:"iq_retest"`
	Events               []CodexTicketEvent         `json:"events,omitempty"`
	Watchdog             CodexTicketWatchdogStatus  `json:"watchdog"`
	TicketPlan           string                     `json:"ticket_plan"`
	TargetLength         int                        `json:"target_length"`
	Enabled              bool                       `json:"enabled"`
	GlobalEnabled        bool                       `json:"global_enabled"`
	Model                string                     `json:"model"`
	ProxyConfigured      bool                       `json:"proxy_configured"`
	ProxyDisplay         string                     `json:"proxy_display"`
	FixedProxyConfigured bool                       `json:"fixed_proxy_configured"`
	State                string                     `json:"state"`
	TicketUsable         bool                       `json:"ticket_usable"`
	Refreshing           bool                       `json:"refreshing"`
	CapturedAt           *time.Time                 `json:"captured_at,omitempty"`
	RetryAfter           *time.Time                 `json:"retry_after,omitempty"`
	RemainingSeconds     int64                      `json:"remaining_seconds"`
	ExpiresAt            *time.Time                 `json:"expires_at,omitempty"`
	LastError            string                     `json:"last_error"`
	Attempts             int                        `json:"attempts"`
}

type codexAccountTicketJob struct {
	transportFingerprint string
	model                string
	leaseToken           string
	revision             string
	fixedFingerprint     string
	harvestProxyURL      string // Immutable global pool snapshot for this job; never returned to clients.
	cancel               context.CancelFunc
	done                 chan struct{}
	running              bool
	attempts             int
	lastError            string
	retryAfter           time.Time
}

func codexAccountTicketConfigOf(account *Account) codexAccountTicketConfig {
	out := codexAccountTicketConfig{MissingPolicy: "block", Models: []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}, Model: openAICodexTicketDefaultModel, TicketPlan: codexTicketPlanPro}
	if account == nil || account.Extra == nil {
		return out
	}
	if account.Extra[codexAccountTicketConfigKey] != nil {
		out.Models = nil
	}
	raw, err := json.Marshal(account.Extra[codexAccountTicketConfigKey])
	if err != nil {
		return out
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return codexAccountTicketConfig{Model: openAICodexTicketDefaultModel, TicketPlan: codexTicketPlanPro}
	}
	if out.Model == "" {
		out.Model = openAICodexTicketDefaultModel
	}
	if len(out.Models) == 0 {
		out.Models = []string{out.Model}
	}
	out.Model = out.Models[0]
	if out.MissingPolicy != "allow_unprotected" {
		out.MissingPolicy = "block"
	}
	if out.TicketPlan == "" {
		out.TicketPlan = codexTicketPlanPro
	}
	if codexTicketTargetLength(out.TicketPlan) == 0 {
		out.Enabled = false
	}
	// An incomplete or imported legacy blob must never opt an account in.
	if out.Revision == "" {
		out.Enabled = false
	}
	return out
}

func codexAccountTicketEligible(account *Account) bool {
	return isOpenAICodexTicketAccount(account) && account.Status == StatusActive && account.Proxy != nil && account.ProxyID != nil && (account.Proxy.Status == "" || account.Proxy.Status == StatusActive)
}

func codexTicketFixedProxyFingerprint(account *Account) string {
	if account == nil || account.Proxy == nil || account.ProxyID == nil {
		return ""
	}
	identity := map[string]any{"account_id": account.ID, "type": account.Type, "proxy_id": *account.ProxyID, "proxy": account.Proxy.URL(), "proxy_status": account.Proxy.Status, "identity_namespace": codexAccountIdentityNamespace(account)}
	for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id", "email", "client_id", "oauth_type", "openai_auth_type", "agent_identity", "header_override_enabled", "header_overrides", "user_agent", "device_id", "base_url"} {
		identity[key] = account.Credentials[key]
	}
	for _, key := range []string{codexFingerprintModeExtraKey, codexFingerprintSeedExtraKey, "enable_tls_fingerprint", "tls_fingerprint_profile_id", "openai_responses_mode", "openai_passthrough"} {
		identity[key] = account.Extra[key]
	}
	rawBytes, _ := json.Marshal(identity)
	raw := string(rawBytes)
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *OpenAIGatewayService) codexTicketLiveAccount(ctx context.Context, account *Account) (*Account, error) {
	if account == nil {
		return nil, errors.New("account unavailable")
	}
	if s.accountRepo == nil {
		return account, nil
	}
	// Repository lookup prevents stale scheduler snapshots from re-enabling disabled tickets.
	live, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || live == nil {
		return nil, errors.New("account unavailable")
	}
	return live, nil
}
func (s *OpenAIGatewayService) codexTicketAccountByID(ctx context.Context, id int64) (*Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, apperrors.New(503, "CODEX_TICKET_UNAVAILABLE", "Ticket service is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || account == nil {
		return nil, apperrors.New(404, "ACCOUNT_NOT_FOUND", "Account not found")
	}
	if !isOpenAICodexTicketAccount(account) {
		return nil, apperrors.BadRequest("CODEX_TICKET_ACCOUNT", "STATE tickets require a non-shadow OpenAI OAuth account")
	}
	return account, nil
}

func (s *OpenAIGatewayService) GetCodexAccountTicketStatus(ctx context.Context, id int64) (*CodexAccountTicketStatus, error) {
	a, e := s.codexTicketAccountByID(ctx, id)
	if e != nil {
		return nil, e
	}
	ac := codexAccountTicketConfigOf(a)
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	status := s.codexModelTicketStatus(a, ac, ac.Model, pool, s.openAICodexTicketEnabledContext(ctx))
	status.Models = slices.Clone(ac.Models)
	for _, model := range ac.Models {
		status.Tickets = append(status.Tickets, *s.codexModelTicketStatus(a, ac, model, pool, status.GlobalEnabled))
	}
	return status, nil
}
func (s *OpenAIGatewayService) codexModelTicketStatus(a *Account, ac codexAccountTicketConfig, model, pool string, global bool) *CodexAccountTicketStatus {
	st := &CodexAccountTicketStatus{TicketPlan: ac.TicketPlan, TargetLength: codexTicketTargetLength(ac.TicketPlan), Enabled: ac.Enabled, GlobalEnabled: global, Model: model, MissingPolicy: ac.MissingPolicy, ProxyConfigured: pool != "" && ValidateOpenAICodexTicketHarvestProxyURL(pool) == nil, FixedProxyConfigured: a.Proxy != nil && a.ProxyID != nil, State: "waiting", Protection: "paused", IQStatus: a.IQCheck.Status}
	if u, e := url.Parse(strings.ReplaceAll(pool, "{sid}", "%7Bsid%7D")); e == nil {
		st.ProxyDisplay = u.Host
	}
	rt := codexTicketRuntimes(a)[model]
	st.Counters = map[string]int64{"rounds_started": rt.RoundsStarted, "rounds_succeeded": rt.RoundsSucceeded, "rounds_failed": rt.RoundsFailed, "watchdog_triggers": rt.TriggerCount}
	st.Attempts = rt.Attempts
	st.LastError = rt.LastError
	st.RetryAfter = rt.RetryAfter
	if shared := codexTicketAccountRetryAfter(a, time.Now()); shared != nil && (st.RetryAfter == nil || shared.After(*st.RetryAfter)) {
		st.RetryAfter = shared
		if st.LastError == "" {
			st.LastError = "Account acquisition is cooling down after an authentication or rate-limit rejection"
		}
	}
	st.IQRetest = rt.IQRetest
	st.Events = rt.Events
	st.Watchdog.Enabled = ac.Enabled && global
	st.Watchdog.TriggerCount = rt.TriggerCount
	for i := len(rt.Events) - 1; i >= 0; i-- {
		ev := rt.Events[i]
		if ev.Reason == "model_mismatch" || ev.Reason == "state_312" || ev.Reason == "iq_degraded" {
			st.Watchdog.LastReason = ev.Reason
			st.Watchdog.LastTriggeredAt = &ev.At
			break
		}
	}
	if !ac.Enabled {
		st.State = "disabled"
		st.Protection = "disabled"
		return st
	}
	if !global {
		st.State = "global_disabled"
		st.Protection = "disabled"
		return st
	}
	now := time.Now()
	if t := s.lookupOpenAICodexTicket(a, model); t.validFor(a, ac, now) {
		st.State = "ready"
		st.TicketUsable = true
		st.ModelVerified = true
		st.CapturedAt = &t.CapturedAt
		st.ExpiresAt = &t.ExpiresAt
		st.RemainingSeconds = int64(t.ExpiresAt.Sub(now) / time.Second)
		st.Protection = "protected"
	}
	if rt.LeaseUntil != nil && rt.LeaseUntil.After(now) {
		st.State = "harvesting"
		st.Refreshing = st.TicketUsable
	} else if st.LastError != "" && !st.TicketUsable {
		st.State = "error"
	}
	if !st.TicketUsable && ac.MissingPolicy == "allow_unprotected" {
		st.Protection = "unprotected"
	}
	if !st.FixedProxyConfigured {
		st.State = "error"
		st.LastError = "Configure this account's fixed business proxy"
	}
	if !st.ProxyConfigured && !st.TicketUsable {
		st.State = "error"
		st.LastError = "Configure the global dynamic proxy pool"
	}
	if !codexAccountTicketEligible(a) {
		st.State = "error"
		st.TicketUsable = false
		st.ModelVerified = false
		st.Refreshing = false
		st.CapturedAt = nil
		st.ExpiresAt = nil
		st.RemainingSeconds = 0
		st.Protection = "paused"
		st.LastError = "Account must be active and have a fixed business proxy"
		if a.Status == StatusActive && !st.FixedProxyConfigured && ac.MissingPolicy == "allow_unprotected" {
			st.Protection = "unprotected"
			st.LastError = "Configure this account's fixed business proxy; forwarding without STATE"
		}
	}
	if !a.IQCheck.Enabled {
		st.IQStatus = "not_enabled"
		st.IQRetest = ""
	} else if s.openAICodexTicketOutboundModel(a, a.IQCheck.Profile().Model, false) != model {
		st.IQStatus = "not_configured"
		st.IQRetest = ""
	}
	return st
}
func (ac codexAccountTicketConfig) manages(model string) bool {
	return ac.Enabled && slices.Contains(ac.Models, normalizeOpenAICodexTicketModel(model))
}

func (s *OpenAIGatewayService) ConfigureCodexAccountTicket(ctx context.Context, id int64, input CodexAccountTicketUpdate) (*CodexAccountTicketStatus, error) {
	_, err := s.codexTicketAccountByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if input.ClearProxy || strings.TrimSpace(input.ProxyURL) != "" {
		return nil, apperrors.BadRequest("CODEX_TICKET_GLOBAL_PROXY", "Configure the dynamic proxy pool in gateway settings")
	}
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	changedConfig := false
	_, err = s.mutateCodexTicket(ctx, id, 0, func(a *Account, _ int, _ time.Time) (bool, error) {
		old := codexAccountTicketConfigOf(a)
		next := old
		next.Enabled = input.Enabled
		if input.TicketPlan != "" {
			next.TicketPlan = strings.ToLower(strings.TrimSpace(input.TicketPlan))
		}
		if next.TicketPlan != codexTicketPlanPro && next.TicketPlan != codexTicketPlanTeam {
			return false, apperrors.BadRequest("CODEX_TICKET_PLAN", "Ticket plan must be pro (292) or team (332)")
		}
		if input.Models != nil {
			next.Models = slices.Clone(input.Models)
		} else if input.Model != "" {
			next.Models = []string{input.Model}
		}
		if len(next.Models) == 0 || len(next.Models) > 20 {
			return false, apperrors.BadRequest("CODEX_TICKET_MODEL", "Select between 1 and 20 models")
		}
		seen := map[string]bool{}
		for _, m := range next.Models {
			if strings.TrimSpace(m) != m || m == "" || len(m) > 128 || seen[m] || (!slices.Contains(old.Models, m) && !slices.Contains(s.openAICodexTicketConfig().Models, m) && m != openAICodexTicketDefaultModel && m != openAICodexTicketDefaultSolModel) {
				return false, apperrors.BadRequest("CODEX_TICKET_MODEL", "Select a supported STATE model")
			}
			seen[m] = true
		}
		next.Model = next.Models[0]
		if input.MissingPolicy != "" {
			next.MissingPolicy = input.MissingPolicy
		}
		if next.MissingPolicy != "block" && next.MissingPolicy != "allow_unprotected" {
			return false, apperrors.BadRequest("CODEX_TICKET_POLICY", "Missing policy must be block or allow_unprotected")
		}
		changed := next.Enabled != old.Enabled || next.TicketPlan != old.TicketPlan || !slices.Equal(next.Models, old.Models) || old.Revision == ""
		if next.Enabled && (pool == "" || ValidateOpenAICodexTicketHarvestProxyURL(pool) != nil || a.Proxy == nil || a.ProxyID == nil) {
			return false, apperrors.BadRequest("CODEX_TICKET_PROXY_REQUIRED", "Configure the global dynamic proxy pool and this account fixed business proxy first")
		}
		next.ProxyURL = ""
		changedConfig = changed
		if a.Extra == nil {
			a.Extra = map[string]any{}
		}
		if changed {
			next.Revision = uuid.NewString()
			for k := range a.Extra {
				if strings.HasPrefix(k, openAICodexTicketExtraKeyPrefix) {
					delete(a.Extra, k)
				}
			}
			// Preserve leases, diagnostics and cooldowns across configuration edits.
			// A new revision fences publication without resetting account rate limits.
		}
		if a.Extra == nil {
			a.Extra = map[string]any{}
		}
		a.Extra[codexAccountTicketConfigKey] = next
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	s.openaiCodexAccountMu.Lock()
	if changedConfig {
		if job := s.openaiCodexAccountJobs[id]; job != nil && job.cancel != nil {
			job.cancel()
		}
		delete(s.openaiCodexAccountJobs, id)
	}
	s.openaiCodexAccountMu.Unlock()
	s.InvalidateAgentIdentityWSConnections(id)
	return s.GetCodexAccountTicketStatus(ctx, id)
}

func (s *OpenAIGatewayService) HarvestCodexAccountTicket(ctx context.Context, id int64) (*CodexAccountTicketStatus, error) {
	return s.HarvestCodexAccountTicketModel(ctx, id, "")
}
func (s *OpenAIGatewayService) HarvestCodexAccountTicketModel(ctx context.Context, id int64, model string) (*CodexAccountTicketStatus, error) {
	a, e := s.codexTicketAccountByID(ctx, id)
	if e != nil {
		return nil, e
	}
	ac := codexAccountTicketConfigOf(a)
	if !s.openAICodexTicketEnabledContext(ctx) {
		return nil, apperrors.BadRequest("CODEX_TICKET_GLOBAL_DISABLED", "Enable the gateway STATE master switch first")
	}
	if !ac.Enabled {
		return nil, apperrors.BadRequest("CODEX_TICKET_DISABLED", "Enable STATE tickets for this account first")
	}
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if pool == "" || ValidateOpenAICodexTicketHarvestProxyURL(pool) != nil {
		return nil, apperrors.BadRequest("CODEX_TICKET_GLOBAL_PROXY", "Configure the global dynamic proxy pool")
	}
	if !codexAccountTicketEligible(a) {
		return nil, apperrors.BadRequest("CODEX_TICKET_ACCOUNT_INACTIVE", "Account must be active and have a fixed business proxy")
	}
	if model != "" && !ac.manages(model) {
		return nil, apperrors.BadRequest("CODEX_TICKET_MODEL", "Model is not enabled for this account")
	}
	_, e = s.mutateCodexTicket(ctx, id, 0, func(live *Account, _ int, now time.Time) (bool, error) {
		cfg := codexAccountTicketConfigOf(live)
		if cfg.Revision != ac.Revision {
			return false, ErrCodexTicketConflict
		}
		for _, m := range cfg.Models {
			if model != "" && model != m {
				continue
			}
			rt := codexTicketRuntimes(live)[m]
			if rt.LeaseUntil == nil || !rt.LeaseUntil.After(now) {
				rt.Requested = true
				saveCodexTicketRuntime(live, m, rt)
			}
		}
		return true, nil
	})
	if e != nil {
		return nil, e
	}
	s.startCodexAccountTicketJob(ctx, id, true, model)
	return s.GetCodexAccountTicketStatus(ctx, id)
}

func (s *OpenAIGatewayService) cancelCodexTicketJobsLocked() {
	for _, job := range s.openaiCodexAccountJobs {
		if job.cancel != nil {
			job.cancel()
		}
	}
}
func (s *OpenAIGatewayService) cancelCodexTicketJobs() {
	s.openaiCodexAccountMu.Lock()
	defer s.openaiCodexAccountMu.Unlock()
	s.cancelCodexTicketJobsLocked()
}

func (s *OpenAIGatewayService) startCodexAccountTicketJob(ctx context.Context, id int64, manual bool, models ...string) *codexAccountTicketJob {
	if s == nil || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return nil
	}
	s.openaiCodexAccountMu.Lock()
	defer s.openaiCodexAccountMu.Unlock()
	if s.openaiCodexAccountStopping {
		return nil
	}
	if j := s.openaiCodexAccountJobs[id]; j != nil && j.running {
		return j
	}
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if pool == "" || ValidateOpenAICodexTicketHarvestProxyURL(pool) != nil {
		return nil
	}
	selected := ""
	if len(models) > 0 {
		selected = models[0]
	}
	limit := codexTicketClusterLimit()
	token := uuid.NewString()
	var job *codexAccountTicketJob
	_, err := s.mutateCodexTicket(s.codexTicketFencedContext(ctx, pool), id, limit, func(a *Account, active int, now time.Time) (bool, error) {
		if active >= limit || !codexAccountTicketEligible(a) || codexTicketAccountRetryAfter(a, now) != nil {
			return false, nil
		}
		if reason, _ := iqHealth(a, now); reason != "" {
			return false, nil
		}
		if a.IQCheck.LeaseUntil != nil && a.IQCheck.LeaseUntil.After(now) {
			return false, nil
		}
		ac := codexAccountTicketConfigOf(a)
		if !ac.Enabled {
			return false, nil
		}
		all := codexTicketRuntimes(a)
		for _, rt := range all {
			if rt.LeaseUntil != nil && rt.LeaseUntil.After(now) {
				return false, nil
			}
		}
		order := slices.Clone(ac.Models)
		// Explicit requests take precedence, but never bypass a durable cooldown.
		slices.SortStableFunc(order, func(a, b string) int {
			if all[a].Requested && !all[b].Requested {
				return -1
			}
			if !all[a].Requested && all[b].Requested {
				return 1
			}
			return 0
		})
		for _, m := range order {
			if selected != "" && selected != m {
				continue
			}
			rt := all[m]
			if rt.RetryAfter != nil && rt.RetryAfter.After(now) {
				continue
			}
			ticket := s.lookupOpenAICodexTicket(a, m)
			if !manual && !rt.Requested && ticket.validFor(a, ac, now) && !ticket.needsRefresh(now, time.Duration(s.openAICodexTicketConfig().RefreshBeforeSeconds)*time.Second) {
				continue
			}
			until := now.Add(time.Duration(2*s.openAICodexTicketConfig().HarvestAttemptTimeoutSeconds+60) * time.Second)
			rt.LeaseToken = token
			rt.LeaseUntil = &until
			rt.Revision = ac.Revision
			rt.Phase = "harvesting"
			rt.Attempts = 0
			rt.RoundsStarted++
			rt.Requested = false
			saveCodexTicketRuntime(a, m, rt)
			job = &codexAccountTicketJob{transportFingerprint: s.codexTicketTransportFingerprint(a), model: m, leaseToken: token, revision: ac.Revision, fixedFingerprint: codexTicketFixedProxyFingerprint(a), harvestProxyURL: pool, done: make(chan struct{}), running: true}
			return true, nil
		}
		return false, nil
	})
	if err != nil || job == nil {
		return nil
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	parent := s.openaiCodexTicketContext
	s.openaiCodexTicketLifecycleMu.Unlock()
	if parent == nil {
		parent = context.WithoutCancel(ctx)
	}
	jobCtx, cancel := context.WithCancel(parent)
	job.cancel = cancel
	if s.openaiCodexAccountJobs == nil {
		s.openaiCodexAccountJobs = map[int64]*codexAccountTicketJob{}
	}
	s.openaiCodexAccountJobs[id] = job
	s.openaiCodexAccountWG.Add(1)
	go func() {
		defer cancel()
		defer s.openaiCodexAccountWG.Done()
		defer close(job.done)
		s.runCodexAccountTicketJob(jobCtx, id, job)
	}()
	return job
}

func (s *OpenAIGatewayService) runCodexAccountTicketJob(ctx context.Context, id int64, job *codexAccountTicketJob) {
	lastError := "Unable to obtain a verified STATE ticket"
	var retryAfter time.Time
	accountRejected := false
	defer func() {
		finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		poolChanged := s.openAICodexTicketHarvestProxyURLContext(finishCtx) != job.harvestProxyURL
		_, _ = s.mutateCodexTicket(finishCtx, id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
			rt := codexTicketRuntimes(a)[job.model]
			if rt.LeaseToken != job.leaseToken || rt.Revision != job.revision {
				return false, nil
			}
			rt.LeaseToken = ""
			rt.LeaseUntil = nil
			rt.Phase = "idle"
			rt.LastError = lastError
			next := now.Add(codexTicketRetryCooldown)
			if retryAfter.After(next) {
				next = retryAfter
			}
			if _, health := iqHealth(a, now); health.After(next) {
				next = health
			}
			if accountRejected && (rt.AccountRetryAfter == nil || next.After(*rt.AccountRetryAfter)) {
				rt.AccountRetryAfter = &next
			}
			if retryAfter.IsZero() && poolChanged {
				rt.LastError = ""
				rt.RetryAfter = nil
				saveCodexTicketRuntime(a, job.model, rt)
				return true, nil
			}
			if codexAccountTicketConfigOf(a).Revision != job.revision {
				rt.LastError = ""
				rt.RetryAfter = nil
				saveCodexTicketRuntime(a, job.model, rt)
				return true, nil
			}
			if lastError != "" {
				rt.RetryAfter = &next
				rt.Phase = "cooldown"
				rt.RoundsFailed++
				rt.event(now, "acquisition_failed")
			} else {
				rt.RetryAfter = nil
			}
			saveCodexTicketRuntime(a, job.model, rt)
			return true, nil
		})
		s.openaiCodexAccountMu.Lock()
		job.running = false
		job.lastError = lastError
		job.retryAfter = retryAfter
		s.openaiCodexAccountMu.Unlock()
	}()
	timeout := time.Duration(s.openAICodexTicketConfig().HarvestAttemptTimeoutSeconds) * time.Second
	for attempt := 1; attempt <= codexTicketMaxAttempts; attempt++ {
		if ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) || s.openAICodexTicketHarvestProxyURLContext(ctx) != job.harvestProxyURL {
			return
		}
		account, err := s.mutateCodexTicket(s.codexTicketFencedContext(ctx, job.harvestProxyURL), id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
			ac := codexAccountTicketConfigOf(a)
			rt := codexTicketRuntimes(a)[job.model]
			if !codexTicketLeaseMatches(rt, job, now) || !ac.manages(job.model) || ac.Revision != job.revision || !codexAccountTicketEligible(a) || codexTicketFixedProxyFingerprint(a) != job.fixedFingerprint {
				return false, ErrCodexTicketConflict
			}
			if reason, next := iqHealth(a, now); reason != "" {
				retryAfter = next
				return false, ErrCodexTicketConflict
			}
			until := now.Add(2*timeout + time.Minute)
			rt.LeaseUntil = &until
			rt.Attempts = attempt
			saveCodexTicketRuntime(a, job.model, rt)
			return true, nil
		})
		if err != nil {
			return
		}
		if s.codexTicketTransportFingerprint(account) != job.transportFingerprint {
			return
		}
		ac := codexAccountTicketConfigOf(account)
		account.Extra = maps.Clone(account.Extra)
		account.Credentials = maps.Clone(account.Credentials)
		s.openaiCodexAccountMu.Lock()
		job.attempts = attempt
		s.openaiCodexAccountMu.Unlock()
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 2*timeout+30*time.Second)
		defer attemptCancel()
		token, _, err := s.GetAccessToken(attemptCtx, account)
		if err != nil || (token == "" && !account.IsOpenAIAgentIdentity()) {
			lastError = "Account authentication failed"
			return
		}
		// Reverify a legacy ticket without extending its original expiry.
		legacy := parseOpenAICodexTicketFromAny(id, job.model, account.Extra[openAICodexTicketExtraKey(job.model)])
		legacyCandidate := attempt == 1 && legacy != nil && !legacy.Verified && legacy.ExpiresAt.After(time.Now()) && legacy.Length == codexTicketTargetLength(ac.TicketPlan) && validCodexTicketState(legacy.State)
		state := ""
		status := 0
		if legacyCandidate {
			state = legacy.State
		} else {
			state, status, err = s.fireCodexAccountTicketProbe(attemptCtx, account, token, job.model, freshCodexTicketProxyURL(job.harvestProxyURL), "", timeout)
			if pe := new(codexTicketProbeError); errors.As(err, &pe) {
				retryAfter = pe.retryAt
			}
			if reason := codexTicketProbeRejection(status); reason != "" {
				accountRejected = true
				lastError = reason
				return
			}
		}
		if legacyCandidate || (err == nil && status == http.StatusOK && validCodexTicketState(state) && len(state) == codexTicketTargetLength(ac.TicketPlan)) {
			replay, replayStatus, replayErr := s.fireCodexAccountTicketProbe(attemptCtx, account, token, job.model, account.Proxy.URL(), state, timeout)
			if pe := new(codexTicketProbeError); errors.As(replayErr, &pe) {
				retryAfter = pe.retryAt
			}
			if reason := codexTicketProbeRejection(replayStatus); reason != "" {
				accountRejected = true
				lastError = reason
				return
			}
			if attemptCtx.Err() == nil && replayErr == nil && replayStatus == http.StatusOK && (len(replay) != 312 || !validCodexTicketState(replay)) {
				if !s.openAICodexTicketEnabledContext(ctx) || s.openAICodexTicketHarvestProxyURLContext(ctx) != job.harvestProxyURL {
					return
				}
				_, err = s.mutateCodexTicket(s.codexTicketFencedContext(ctx, job.harvestProxyURL), id, 0, func(live *Account, _ int, now time.Time) (bool, error) {
					rt := codexTicketRuntimes(live)[job.model]
					cfg := codexAccountTicketConfigOf(live)
					if !codexTicketLeaseMatches(rt, job, now) || !cfg.manages(job.model) || cfg.Revision != job.revision || codexTicketFixedProxyFingerprint(live) != job.fixedFingerprint {
						return false, ErrCodexTicketConflict
					}
					if reason, _ := iqHealth(live, now); reason != "" {
						return false, ErrCodexTicketConflict
					}
					expires := now.Add(time.Duration(s.openAICodexTicketConfig().TTLSeconds) * time.Second)
					captured := now
					if legacyCandidate {
						expires = legacy.ExpiresAt
						captured = legacy.CapturedAt
						if !expires.After(now) {
							return false, ErrCodexTicketConflict
						}
					}
					previous := s.lookupOpenAICodexTicket(live, job.model)
					if s.codexTicketTransportFingerprint(live) != job.transportFingerprint {
						return false, ErrCodexTicketConflict
					}
					ticket := &openAICodexTicket{TransportFingerprint: job.transportFingerprint, AccountID: id, Model: job.model, State: state, Length: len(state), CapturedAt: captured, ExpiresAt: expires, Attempts: attempt, Verified: true, ConfigRevision: job.revision, FixedProxyFingerprint: job.fixedFingerprint}
					live.Extra[openAICodexTicketExtraKey(job.model)] = ticket
					if !previous.validFor(live, cfg, now) {
						codexTicketRecoveryHistory(&rt, now)
						rt.Recoveries = append(rt.Recoveries, now)
						s.queueCodexIQRetest(live, job.model, &rt, now)
					}
					rt.RoundsSucceeded++
					rt.event(now, "verified")
					rt.LastError = ""
					rt.RetryAfter = nil
					saveCodexTicketRuntime(live, job.model, rt)
					return true, nil
				})
				if err == nil {
					lastError = ""
				} else {
					lastError = "Could not publish verified STATE"
				}
				return
			}
			lastError = "STATE did not preserve the target model on this account's fixed proxy"
		} else {
			lastError = "Harvest did not return a completed target-model response and matching STATE"
		}
		if attempt < codexTicketMaxAttempts {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (s *OpenAIGatewayService) queueCodexIQRetest(a *Account, model string, rt *codexTicketRuntime, now time.Time) {
	if !a.IQCheck.Enabled || s.openAICodexTicketOutboundModel(a, a.IQCheck.Profile().Model, false) != model {
		return
	}
	// Keep the last effective verdict. Advancing the revision fences late IQ workers.
	a.IQCheck.Revision = uuid.NewString()
	a.IQCheck.NextRunAt = &now
	a.IQCheck.ExecutionState = "queued"
	a.IQCheck.ExecutionReason = "state_recovered"
	a.IQCheck.TaskID = uuid.NewString()
	rt.IQRetest = "queued"
}

// Account authentication, access and rate-limit rejections end the entire round.
// Rotating harvest exits cannot resolve these reliably; retain any still-valid
// ticket and use the existing failure cooldown instead of spending more probes.
func codexTicketProbeRejection(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "Upstream rejected authentication (HTTP 401); acquisition paused for cooldown"
	case http.StatusForbidden:
		return "Upstream denied access (HTTP 403); acquisition paused for cooldown"
	case http.StatusTooManyRequests:
		return "Upstream rate limit (HTTP 429); acquisition paused for cooldown"
	default:
		return ""
	}
}

var codexTicketSIDPattern = regexp.MustCompile(`(?i)-sid-[^-]+(-t-[0-9]+)`)

func freshCodexTicketProxyURL(raw string) string {
	parsed, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || parsed.User == nil {
		return raw
	}
	username := parsed.User.Username()
	sid := strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if strings.Contains(username, "{sid}") {
		username = strings.ReplaceAll(username, "{sid}", sid)
	} else if strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".1024proxy.io") || strings.EqualFold(parsed.Hostname(), "1024proxy.io") {
		username = codexTicketSIDPattern.ReplaceAllString(username, "-sid-"+sid+"${1}")
	}
	if password, ok := parsed.User.Password(); ok {
		parsed.User = url.UserPassword(username, password)
	} else {
		parsed.User = url.User(username)
	}
	return parsed.String()
}
