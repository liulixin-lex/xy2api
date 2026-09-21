package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const codexTicketBudgetKey = "codex_ticket_budget"

var errCodexBackgroundBudget = errors.New("STATE background request budget exhausted")

type codexBackgroundCall struct {
	At    time.Time `json:"at"`
	Model string    `json:"model"`
	Kind  string    `json:"kind"`
}
type codexBackgroundReservation struct {
	ID        string    `json:"id"`
	Model     string    `json:"model"`
	Remaining int       `json:"remaining"`
	Until     time.Time `json:"until"`
}
type codexBackgroundBudget struct {
	Calls        []codexBackgroundCall        `json:"calls"`
	Reservations []codexBackgroundReservation `json:"reservations"`
}
type CodexTicketBudgetStatus struct {
	Calls      int        `json:"calls"`
	ModelCalls int        `json:"model_calls"`
	Limit      int        `json:"limit"`
	ModelLimit int        `json:"model_limit"`
	Reserved   int        `json:"reserved"`
	RetryAt    *time.Time `json:"retry_at,omitempty"`
}
type codexBudgetContextKey struct{}

func codexBudgetOf(a *Account, now time.Time) codexBackgroundBudget {
	var b codexBackgroundBudget
	raw, _ := json.Marshal(a.Extra[codexTicketBudgetKey])
	_ = json.Unmarshal(raw, &b)
	calls := b.Calls[:0]
	for _, c := range b.Calls {
		if c.At.After(now.Add(-time.Hour)) {
			calls = append(calls, c)
		}
	}
	b.Calls = calls
	reservations := b.Reservations[:0]
	for _, r := range b.Reservations {
		if r.Remaining > 0 && r.Until.After(now) {
			reservations = append(reservations, r)
		}
	}
	b.Reservations = reservations
	return b
}
func codexBudgetLimits(a *Account, model string, now time.Time) (int, int, bool) {
	ac := codexAccountTicketConfigOf(a)
	active := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[openAICodexTicketExtraKey(model)])
	standby := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[codexStandbyKey(model)])
	rescue := ac.manages(model) && (!active.validFor(a, ac, now) || active.ExpiresAt.Before(now.Add(5*time.Minute)) && !standby.validFor(a, ac, now))
	if rescue {
		return 48, 32, true
	}
	return 24, 16, false
}
func codexBudgetStatus(a *Account, model string, now time.Time) CodexTicketBudgetStatus {
	b := codexBudgetOf(a, now)
	limit, modelLimit, _ := codexBudgetLimits(a, model, now)
	st := CodexTicketBudgetStatus{Calls: len(b.Calls), Limit: limit, ModelLimit: modelLimit}
	for _, c := range b.Calls {
		if c.Model == model {
			st.ModelCalls++
		}
		next := c.At.Add(time.Hour)
		if st.RetryAt == nil || next.Before(*st.RetryAt) {
			st.RetryAt = &next
		}
	}
	for _, r := range b.Reservations {
		st.Reserved += r.Remaining
	}
	return st
}
func (s *OpenAIGatewayService) reserveCodexBudget(ctx context.Context, id int64, model string, count int, kind string) (string, error) {
	if !s.openAICodexTicketConfig().AdaptiveSchedulingEnabled && !s.openAICodexTicketConfig().QualityObservationEnabled {
		return "", nil
	}
	reservation := uuid.NewString()
	_, err := s.mutateCodexTicket(ctx, id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		b := codexBudgetOf(a, now)
		limit, modelLimit, rescue := codexBudgetLimits(a, model, now)
		used, modelUsed := len(b.Calls), 0
		for _, c := range b.Calls {
			if c.Model == model {
				modelUsed++
			}
		}
		for _, r := range b.Reservations {
			used += r.Remaining
			if r.Model == model {
				modelUsed += r.Remaining
			}
		}
		// Eight calls stay available for rescue and already-reserved replay.
		if !rescue {
			limit -= 8
		}
		if used+count > limit || modelUsed+count > modelLimit {
			return false, errCodexBackgroundBudget
		}
		b.Reservations = append(b.Reservations, codexBackgroundReservation{reservation, model, count, now.Add(3 * time.Minute)})
		a.Extra[codexTicketBudgetKey] = b
		return true, nil
	})
	return reservation, err
}
func (s *OpenAIGatewayService) consumeCodexBudget(ctx context.Context, id int64, model, kind, reservation string) error {
	if reservation == "" {
		return nil
	}
	_, err := s.mutateCodexTicket(ctx, id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		b := codexBudgetOf(a, now)
		for i := range b.Reservations {
			r := &b.Reservations[i]
			if r.ID == reservation && r.Model == model && r.Remaining > 0 {
				if kind == "quality" {
					q := codexQualityOf(a, model)
					recent := q.Calls[:0]
					for _, at := range q.Calls {
						if at.After(now.Add(-24 * time.Hour)) {
							recent = append(recent, at)
						}
					}
					q.Calls = recent
					if len(q.Calls) >= 36 {
						return false, errCodexBackgroundBudget
					}
					q.Calls = append(q.Calls, now)
					rt := codexTicketRuntimes(a)[model]
					if rt.Task.active() && rt.Task.Source == "quality" {
						rt.Task.Attempts++
						saveCodexTicketRuntime(a, model, rt)
					}
					saveCodexQuality(a, model, q)
				}
				r.Remaining--
				b.Calls = append(b.Calls, codexBackgroundCall{now, model, kind})
				a.Extra[codexTicketBudgetKey] = b
				return true, nil
			}
		}
		return false, errCodexBackgroundBudget
	})
	return err
}
func (s *OpenAIGatewayService) releaseCodexBudget(id int64, reservation string) {
	if reservation == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.mutateCodexTicket(ctx, id, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		b := codexBudgetOf(a, now)
		for i := range b.Reservations {
			if b.Reservations[i].ID == reservation {
				b.Reservations[i].Remaining = 0
			}
		}
		a.Extra[codexTicketBudgetKey] = b
		return true, nil
	})
}
