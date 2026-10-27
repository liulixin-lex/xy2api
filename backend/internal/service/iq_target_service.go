package service

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
)

type IQTargetService struct {
	repo           IQTargetRepository
	accounts       AccountRepository
	maxConcurrency int
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	wake           chan struct{}
}

func ProvideIQTargetService(repo IQTargetRepository, accounts AccountRepository) *IQTargetService {
	return &IQTargetService{repo: repo, accounts: accounts, maxConcurrency: iqMonitoringConfig(), wake: make(chan struct{}, 1)}
}
func (s *IQTargetService) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	c, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run(c) }()
}
func (s *IQTargetService) Stop() {
	if s != nil && s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
}
func (s *IQTargetService) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *IQTargetService) run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		claims, err := s.repo.ClaimIQTargets(ctx, time.Now().UTC(), s.maxConcurrency)
		if err == nil {
			for _, claim := range claims {
				s.wg.Add(1)
				go func(c IQTargetClaim) { defer s.wg.Done(); s.execute(ctx, c) }(claim)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// execute runs the persisted round state machine. Transport integration is
// supplied by IQCheckService.probe; this method owns attempts and scheduling.
func (s *IQTargetService) execute(ctx context.Context, claim IQTargetClaim) {
	if s.accounts == nil {
		return
	}
	outcomes := make([]iqcheck.Outcome, 0, iqcheck.MaxAttemptsPerRound)
	for attempt := 1; attempt <= iqcheck.MaxAttemptsPerRound; attempt++ {
		challenge := iqcheck.NewChallenge()
		started := time.Now().UTC()
		result := iqcheck.Unknown("transport_unavailable")
		effort := claim.ReasoningEffort
		if attempt > 1 {
			effort = iqcheck.AttemptEffort(claim.Mode, claim.ReasoningEffort, outcomes)
		}
		if err := s.repo.AddIQTargetAttempt(ctx, claim, attempt, effort, challenge, result, started, time.Now().UTC()); err != nil {
			return
		}
		outcomes = append(outcomes, iqcheck.Outcome{Status: result.Status, Retryable: result.Reason == "transport_unavailable"})
		status, done := iqcheck.DecideRound(claim.Mode, outcomes)
		if done {
			result.Status = status
			result.Reason = map[string]string{"smart": "correct_answer", "degraded": "wrong_answer", "unknown": "unknown"}[status]
			_, _ = s.repo.CompleteIQTarget(ctx, claim, result, time.Now().UTC())
			return
		}
	}
}

func targetExpected(ch iqcheck.Challenge) string { return strconv.Itoa(ch.Answer()) }

var _ = targetExpected
