package service

import (
	"context"
	"time"
)

type CodexTicketObservation struct {
	Receipt              codexTicketReceipt
	At                   time.Time
	Result               string
	Checked, Unconfirmed int64
}

type codexTicketDiagnosticsKey struct{}

func CodexTicketDiagnosticsOnly(ctx context.Context) bool {
	v, _ := ctx.Value(codexTicketDiagnosticsKey{}).(bool)
	return v
}

func (s *OpenAIGatewayService) recordCodexTicketObservation(receipt codexTicketReceipt, result string) {
	s.openaiCodexObservationMu.Lock()
	defer s.openaiCodexObservationMu.Unlock()
	if s.openaiCodexObservations == nil {
		s.openaiCodexObservations = make(map[string]CodexTicketObservation)
	}
	key := openAICodexTicketKey(receipt.accountID, receipt.model) + receipt.identity()
	o, exists := s.openaiCodexObservations[key]
	if !exists && len(s.openaiCodexObservations) >= 8192 {
		return
	}
	o.Receipt, o.At, o.Result = receipt, time.Now(), result
	if result == "verified" || result == "model_mismatch" || result == "state_312" {
		o.Checked++
	} else {
		o.Unconfirmed++
	}
	s.openaiCodexObservations[key] = o
}

// Observations are best-effort diagnostics, never an admission authority.
func ApplyCodexTicketObservations(a *Account, observations []CodexTicketObservation) bool {
	changed := false
	for _, o := range observations {
		if o.Receipt.accountID != a.ID {
			continue
		}
		t := parseOpenAICodexTicketFromAny(a.ID, o.Receipt.model, a.Extra[openAICodexTicketExtraKey(o.Receipt.model)])
		if !o.Receipt.matches(t) || t.FixedProxyFingerprint != codexTicketFixedProxyFingerprint(a) {
			continue
		}
		rt := codexTicketRuntimes(a)[o.Receipt.model]
		rt.BusinessChecked += o.Checked
		rt.BusinessUnconfirmed += o.Unconfirmed
		if rt.LastBusinessAt == nil || o.At.After(*rt.LastBusinessAt) {
			rt.LastBusinessAt = &o.At
			rt.LastBusinessResult = o.Result
		}
		saveCodexTicketRuntime(a, o.Receipt.model, rt)
		changed = true
	}
	return changed
}

func (s *OpenAIGatewayService) flushCodexTicketObservations(ctx context.Context) {
	repo, ok := s.accountRepo.(interface {
		MutateCodexTicketBatch(context.Context, []int64, int, CodexTicketMutation) ([]*Account, error)
	})
	if !ok {
		return
	}
	s.openaiCodexObservationMu.Lock()
	batch := s.openaiCodexObservations
	s.openaiCodexObservations = nil
	s.openaiCodexObservationMu.Unlock()
	byAccount := make(map[int64][]CodexTicketObservation)
	for _, o := range batch {
		byAccount[o.Receipt.accountID] = append(byAccount[o.Receipt.accountID], o)
	}
	for id, observations := range byAccount {
		writeCtx, cancel := context.WithTimeout(context.WithValue(ctx, codexTicketDiagnosticsKey{}, true), 3*time.Second)
		_, err := repo.MutateCodexTicketBatch(writeCtx, []int64{id}, 0, func(a *Account, _ int, _ time.Time) (bool, error) {
			return ApplyCodexTicketObservations(a, observations), nil
		})
		cancel()
		if err != nil {
			s.openaiCodexObservationMu.Lock()
			if s.openaiCodexObservations == nil {
				s.openaiCodexObservations = make(map[string]CodexTicketObservation)
			}
			for _, o := range observations {
				key := openAICodexTicketKey(id, o.Receipt.model) + o.Receipt.identity()
				current, exists := s.openaiCodexObservations[key]
				if !exists && len(s.openaiCodexObservations) >= 8192 {
					continue
				}
				if current.At.After(o.At) {
					current.Checked += o.Checked
					current.Unconfirmed += o.Unconfirmed
					s.openaiCodexObservations[key] = current
				} else {
					o.Checked += current.Checked
					o.Unconfirmed += current.Unconfirmed
					s.openaiCodexObservations[key] = o
				}
			}
			s.openaiCodexObservationMu.Unlock()
		}
	}
}
