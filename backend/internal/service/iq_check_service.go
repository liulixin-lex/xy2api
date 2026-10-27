package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
)

var ErrIQCheckInvalid = infraerrors.BadRequest("IQ_CHECK_INVALID", "Invalid OpenAI IQ check settings")

type iqProbeProfile struct {
	Model           string
	ReasoningEffort string
}

type iqProbeRequest struct {
	TimeoutSeconds int
	TargetModel    string
	TargetEffort   string
	Challenge      iqcheck.Challenge
	Revision       string
	Profile        iqProbeProfile
	Protocol       string
}

type IQTargetClaim struct {
	ID, AccountID                 int64
	Model, Revision, LeaseToken   string
	Enabled                       bool
	ManualRequested               bool
	ManualRequestedAt             *time.Time
	ReasoningEffort               string
	TimeoutSeconds                int
	Verdict                       string
	LeaseUntil                    time.Time
	Mode                          string
	OutboundModel                 string
	DegradedStreak, FailureStreak int
	TicketIdentities              []IQTicketIdentity
}

type IQTicketIdentity struct {
	StateTicketID    string
	StateFingerprint string
	StateRevision    string
	StateModel       string
	StateProtection  string
}
type IQTargetRepository interface {
	ListIQTargetSummaries(context.Context, []int64) (map[int64][]domain.IQTargetSummary, error)
	ClaimIQTargets(context.Context, time.Time, int) ([]IQTargetClaim, error)
	AddIQTargetAttempt(context.Context, IQTargetClaim, int, string, iqcheck.Challenge, iqcheck.Result, time.Time, time.Time) error
	CompleteIQTarget(context.Context, IQTargetClaim, iqcheck.Result, time.Time) (bool, error)
	SetIQTargetOutboundModel(context.Context, IQTargetClaim, string) (bool, error)
	DeferIQTarget(context.Context, IQTargetClaim, string, time.Time) error
	CleanupIQResults(context.Context, time.Time, int) (int64, error)
	ListIQResults(context.Context, int64, string, int64, int) (IQResultsPage, error)
	QueueIQTargets(context.Context, int64, []string) ([]IQRunTarget, error)
	ReconcileIQGates(context.Context) error
	ListIQTargetViews(context.Context, int64) ([]domain.IQTargetView, error)
	SaveIQTargets(context.Context, int64, domain.IQTargetsPatch) error
}

type IQResultsPage struct {
	Rounds       []IQRound `json:"rounds"`
	NextBeforeID *int64    `json:"next_before_id"`
}
type IQRunTarget struct {
	Model   string `json:"model"`
	State   string `json:"state"`
	RoundID *int64 `json:"round_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
type IQRunAccount struct {
	AccountID int64         `json:"account_id"`
	Reason    string        `json:"reason,omitempty"`
	Targets   []IQRunTarget `json:"targets"`
}
type IQRunRequest struct {
	Models []string `json:"models,omitempty"`
}
type IQRunBatchRequest struct {
	AccountIDs []int64  `json:"account_ids"`
	Models     []string `json:"models,omitempty"`
}

func (s *IQCheckService) TargetViews(ctx context.Context, id int64) ([]domain.IQTargetView, error) {
	if s == nil || s.targetRepo == nil {
		return nil, ErrIQCheckInvalid
	}
	views, err := s.targetRepo.ListIQTargetViews(ctx, id)
	if err == nil && views == nil {
		views = []domain.IQTargetView{}
	}
	return views, err
}
func (s *IQCheckService) SaveTargets(ctx context.Context, id int64, patch domain.IQTargetsPatch) error {
	if s == nil || s.targetRepo == nil {
		return ErrIQCheckInvalid
	}
	return s.targetRepo.SaveIQTargets(ctx, id, patch)
}

func (s *IQCheckService) RequestRun(ctx context.Context, id int64, models []string) ([]IQRunTarget, error) {
	if s == nil || s.targetRepo == nil {
		return nil, ErrIQCheckInvalid
	}
	if s.accounts == nil {
		return nil, ErrIQCheckInvalid
	}
	a, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Platform != PlatformOpenAI || a.IsCredentialShadow() {
		return rejectedIQTargets(models, "not_openai"), nil
	}
	if !iqProbeCredentialsAvailable(a, s.tokens != nil) {
		return rejectedIQTargets(models, "credentials_unavailable"), nil
	}
	models = append([]string(nil), models...)
	requestedForDefaults := models
	if len(requestedForDefaults) == 0 {
		requestedForDefaults = domain.DefaultIQModels
	}
	defaults := make([]domain.IQTargetSettings, 0, len(requestedForDefaults))
	for _, model := range requestedForDefaults {
		if model == domain.IQModelAstra || model == domain.IQModelSol {
			defaults = append(defaults, domain.IQTargetSettings{Model: model})
		}
	}
	if len(defaults) > 0 {
		if err := s.targetRepo.SaveIQTargets(ctx, id, domain.IQTargetsPatch{Targets: defaults}); err != nil {
			return nil, err
		}
	}
	out, err := s.targetRepo.QueueIQTargets(ctx, id, models)
	if err == nil {
		s.notify()
	}
	return out, err
}
func (s *IQCheckService) RequestRunBatch(ctx context.Context, ids []int64, models []string) []IQRunAccount {
	out := make([]IQRunAccount, 0, len(ids))
	for _, id := range ids {
		targets, err := s.RequestRun(ctx, id, models)
		if err != nil {
			out = append(out, IQRunAccount{AccountID: id, Reason: runReason(err), Targets: []IQRunTarget{}})
		} else if len(targets) > 0 && targets[0].State == "rejected" {
			out = append(out, IQRunAccount{AccountID: id, Reason: targets[0].Reason, Targets: []IQRunTarget{}})
		} else {
			if targets == nil {
				targets = []IQRunTarget{}
			}
			out = append(out, IQRunAccount{AccountID: id, Targets: targets})
		}
	}
	return out
}
func runReason(err error) string {
	if errors.Is(err, ErrAccountNotFound) {
		return "not_found"
	}
	if errors.Is(err, ErrIQCheckInvalid) {
		return "not_openai"
	}
	return "credentials_unavailable"
}

func rejectedIQTargets(models []string, reason string) []IQRunTarget {
	if len(models) == 0 {
		models = domain.DefaultIQModels
	}
	out := make([]IQRunTarget, 0, len(models))
	for _, model := range models {
		out = append(out, IQRunTarget{Model: model, State: "rejected", Reason: reason})
	}
	return out
}

func iqProbeCredentialsAvailable(account *Account, canRefresh bool) bool {
	if account == nil || account.IsCredentialShadow() {
		return false
	}
	if account.IsOpenAIAgentIdentity() {
		_, err := agentIdentityPrivateKey(account)
		return err == nil
	}
	if account.IsOAuth() {
		return strings.TrimSpace(account.GetOpenAIAccessToken()) != "" || canRefresh && strings.TrimSpace(account.GetOpenAIRefreshToken()) != ""
	}
	return strings.TrimSpace(account.GetOpenAIProtocolAPIKey()) != ""
}

type IQRound struct {
	ID            int64       `json:"id"`
	Model         string      `json:"model"`
	OutboundModel string      `json:"outbound_model"`
	Mode          string      `json:"mode"`
	Effort        string      `json:"effort"`
	Status        string      `json:"status"`
	Reason        string      `json:"reason"`
	StartedAt     time.Time   `json:"started_at"`
	FinishedAt    *time.Time  `json:"finished_at"`
	PromptVersion string      `json:"prompt_version"`
	Attempts      []IQAttempt `json:"attempts"`
}
type IQAttempt struct {
	AttemptNo     int             `json:"attempt_no"`
	Effort        string          `json:"effort"`
	Challenge     json.RawMessage `json:"challenge"`
	Expected      string          `json:"expected"`
	Answer        string          `json:"answer"`
	ReportedModel string          `json:"reported_model"`
	ModelClass    string          `json:"model_class"`
	Status        string          `json:"status"`
	Reason        string          `json:"reason"`
	LatencyMS     *int64          `json:"latency_ms"`
	Diagnostic    json.RawMessage `json:"diagnostic"`
}

func (s *IQCheckService) ProbeTarget(ctx context.Context, claim IQTargetClaim) iqcheck.Result {
	account, err := s.accounts.GetByID(ctx, claim.AccountID)
	if err != nil {
		return iqcheck.Unknown("account_unavailable")
	}
	model := CanonicalIQOutboundModel(account, claim.Model)
	return s.probe(ctx, claim.AccountID, iqProbeRequest{Revision: claim.Revision, TimeoutSeconds: claim.TimeoutSeconds, TargetModel: model, TargetEffort: claim.ReasoningEffort, Challenge: iqcheck.NewChallenge(), Profile: iqProbeProfile{Model: model, ReasoningEffort: claim.ReasoningEffort}})
}

func applyIQProbeRequestIdentity(req *http.Request, account *Account) error {
	apiKey := account.Type == AccountTypeAPIKey
	var ids *codexFingerprintIDs
	if !apiKey {
		ids = resolveCodexFingerprintIDsFromRequest(account, nil)
		if ids == nil {
			return nil
		}
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}
	_ = req.Body.Close()
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if apiKey {
		session := uuid.NewString()
		payload["prompt_cache_key"] = session
		for _, name := range []string{"session_id", "conversation_id", "session-id", "x-session-id", "x-session-affinity", "x-opencode-session", "x-conversation-id", "thread-id", "x-client-request-id", "x-codex-window-id", "previous_response_id", "previous-response-id", "x-codex-turn-state", "x-codex-turn-metadata", "idempotency-key", "x-idempotency-key"} {
			for key := range req.Header {
				if strings.EqualFold(key, name) {
					delete(req.Header, key)
				}
			}
		}
		for _, name := range []string{"session_id", "conversation_id", "session-id", "x-session-id", "x-session-affinity", "x-conversation-id"} {
			req.Header.Set(name, session)
		}
	} else {
		applyCodexFingerprintHeaders(req.Header, ids)
		applyCodexFingerprintClientMetadata(payload, ids)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(encoded))
	req.ContentLength = int64(len(encoded))
	req.GetBody = nil
	return nil
}

type iqStatusContextKey struct{}

func WithIQStatusFilter(ctx context.Context, status string) context.Context {
	return context.WithValue(ctx, iqStatusContextKey{}, status)
}
func IQStatusFilter(ctx context.Context) string {
	value, _ := ctx.Value(iqStatusContextKey{}).(string)
	return value
}
func ValidIQStatusFilter(status string) bool {
	return status == "" || status == "smart" || status == "degraded" || status == "unknown"
}

type IQCheckService struct {
	concurrency    *ConcurrencyService
	maxConcurrency int
	accounts       AccountRepository
	tester         *AccountTestService
	tokens         *OpenAITokenProvider
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	wake           chan struct{}
	modelsMu       sync.Mutex
	modelsCache    map[string]IQModelCatalog
	modelsFlight   singleflight.Group
	targetRepo     IQTargetRepository
}

func (s *IQCheckService) ResultsPage(ctx context.Context, id int64, model string, beforeID int64, limit int) (IQResultsPage, error) {
	if s == nil || s.targetRepo == nil {
		return IQResultsPage{}, ErrIQCheckInvalid
	}
	page, err := s.targetRepo.ListIQResults(ctx, id, model, beforeID, limit)
	if err == nil {
		normalizeIQResultsPage(&page)
	}
	return page, err
}

func normalizeIQResultsPage(page *IQResultsPage) {
	if page.Rounds == nil {
		page.Rounds = []IQRound{}
	}
	for i := range page.Rounds {
		if page.Rounds[i].Attempts == nil {
			page.Rounds[i].Attempts = []IQAttempt{}
		}
		for j := range page.Rounds[i].Attempts {
			answer := iqcheck.ExtractAnswer(page.Rounds[i].Attempts[j].Answer).Value
			if len(answer) > 64 {
				answer = ""
			}
			page.Rounds[i].Attempts[j].Answer = answer
		}
	}
}

func (s *IQCheckService) Diagnostics(ctx context.Context, id int64) (any, error) {
	if s == nil || s.targetRepo == nil {
		return nil, ErrIQCheckInvalid
	}
	targets, err := s.targetRepo.ListIQTargetViews(ctx, id)
	if err != nil {
		return nil, err
	}
	type diagnosticTarget struct {
		Model  string    `json:"model"`
		Rounds []IQRound `json:"rounds"`
	}
	out := struct {
		Version int                `json:"version"`
		Targets []diagnosticTarget `json:"targets"`
	}{Version: 3, Targets: make([]diagnosticTarget, 0, len(targets))}
	for _, target := range targets {
		page, err := s.targetRepo.ListIQResults(ctx, id, target.Model, 0, 100)
		if err != nil {
			return nil, err
		}
		normalizeIQResultsPage(&page)
		out.Targets = append(out.Targets, diagnosticTarget{Model: target.Model, Rounds: page.Rounds})
	}
	return out, nil
}

func ProvideIQCheckService(accounts AccountRepository, tester *AccountTestService, tokens *OpenAITokenProvider, concurrency *ConcurrencyService) (*IQCheckService, error) {
	targetRepo, ok := accounts.(IQTargetRepository)
	s := &IQCheckService{wake: make(chan struct{}, 1), concurrency: concurrency, maxConcurrency: iqTargetMaxConcurrency(), accounts: accounts, tester: tester, tokens: tokens}
	if !ok {
		return s, nil
	}
	s.targetRepo = targetRepo
	if err := targetRepo.ReconcileIQGates(context.Background()); err != nil {
		return nil, fmt.Errorf("reconcile IQ gates: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runTargets(ctx)
	}()
	return s, nil
}

func (s *IQCheckService) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
}

func (s *IQCheckService) runTargets(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(10 * time.Minute)
	defer cleanup.Stop()
	for {
		claims, err := s.targetRepo.ClaimIQTargets(ctx, time.Now().UTC(), s.maxConcurrency)
		if err == nil {
			for _, c := range claims {
				s.wg.Add(1)
				go func(c IQTargetClaim) { defer s.wg.Done(); s.executeTarget(ctx, c) }(c)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		case <-cleanup.C:
			if _, e := s.targetRepo.CleanupIQResults(ctx, time.Now().UTC().Add(-7*24*time.Hour), 5000); e != nil {
				logger.LegacyPrintf("iq_check", "cleanup failed: %v", e)
			}
		}
	}
}
func (s *IQCheckService) executeTarget(ctx context.Context, c IQTargetClaim) {
	defer s.notify()
	startedAt := time.Now().UTC()
	leaseDeadline := c.LeaseUntil
	if leaseDeadline.IsZero() {
		leaseDeadline = startedAt.Add(time.Duration(5*c.TimeoutSeconds+120) * time.Second)
	}
	roundCtx, cancelRound := context.WithDeadline(ctx, leaseDeadline)
	defer cancelRound()
	account, err := s.accounts.GetByID(roundCtx, c.AccountID)
	if err != nil {
		if ctx.Err() == nil {
			s.deferTarget(ctx, c, "account_unavailable", time.Now().UTC().Add(time.Minute))
		}
		return
	}
	now := time.Now().UTC()
	if !c.ManualRequested {
		if reason, next := accountProbeHealth(account, now); reason != "" {
			s.deferTarget(ctx, c, reason, next)
			return
		}
	}
	c.OutboundModel = CanonicalIQOutboundModel(account, c.Model)
	if ok, snapshotErr := s.targetRepo.SetIQTargetOutboundModel(roundCtx, c, c.OutboundModel); snapshotErr != nil || !ok {
		return
	}
	if s.concurrency == nil || s.concurrency.cache == nil {
		s.deferTarget(ctx, c, "concurrency_unavailable", now.Add(time.Minute))
		return
	}
	if s.tester != nil && s.tester.openaiGatewayService != nil {
		if reason, next := s.tester.openaiGatewayService.codexTicketIQWait(roundCtx, account, c.OutboundModel); reason != "" {
			s.deferTarget(ctx, c, reason, next)
			return
		}
	}
	outcomes := make([]iqcheck.Outcome, 0, iqcheck.MaxAttemptsPerRound)
	judgedReasons := make([]string, 0, iqcheck.MaxAttemptsPerRound)
	var finalResult iqcheck.Result
	var retryAfter *time.Time
	retryAfterUnbounded := false
	for attempt := 1; attempt <= iqcheck.MaxAttemptsPerRound; attempt++ {
		if roundCtx.Err() != nil {
			if ctx.Err() != nil {
				return
			}
			finalResult = iqcheck.Unknown("timeout")
			break
		}
		slot, slotErr := acquireAccountProbeSlot(roundCtx, s.concurrency, c.AccountID, account.Concurrency)
		if slotErr != nil || slot == nil || !slot.Acquired {
			if roundCtx.Err() != nil {
				if ctx.Err() != nil {
					return
				}
				finalResult = iqcheck.Unknown("timeout")
				break
			}
			if attempt == 1 {
				s.deferTarget(ctx, c, "account_busy", iqAccountBusyRetryAt(time.Now().UTC()))
				return
			}
			finalResult = iqcheck.Unknown("account_busy")
			judgedReasons = append(judgedReasons, finalResult.Reason)
			break
		}
		challenge := iqcheck.NewChallenge()
		effort := c.ReasoningEffort
		if attempt > 1 {
			effort = iqcheck.AttemptEffort(c.Mode, c.ReasoningEffort, outcomes)
		}
		started := time.Now().UTC()
		var result iqcheck.Result
		questionDeadline := time.Now().Add(time.Duration(c.TimeoutSeconds) * time.Second)
		leaseQuestionDeadline := leaseDeadline.Add(-10 * time.Second)
		if questionDeadline.After(leaseQuestionDeadline) {
			questionDeadline = leaseQuestionDeadline
		}
		questionCtx, cancelQuestion := context.WithDeadline(roundCtx, questionDeadline)
		func() {
			if slot.ReleaseFunc != nil {
				defer slot.ReleaseFunc()
			}
			result = s.probe(questionCtx, c.AccountID, iqProbeRequest{Revision: c.Revision, TimeoutSeconds: c.TimeoutSeconds, TargetModel: c.OutboundModel, TargetEffort: effort, Challenge: challenge, Profile: iqProbeProfile{Model: c.OutboundModel, ReasoningEffort: effort}})
		}()
		cancelQuestion()
		if identity, judged := iqTicketIdentityForResult(result); judged {
			c.TicketIdentities = append(c.TicketIdentities, identity)
		}
		judgedReasons = append(judgedReasons, result.Reason)
		finished := time.Now().UTC()
		writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err := s.targetRepo.AddIQTargetAttempt(writeCtx, c, attempt, effort, challenge, result, started, finished)
		cancelWrite()
		if err != nil {
			return
		}
		finalResult = result
		if result.Diagnostic != nil {
			if result.Diagnostic.RetryAfter != nil && (retryAfter == nil || result.Diagnostic.RetryAfter.After(*retryAfter)) {
				t := *result.Diagnostic.RetryAfter
				retryAfter = &t
			}
			retryAfterUnbounded = retryAfterUnbounded || result.Diagnostic.RetryAfterUnbounded
		}
		outcomes = append(outcomes, iqcheck.Outcome{Status: result.Status, Retryable: iqcheck.Retryable(result)})
		status, done := iqcheck.DecideRound(c.Mode, outcomes)
		if done {
			result.Status = status
			result.Reason = iqTargetRoundReason(status, judgedReasons)
			if status == "unknown" {
				if result.Diagnostic == nil {
					result.Diagnostic = &iqcheck.Diagnostic{ParserVersion: iqcheck.ParserVersion, Stage: "request"}
				}
				result.Diagnostic.RetryAfter = retryAfter
				result.Diagnostic.RetryAfterUnbounded = retryAfterUnbounded
			}
			writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			accepted, err := s.targetRepo.CompleteIQTarget(writeCtx, c, result, finished)
			cancelWrite()
			if err == nil && accepted && status == "degraded" && s.tester != nil && s.tester.openaiGatewayService != nil {
				s.tester.openaiGatewayService.recoverCodexFromIQTarget(ctx, c, result, c.TicketIdentities)
			}
			return
		}
		waitUntil := time.Now().Add(2 * time.Second)
		if retryAfter != nil && retryAfter.After(waitUntil) {
			waitUntil = *retryAfter
		}
		if err := sleepIQAttempt(roundCtx, time.Until(waitUntil)); err != nil {
			if ctx.Err() == nil {
				finalResult = iqcheck.Unknown("timeout")
				break
			}
			return
		}
	}
	if finalResult.Status == "" {
		return
	}
	if finalResult.Reason != "" && (len(judgedReasons) == 0 || judgedReasons[len(judgedReasons)-1] != finalResult.Reason) {
		judgedReasons = append(judgedReasons, finalResult.Reason)
	}
	finalResult.Status = "unknown"
	finalResult.Reason = iqTargetRoundReason("unknown", judgedReasons)
	if finalResult.Diagnostic == nil {
		finalResult.Diagnostic = &iqcheck.Diagnostic{ParserVersion: iqcheck.ParserVersion, Stage: "request"}
	}
	finalResult.Diagnostic.RetryAfter = retryAfter
	finalResult.Diagnostic.RetryAfterUnbounded = retryAfterUnbounded
	writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	_, _ = s.targetRepo.CompleteIQTarget(writeCtx, c, finalResult, time.Now().UTC())
	cancelWrite()
}

func (s *IQCheckService) deferTarget(ctx context.Context, claim IQTargetClaim, reason string, next time.Time) {
	if err := s.targetRepo.DeferIQTarget(ctx, claim, reason, next); err != nil {
		logger.LegacyPrintf("iq_check", "defer failed: account=%d err=%v", claim.AccountID, err)
	}
}

func iqTicketIdentityForResult(result iqcheck.Result) (IQTicketIdentity, bool) {
	if result.Status != "smart" && result.Status != "degraded" {
		return IQTicketIdentity{}, false
	}
	stateProtection := ""
	if result.Diagnostic != nil {
		stateProtection = result.Diagnostic.StateProtection
	}
	return IQTicketIdentity{
		StateTicketID: result.StateTicketID, StateFingerprint: result.StateFingerprint,
		StateRevision: result.StateRevision, StateModel: result.StateModel, StateProtection: stateProtection,
	}, true
}

func iqTargetRoundReason(status string, judgedReasons []string) string {
	switch status {
	case "smart":
		return "correct_answer"
	case "degraded":
		for i := len(judgedReasons) - 1; i >= 0; i-- {
			reason := judgedReasons[i]
			if reason == "model_mismatch" {
				return reason
			}
		}
		return "wrong_answer"
	default:
		for i := len(judgedReasons) - 1; i >= 0; i-- {
			reason := judgedReasons[i]
			if reason != "" && reason != "correct_answer" && reason != "wrong_answer" {
				return reason
			}
		}
		return "unknown"
	}
}
func sleepIQAttempt(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (s *IQCheckService) probe(ctx context.Context, id int64, claims ...iqProbeRequest) (result iqcheck.Result) {
	transport := "http"
	stateProtection := "unmanaged"
	stateTicketID := ""
	stateModel := ""
	stateFingerprint := ""
	stateRevision := ""
	started := time.Now()
	defer func() {
		if result.Diagnostic == nil {
			result.Diagnostic = (&iqcheck.Diagnostic{ParserVersion: iqcheck.ParserVersion, Stage: "request", Code: result.Reason, Transport: transport}).Bounded()
		}
		result.StateTicketID = stateTicketID
		result.StateModel = stateModel
		result.StateFingerprint = stateFingerprint
		result.StateRevision = stateRevision
		result.Diagnostic.StateProtection = stateProtection
		result.Diagnostic.TotalMS = time.Since(started).Milliseconds()
		if transport == "plugin" {
			result.Diagnostic.RetryVisibility = "unknown"
		} else {
			result.Diagnostic.RetryVisibility = "single_attempt"
		}
	}()
	if s.tester == nil {
		return iqcheck.Unknown("transport_unavailable")
	}
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return iqcheck.Unknown("account_unavailable")
	}
	if account.Platform != PlatformOpenAI || account.IsCredentialShadow() {
		return iqcheck.Unknown("unsupported_model")
	}
	if s.tester.pluginManager != nil && s.tester.pluginManager.ShouldRouteOpenAIOAuth(account) {
		transport = "plugin"
	}
	profile := iqProbeProfile{Model: domain.IQModelAstra, ReasoningEffort: domain.DefaultIQEffort}
	if len(claims) > 0 {
		profile = claims[0].Profile
		if profile.Model == "" {
			profile.Model = domain.IQModelAstra
		}
		if profile.ReasoningEffort == "" {
			profile.ReasoningEffort = domain.DefaultIQEffort
		}
		if claims[0].TargetModel != "" {
			profile.Model = claims[0].TargetModel
		}
		if claims[0].TargetEffort != "" {
			profile.ReasoningEffort = claims[0].TargetEffort
		}
	}
	isOAuth := account.IsOAuth()
	chat := !isOAuth && !openai_compat.ShouldUseResponsesAPI(account.Extra)
	if len(claims) > 0 && claims[0].Protocol != "" {
		protocol := "responses"
		if chat {
			protocol = "chat_completions"
		}
		if protocol != claims[0].Protocol {
			return iqcheck.Unknown("cancelled_by_account_change")
		}
	}
	apiURL := chatgptCodexAPIURL
	token := ""
	if isOAuth {
		if !account.IsOpenAIAgentIdentity() {
			if account.Type == AccountTypeOAuth && s.tokens != nil {
				// A failed IQ probe must not invoke the request-path missing-token quarantine.
				expiresAt := account.GetCredentialAsTime("expires_at")
				if !account.IsOpenAIPersonalAccessToken() && expiresAt != nil && !time.Now().Before(*expiresAt) && strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
					return iqcheck.Unknown("authentication_unavailable")
				}
				token, err = s.tokens.GetAccessTokenForIQProbe(ctx, account)
			} else {
				token = account.GetOpenAIAccessToken()
			}
		}
	} else if account.Type == AccountTypeAPIKey {
		token = account.GetOpenAIProtocolAPIKey()
		baseURL := account.GetOpenAIBaseURL()
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		baseURL, err = s.tester.validateUpstreamBaseURL(baseURL)
		if err == nil {
			if chat {
				apiURL = buildOpenAIChatCompletionsURL(baseURL)
			} else {
				apiURL = buildOpenAIResponsesURLForPlatform(account.Platform, baseURL)
			}
		}
	} else {
		return iqcheck.Unknown("unsupported_account_type")
	}
	if err != nil || (token == "" && !account.IsOpenAIAgentIdentity()) {
		return iqcheck.Unknown("authentication_unavailable")
	}
	challenge := iqcheck.NewChallenge()
	if len(claims) > 0 && claims[0].Challenge.V != 0 {
		challenge = claims[0].Challenge
	}
	check := iqcheck.Check{Expected: strconv.Itoa(challenge.Answer()), Naive: strconv.Itoa(challenge.Naive), Sent: profile.Model, PreferHeader: isOAuth || account.IsOpenAIAgentIdentity(), Rules: qualityDowngradeRules(s)}
	payload, _ := json.Marshal(iqcheck.Payload(chat, profile.Model, profile.ReasoningEffort, challenge))
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAI)), http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return iqcheck.Unknown("invalid_endpoint")
	}
	req = req.WithContext(WithHTTPUpstreamSingleAttempt(req.Context()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if account.IsOpenAIAgentIdentity() {
		headers, err := buildAgentIdentityAuthenticationHeaders(ctx, s.accounts, s.tester.agentIdentityWS, &s.tester.agentIdentityTaskMu, account)
		if err != nil {
			return iqcheck.Unknown("authentication_unavailable")
		}
		for key, values := range headers {
			req.Header[key] = values
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if isOAuth {
		req.Host = "chatgpt.com"
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		identity := resolveCodexOutboundIdentity("")
		req.Header.Set("Originator", identity.originator)
		req.Header.Set("User-Agent", identity.userAgent)
		setOpenAIChatGPTAccountHeaders(req.Header, account)
		enforceCodexIdentityHeadersWithUA(req.Header, account.GetOpenAIUserAgent())
	} else {
		ua := account.GetOpenAIUserAgent()
		originator := ""
		if ua == "" {
			ua, originator = CodexCanonicalAuthIdentity()
		}
		req.Header.Set("User-Agent", ua)
		if originator != "" {
			req.Header.Set("Originator", originator)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
	// Disable net/http replay of a POST on a reused connection, including overrides.
	req.GetBody = nil
	req.Header.Del("Idempotency-Key")
	req.Header.Del("X-Idempotency-Key")
	for _, name := range []string{"Session_id", "Conversation_id", "X-Conversation-Id", "Previous_response_id"} {
		req.Header.Del(name)
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	if gateway := s.tester.openaiGatewayService; gateway != nil {
		outbound := profile.Model
		if codexAccountTicketConfigOf(account).manages(outbound) && gateway.openAICodexTicketEnabledContext(ctx) {
			stateProtection = "unprotected"
			stateModel = outbound
			stateFingerprint = codexTicketFixedProxyFingerprint(account)
			stateRevision = codexAccountTicketConfigOf(account).Revision
		}
		if e := gateway.applyOpenAICodexTicketToRequest(ctx, account, outbound, req); e != nil {
			return iqcheck.Unknown("waiting_state")
		}
		if receipt, _ := req.Context().Value(codexTicketReceiptContextKey{}).(*codexTicketReceipt); receipt != nil {
			stateProtection = "protected"
			stateModel = receipt.model
			stateTicketID = receipt.identity()
		}
	}
	// The probe must use the same canonical outbound model used by scheduling.
	if body, e := io.ReadAll(req.Body); e == nil {
		var envelope map[string]any
		if json.Unmarshal(body, &envelope) == nil {
			if model, ok := envelope["model"].(string); !ok || model != check.Sent {
				return iqcheck.Unknown("request_model_mismatch")
			}
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = nil
	}
	if gateway := s.tester.openaiGatewayService; gateway != nil && codexAccountTicketConfigOf(account).Enabled {
		budgetModel := profile.Model
		reservation, e := gateway.reserveCodexBudget(ctx, account.ID, budgetModel, 1, "iq")
		if e != nil {
			return iqcheck.Unknown("background_budget")
		}
		defer gateway.releaseCodexBudget(account.ID, reservation)
		if e = gateway.consumeCodexBudget(ctx, account.ID, budgetModel, "iq", reservation); e != nil {
			return iqcheck.Unknown("background_budget")
		}
	}
	if err := applyIQProbeRequestIdentity(req, account); err != nil {
		return iqcheck.Unknown("request_failed")
	}
	var resp *http.Response
	// STATE-managed probes use the exact business transport. A tester-only TLS
	// override must not change the transport against which the ticket was verified.
	if gateway := s.tester.openaiGatewayService; gateway != nil && stateProtection != "unmanaged" {
		resp, err = gateway.doOpenAIUpstream(req, proxyURL, account)
	} else {
		resp, err = s.tester.doOpenAIAccountTestUpstream(req, proxyURL, account, true)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return iqcheck.Unknown("interrupted")
		}
		var networkError net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
			return iqcheck.Unknown("timeout")
		}
		return iqcheck.Unknown("request_failed")
	}
	if gateway := s.tester.openaiGatewayService; gateway != nil && stateProtection == "unmanaged" {
		gateway.observeCodexTicketResponse(req, resp)
	}
	defer func() { _ = resp.Body.Close() }()
	headerMS := time.Since(started).Milliseconds()
	result = iqcheck.ParseHTTP(resp, chat, transport, check)
	if result.Diagnostic != nil {
		result.Diagnostic.FirstByteMS += headerMS
	}
	return result
}

func (s *IQCheckService) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func qualityDowngradeRules(s *IQCheckService) []openai.DowngradeRule {
	if s != nil && s.tester != nil && s.tester.openaiGatewayService != nil {
		return s.tester.openaiGatewayService.qualityConfig().DowngradeRules()
	}
	return nil
}
