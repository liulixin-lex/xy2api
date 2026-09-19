package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
)

const codexTicketRuntimeKey = "codex_ticket_runtime"

var ErrCodexTicketConflict = errors.New("STATE configuration or lease changed")

type CodexTicketMutation func(*Account, int, time.Time) (bool, error)
type CodexTicketRepository interface {
	MutateCodexTicket(context.Context, int64, int, CodexTicketMutation) (*Account, error)
}

type CodexTicketEvent struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}
type codexTicketRuntime struct {
	AccountRetryAfter *time.Time         `json:"account_retry_after,omitempty"`
	RoundsStarted     int64              `json:"rounds_started"`
	RoundsSucceeded   int64              `json:"rounds_succeeded"`
	RoundsFailed      int64              `json:"rounds_failed"`
	LeaseToken        string             `json:"lease_token,omitempty"`
	LeaseUntil        *time.Time         `json:"lease_until,omitempty"`
	Revision          string             `json:"revision,omitempty"`
	Phase             string             `json:"phase,omitempty"`
	Attempts          int                `json:"attempts"`
	RetryAfter        *time.Time         `json:"retry_after,omitempty"`
	LastError         string             `json:"last_error,omitempty"`
	Requested         bool               `json:"requested"`
	TriggerCount      int64              `json:"trigger_count"`
	Events            []CodexTicketEvent `json:"events,omitempty"`
	Recoveries        []time.Time        `json:"recoveries,omitempty"`
	IQRecoveryAt      *time.Time         `json:"iq_recovery_at,omitempty"`
	IQRetest          string             `json:"iq_retest,omitempty"`
}

func codexTicketRuntimes(a *Account) map[string]codexTicketRuntime {
	out := map[string]codexTicketRuntime{}
	if a == nil {
		return out
	}
	b, e := json.Marshal(a.Extra[codexTicketRuntimeKey])
	if e == nil {
		_ = json.Unmarshal(b, &out)
	}
	if out == nil {
		out = map[string]codexTicketRuntime{}
	}
	return out
}
func codexTicketAccountRetryAfter(a *Account, now time.Time) *time.Time {
	var latest *time.Time
	for _, rt := range codexTicketRuntimes(a) {
		if rt.AccountRetryAfter != nil && rt.AccountRetryAfter.After(now) && (latest == nil || rt.AccountRetryAfter.After(*latest)) {
			stamp := *rt.AccountRetryAfter
			latest = &stamp
		}
	}
	return latest
}

func saveCodexTicketRuntime(a *Account, model string, rt codexTicketRuntime) {
	all := codexTicketRuntimes(a)
	all[model] = rt
	a.Extra = maps.Clone(a.Extra)
	if a.Extra == nil {
		a.Extra = map[string]any{}
	}
	a.Extra[codexTicketRuntimeKey] = all
}
func (r *codexTicketRuntime) event(now time.Time, reason string) {
	r.Events = append(r.Events, CodexTicketEvent{now, reason})
	if len(r.Events) > 20 {
		r.Events = r.Events[len(r.Events)-20:]
	}
}
func codexTicketClusterLimit() int {
	n, e := strconv.Atoi(os.Getenv("CODEX_TICKET_MAX_CONCURRENCY"))
	if e != nil || n < 1 || n > 10 {
		return 2
	}
	return n
}
func (s *OpenAIGatewayService) mutateCodexTicket(ctx context.Context, id int64, limit int, fn CodexTicketMutation) (*Account, error) {
	repo, ok := s.accountRepo.(CodexTicketRepository)
	if !ok {
		return nil, errors.New("STATE transactional repository unavailable")
	}
	return repo.MutateCodexTicket(ctx, id, limit, fn)
}
func codexTicketLeaseMatches(rt codexTicketRuntime, job *codexAccountTicketJob, now time.Time) bool {
	return rt.LeaseToken == job.leaseToken && rt.Revision == job.revision && rt.LeaseUntil != nil && rt.LeaseUntil.After(now)
}
func codexTicketRecoveryHistory(rt *codexTicketRuntime, now time.Time) {
	recent := make([]time.Time, 0, len(rt.Recoveries))
	for _, t := range rt.Recoveries {
		if t.After(now.Add(-15 * time.Minute)) {
			recent = append(recent, t)
		}
	}
	rt.Recoveries = recent
}

type codexTicketProbeError struct{ retryAt time.Time }

func (e *codexTicketProbeError) Error() string { return "upstream request was rejected" }
func codexTicketRetryAfter(raw string, now time.Time) time.Time {
	if seconds, e := strconv.ParseInt(raw, 10, 32); e == nil && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if t, e := http.ParseTime(raw); e == nil && t.After(now) {
		return t
	}
	return now
}

// InitializeNewCodexTicketAccount marks API-created accounts explicitly disabled,
// including accounts created while the first legacy-scope migration is pending.
func InitializeNewCodexTicketAccount(a *Account) {
	if !isOpenAICodexTicketAccount(a) {
		return
	}
	cfg := codexAccountTicketConfigOf(nil)
	cfg.Revision = uuid.NewString()
	a.Extra = maps.Clone(a.Extra)
	if a.Extra == nil {
		a.Extra = map[string]any{}
	}
	a.Extra[codexAccountTicketConfigKey] = cfg
}

// CodexTicketMigrationExtra retains the old global scope exactly once. Import and
// account creation do not call this function; private fields are stripped there.
func CodexTicketMigrationExtra(a *Account, cfg config.OpenAICodexTicketConfig) bool {
	if a == nil || !isOpenAICodexTicketAccount(a) || a.Extra[codexAccountTicketConfigKey] != nil {
		return false
	}
	models := append([]string(nil), cfg.Models...)
	if len(models) == 0 {
		models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	plan := "pro"
	if cfg.TargetLength == 332 {
		plan = "team"
	}
	policy := "allow_unprotected"
	if cfg.FailClosed {
		policy = "block"
	}
	if a.Extra == nil {
		a.Extra = map[string]any{}
	}
	a.Extra[codexAccountTicketConfigKey] = codexAccountTicketConfig{Enabled: cfg.Enabled, TicketPlan: plan, Models: models, Model: models[0], MissingPolicy: policy, Revision: uuid.NewString()}
	return true
}
func (s *OpenAIGatewayService) migrateCodexTicketAccounts(ctx context.Context) error {
	repo, ok := s.accountRepo.(interface {
		MigrateCodexTicketAccounts(context.Context, config.OpenAICodexTicketConfig) error
	})
	if !ok {
		return nil
	}
	cfg := s.openAICodexTicketConfig()
	cfg.Enabled = s.openAICodexTicketEnabledContext(ctx)
	return repo.MigrateCodexTicketAccounts(ctx, cfg)
}

type CodexTicketFence struct {
	Pool            string
	FallbackPool    string
	FallbackEnabled bool
}
type codexTicketFenceKey struct{}

// ContextWithCodexTicketFence binds the pool observed by a worker to its database transaction.
func ContextWithCodexTicketFence(ctx context.Context, fence CodexTicketFence) context.Context {
	return context.WithValue(ctx, codexTicketFenceKey{}, fence)
}

func CodexTicketFenceFromContext(ctx context.Context) (CodexTicketFence, bool) {
	f, ok := ctx.Value(codexTicketFenceKey{}).(CodexTicketFence)
	return f, ok
}
func (s *OpenAIGatewayService) codexTicketFencedContext(ctx context.Context, pool string) context.Context {
	cfg := s.openAICodexTicketConfig()
	return ContextWithCodexTicketFence(ctx, CodexTicketFence{pool, cfg.HarvestProxyURL, cfg.Enabled})
}

func (s *OpenAIGatewayService) codexTicketTransportFingerprint(a *Account) string {
	s.openaiCodexTransportMu.RLock()
	manager := s.pluginManager
	s.openaiCodexTransportMu.RUnlock()
	if manager == nil {
		return ""
	}
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	if !manager.ShouldRouteOpenAIOAuth(a) {
		return ""
	}
	route := manager.route.Load()
	if route == nil {
		return ""
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	identity := fmt.Sprintf("%d:%s", route.pluginID, route.unavailable)
	if route.runtime != nil && route.runtime.installation != nil {
		installation := route.runtime.installation
		identity += ":" + installation.BinarySHA256 + ":" + installation.ConfigEncrypted
	}
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

func OpenAICodexTicketSchedulingConfig(extra map[string]any) map[string]any {
	if extra[codexAccountTicketConfigKey] == nil {
		return nil
	}
	ac := codexAccountTicketConfigOf(&Account{Extra: extra})
	return map[string]any{"enabled": ac.Enabled, "models": ac.Models, "model": ac.Model, "ticket_plan": ac.TicketPlan, "missing_policy": ac.MissingPolicy, "revision": ac.Revision}
}
