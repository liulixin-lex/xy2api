package service

import (
	"context"
	"crypto/sha256"
	"errors"
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
type codexTicketClientContextKey struct{}

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
	if req == nil || resp == nil || resp.Body == nil {
		return
	}
	receipt, _ := req.Context().Value(codexTicketReceiptContextKey{}).(*codexTicketReceipt)
	if receipt == nil {
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.traceCodexTicket(CodexTicketTraceEvent{AccountID: receipt.accountID, Model: receipt.model, Stage: "business", Outcome: "upstream_failed", Detail: CodexTicketTraceDetail{TicketID: receipt.identity(), HTTPStatus: resp.StatusCode}})
		s.recordCodexTicketObservation(*receipt, "upstream_failed")
		return
	}
	state, headerErr := uniqueCodexTicketHeader(resp.Header)
	resp.Body = &codexTicketWatchdogBody{ReadCloser: resp.Body, ctx: req.Context(), model: receipt.model, encoding: resp.Header.Get("Content-Encoding"), state312: len(state) == 312 && validCodexTicketState(state), observe: func(result string) {
		s.recordCodexTicketObservation(*receipt, result)
		if result == "verified" && headerErr == nil {
			s.capturePassiveCodexCandidate(*receipt, state)
		}
		s.traceCodexTicket(CodexTicketTraceEvent{AccountID: receipt.accountID, Model: receipt.model, Stage: "business", Outcome: result, Detail: CodexTicketTraceDetail{TicketID: receipt.identity(), HTTPStatus: resp.StatusCode, Length: len(state)}})
	}, diagnostics: func(outcome, reason string) {
		s.traceCodexTicket(CodexTicketTraceEvent{AccountID: receipt.accountID, Model: receipt.model, Stage: "business_detail", Outcome: outcome, Detail: CodexTicketTraceDetail{TicketID: receipt.identity(), Reason: reason}})
	}, trigger: func(reason string) { s.invalidateCodexTicketFromResponse(*receipt, reason) }}
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
		// Keep the original time bound if the pool returns the revoked value again.
		// It is never injectable or a legacy replay candidate while revoked.
		current.Verified, current.Revoked = false, true
		a.Extra[openAICodexTicketExtraKey(receipt.model)] = current
		rememberCodexRevoked(a, receipt.model, current, now)
		rt.RevokedValues = codexTicketRuntimes(a)[receipt.model].RevokedValues
		s.promoteCodexStandby(a, receipt.model, now)
		rt.TriggerCount++
		if reason != "iq_degraded" {
			rt.LastBusinessAt = &now
			rt.LastBusinessResult = reason
			rt.BusinessChecked++
		}
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
	ctx       context.Context
	closed    atomic.Bool
	readMu    sync.Mutex
	closeOnce sync.Once
	closeErr  error
	completed chan struct{}
	io.ReadCloser
	model       string
	encoding    string
	state312    bool
	trigger     func(string)
	observe     func(string)
	diagnostics func(string, string)
	mu          sync.Mutex
	once        sync.Once
	writer      *io.PipeWriter
	result      chan codexStreamResult
	finished    bool
}
type codexStreamResult struct {
	model string
	err   error
}

func (b *codexTicketWatchdogBody) init() {
	b.once.Do(func() {
		reader, writer := io.Pipe()
		b.writer = writer
		b.result = make(chan codexStreamResult, 1)
		b.completed = make(chan struct{})
		go func() {
			model, err := codexTicketResponseModel(reader, b.encoding)
			_ = reader.Close()
			b.result <- codexStreamResult{model, err}
		}()
	})
}
func (b *codexTicketWatchdogBody) finish(result codexStreamResult, complete bool) {
	b.mu.Lock()
	if b.finished {
		b.mu.Unlock()
		return
	}
	b.finished = true
	b.mu.Unlock()
	// Completion includes the observation callbacks, not only the parser result.
	// Close may return as soon as this signal is received.
	defer close(b.completed)
	outcome, reason := "unconfirmed", ""
	if result.err != nil && result.err.Error() == "unsupported content encoding" {
		outcome = "unsupported_encoding"
	}
	if complete && result.err == nil {
		outcome = "verified"
		if result.model != b.model {
			reason = "model_mismatch"
		} else if b.state312 {
			reason = "state_312"
		}
	}
	if reason != "" {
		outcome = reason
	}
	if b.diagnostics != nil {
		detail := ""
		if !complete || result.err != nil {
			detail = "incomplete_response"
			if result.err != nil {
				switch {
				case strings.Contains(result.err.Error(), "conflicting"):
					detail = "conflicting_terminal"
				case errors.Is(result.err, io.ErrClosedPipe):
					detail = "closed_before_eof"
				case errors.Is(result.err, context.DeadlineExceeded):
					detail = "upstream_timeout"
				default:
					detail = "read_or_protocol_error"
				}
			}
			if b.ctx != nil {
				if client, ok := b.ctx.Value(codexTicketClientContextKey{}).(context.Context); ok && client.Err() != nil {
					detail = "client_cancelled"
				}
			}
		}
		b.diagnostics(outcome, detail)
	}
	if b.observe != nil {
		b.observe(outcome)
	}
	if reason != "" && b.trigger != nil {
		b.trigger(reason)
	}
}
func (b *codexTicketWatchdogBody) Read(p []byte) (int, error) {
	b.init()
	b.readMu.Lock()
	defer b.readMu.Unlock()
	if b.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		_, _ = b.writer.Write(p[:n])
	}
	if err != nil {
		if err == io.EOF {
			_ = b.writer.Close()
		} else {
			_ = b.writer.CloseWithError(err)
		}
		result := <-b.result
		// Retain the result for a concurrent or repeated Close/Read.
		b.result <- result
		b.finish(result, err == io.EOF)
	}
	return n, err
}
func (b *codexTicketWatchdogBody) Close() error {
	return b.closeWithCancel(func() {})
}

// Protocol converters may stop at the terminal event. Finish observation within
// the deadline, then cancel before Close so context-bound transports cannot hang.
func (b *codexTicketWatchdogBody) closeWithCancel(cancel context.CancelFunc) error {
	b.init()
	b.closeOnce.Do(func() {
		b.mu.Lock()
		finished := b.finished
		b.mu.Unlock()
		if b.ctx == nil || b.ctx.Err() == nil {
			// The read lock preserves the byte order with the converter's scanner.
			// Completion can also come from that scanner, not only our drain.
			if !finished {
				go func() { _, _ = io.Copy(io.Discard, b) }()
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-b.completed:
			case <-timer.C:
			}
			timer.Stop()
		}
		b.closed.Store(true)
		cancel()
		_ = b.writer.CloseWithError(io.ErrClosedPipe)
		b.closeErr = b.ReadCloser.Close()
		result := <-b.result
		b.result <- result
		b.finish(result, false)
	})
	return b.closeErr
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
