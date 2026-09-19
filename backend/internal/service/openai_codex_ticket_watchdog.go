package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const codexTicketWatchdogExtraKey = "codex_ticket_watchdog"

// Persist only a small reason/timestamp summary, never response bodies or STATE.
type CodexTicketWatchdogStatus struct {
	Enabled         bool       `json:"enabled"`
	TriggerCount    int64      `json:"trigger_count"`
	LastReason      string     `json:"last_reason,omitempty"`
	LastTriggeredAt *time.Time `json:"last_triggered_at,omitempty"`
}

// Bound at the exact injection point. Client-supplied headers cannot opt a
// request into the watchdog, and an old response cannot revoke a newer ticket.
type codexTicketReceipt struct {
	accountID        int64
	model            string
	revision         string
	fixedFingerprint string
	stateHash        [32]byte
	capturedAt       time.Time
}

type codexTicketReceiptContextKey struct{}

func receiptForCodexTicket(ticket *openAICodexTicket) codexTicketReceipt {
	return codexTicketReceipt{ticket.AccountID, ticket.Model, ticket.ConfigRevision,
		ticket.FixedProxyFingerprint, sha256.Sum256([]byte(ticket.State)), ticket.CapturedAt}
}

func (r codexTicketReceipt) matches(ticket *openAICodexTicket) bool {
	if ticket == nil {
		return false
	}
	other := receiptForCodexTicket(ticket)
	return r.accountID == other.accountID && r.model == other.model && r.revision == other.revision &&
		r.fixedFingerprint == other.fixedFingerprint && r.stateHash == other.stateHash && r.capturedAt.Equal(other.capturedAt)
}

func (s *OpenAIGatewayService) codexTicketRejectedByWatchdog(ticket *openAICodexTicket) bool {
	if s == nil || ticket == nil {
		return false
	}
	raw, ok := s.openaiCodexWatchdogRevoked.Load(openAICodexTicketKey(ticket.AccountID, ticket.Model))
	if !ok {
		return false
	}
	through, ok := raw.(time.Time)
	return ok && !ticket.CapturedAt.After(through)
}

func (s *OpenAIGatewayService) applyOpenAICodexTicketToRequest(ctx context.Context, account *Account, model string, req *http.Request) error {
	receipt, err := s.applyOpenAICodexTicketWithReceipt(ctx, account, model, req.Header)
	if err != nil {
		return err
	}
	if receipt == nil && req.Context().Value(codexTicketReceiptContextKey{}) == nil {
		return nil
	}
	*req = *req.WithContext(context.WithValue(req.Context(), codexTicketReceiptContextKey{}, receipt))
	return nil
}

func (s *OpenAIGatewayService) observeCodexTicketResponse(req *http.Request, resp *http.Response) {
	if req == nil || resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.Body == nil {
		return
	}
	receipt, _ := req.Context().Value(codexTicketReceiptContextKey{}).(*codexTicketReceipt)
	if receipt == nil {
		return
	}
	state := strings.TrimSpace(resp.Header.Get(openAICodexTurnStateHeader))
	resp.Body = &codexTicketWatchdogBody{ReadCloser: resp.Body, ctx: req.Context(), model: receipt.model, encoding: resp.Header.Get("Content-Encoding"), state312: len(state) == 312 && validCodexTicketState(state), trigger: func(reason string) { s.invalidateCodexTicketFromResponse(*receipt, reason) }}
}
func (s *OpenAIGatewayService) invalidateCodexTicketFromResponse(receipt codexTicketReceipt, reason string) {
	if reason != "model_mismatch" && reason != "state_312" && reason != "iq_degraded" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	revoked := false
	_, err := s.mutateCodexTicket(ctx, receipt.accountID, 0, func(a *Account, _ int, now time.Time) (bool, error) {
		ac := codexAccountTicketConfigOf(a)
		current := parseOpenAICodexTicketFromAny(a.ID, receipt.model, a.Extra[openAICodexTicketExtraKey(receipt.model)])
		if !ac.manages(receipt.model) || !current.validFor(a, ac, now) || !receipt.matches(current) {
			return false, nil
		}
		rt := codexTicketRuntimes(a)[receipt.model]
		if reason == "iq_degraded" {
			if rt.IQRecoveryAt != nil && rt.IQRecoveryAt.After(now.Add(-15*time.Minute)) {
				return false, nil
			}
			rt.IQRecoveryAt = &now
		}
		delete(a.Extra, openAICodexTicketExtraKey(receipt.model))
		rt.TriggerCount++
		rt.Requested = true
		rt.event(now, reason)
		codexTicketRecoveryHistory(&rt, now)
		if len(rt.Recoveries) >= 3 {
			next := now.Add(codexTicketRetryCooldown)
			if rt.RetryAfter == nil || next.After(*rt.RetryAfter) {
				rt.RetryAfter = &next
			}
			rt.Phase = "cooldown"
		}
		saveCodexTicketRuntime(a, receipt.model, rt)
		revoked = true
		return true, nil
	})
	if err != nil {
		logger.L().Warn("codex ticket watchdog invalidation persistence failed", zap.Int64("account_id", receipt.accountID))
		return
	}
	if revoked {
		s.rememberCodexTicketRevocation(receipt)
		s.openaiCodexTickets.Delete(openAICodexTicketKey(receipt.accountID, receipt.model))
		s.startCodexAccountTicketJob(ctx, receipt.accountID, false, receipt.model)
	}
}

const codexTicketWatchdogBufferLimit = codexTicketResponseLimit

// Observes upstream bytes without response rewriting or replay. Early terminal
// consumers use the bounded completion drain in Close; errors remain inconclusive.
type codexTicketWatchdogBody struct {
	ctx     context.Context
	reading atomic.Int32
	io.ReadCloser
	model    string
	encoding string
	state312 bool
	trigger  func(string)
	buffer   []byte
	overflow bool
	mu       sync.Mutex
	finished bool
}

func (b *codexTicketWatchdogBody) Read(p []byte) (int, error) {
	b.reading.Add(1)
	defer b.reading.Add(-1)
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	reason := ""
	if !b.finished {
		if !b.overflow && len(b.buffer)+n <= codexTicketWatchdogBufferLimit {
			b.buffer = append(b.buffer, p[:n]...)
		} else {
			b.overflow = true
			b.buffer = nil
		}
		if err != nil {
			b.finished = true
			if err == io.EOF && !b.overflow {
				actual, e := codexTicketResponseModel(bytes.NewReader(b.buffer), b.encoding)
				if e == nil {
					if actual != b.model {
						reason = "model_mismatch"
					} else if b.state312 {
						reason = "state_312"
					}
				}
			}
			b.buffer = nil
		}
	}
	b.mu.Unlock()
	if reason != "" {
		b.trigger(reason)
	}
	return n, err
}
func (b *codexTicketWatchdogBody) Close() error {
	// Some conversion/WS consumers stop at response.completed. Finish observing
	// their remaining HTTP body for at most 250 ms, without forwarding or replaying
	// bytes. A transport error, timeout, overflow or conflicting terminal is ignored.
	b.mu.Lock()
	drain := !b.finished && !b.overflow && b.reading.Load() == 0
	captured := append([]byte(nil), b.buffer...)
	if b.ctx != nil && b.ctx.Err() != nil {
		drain = false
	}
	if _, e := codexTicketResponseModel(bytes.NewReader(captured), b.encoding); e != nil {
		drain = false
	}
	b.finished = true
	b.buffer = nil
	b.mu.Unlock()
	reason := ""
	if drain {
		type outcome struct {
			data []byte
			err  error
		}
		done := make(chan outcome, 1)
		go func() {
			rest, e := io.ReadAll(io.LimitReader(b.ReadCloser, int64(codexTicketWatchdogBufferLimit-len(captured)+1)))
			done <- outcome{rest, e}
		}()
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case out := <-done:
			if out.err == nil && len(captured)+len(out.data) <= codexTicketWatchdogBufferLimit {
				actual, e := codexTicketResponseModel(bytes.NewReader(append(captured, out.data...)), b.encoding)
				if e == nil {
					if actual != b.model {
						reason = "model_mismatch"
					} else if b.state312 {
						reason = "state_312"
					}
				}
			}
		case <-timer.C:
		}
		timer.Stop()
	}
	err := b.ReadCloser.Close()
	if reason != "" {
		b.trigger(reason)
	}
	return err
}

func (s *OpenAIGatewayService) rememberCodexTicketRevocation(receipt codexTicketReceipt) {
	key := openAICodexTicketKey(receipt.accountID, receipt.model)
	for {
		old, loaded := s.openaiCodexWatchdogRevoked.LoadOrStore(key, receipt.capturedAt)
		if !loaded {
			return
		}
		through, ok := old.(time.Time)
		if ok && !receipt.capturedAt.After(through) {
			return
		}
		if s.openaiCodexWatchdogRevoked.CompareAndSwap(key, old, receipt.capturedAt) {
			return
		}
	}
}
