package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type CodexTicketTraceEvent struct {
	ID        int64                  `json:"id,omitempty"`
	EventID   string                 `json:"event_id"`
	AccountID int64                  `json:"account_id"`
	Model     string                 `json:"model"`
	At        time.Time              `json:"at"`
	Stage     string                 `json:"stage"`
	Outcome   string                 `json:"outcome"`
	Detail    CodexTicketTraceDetail `json:"detail"`
}
type CodexTicketTraceDetail struct {
	Reason     string `json:"reason,omitempty"`
	Structure  string `json:"structure,omitempty"`
	Transport  string `json:"transport,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	ProxyID    string `json:"proxy_id,omitempty"`
	TicketID   string `json:"ticket_id,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Length     int    `json:"length,omitempty"`
	LatencyMS  int64  `json:"latency_ms,omitempty"`
	Source     string `json:"source,omitempty"`
	Count      int64  `json:"count,omitempty"`
}
type codexTicketEventRepository interface {
	AppendCodexTicketEvents(context.Context, []CodexTicketTraceEvent) error
	ListCodexTicketEvents(context.Context, int64, string, int64, int) ([]CodexTicketTraceEvent, error)
}

func (s *OpenAIGatewayService) traceCodexTicket(event CodexTicketTraceEvent) {
	if _, ok := s.accountRepo.(codexTicketEventRepository); !ok {
		return
	}
	if event.EventID == "" {
		event.EventID = uuid.NewString()
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	s.openaiCodexObservationMu.Lock()
	defer s.openaiCodexObservationMu.Unlock()
	if len(s.openaiCodexEvents) >= 1024 {
		s.openaiCodexEventsDropped++
		return
	}
	s.openaiCodexEvents = append(s.openaiCodexEvents, event)
}
func (s *OpenAIGatewayService) flushCodexTicketEvents(ctx context.Context) {
	repo, ok := s.accountRepo.(codexTicketEventRepository)
	if !ok {
		return
	}
	s.openaiCodexObservationMu.Lock()
	batch := s.openaiCodexEvents
	s.openaiCodexEvents = nil
	dropped := s.openaiCodexEventsDropped
	s.openaiCodexEventsDropped = 0
	s.openaiCodexObservationMu.Unlock()
	// Empty periodic batches still prune expired records while STATE is idle.
	if dropped > 0 && len(batch) > 0 {
		event := batch[0]
		event.EventID = uuid.NewString()
		event.Stage = "diagnostics"
		event.Outcome = "events_dropped"
		event.Detail = CodexTicketTraceDetail{Count: dropped}
		batch = append(batch, event)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := repo.AppendCodexTicketEvents(writeCtx, batch); err != nil {
		s.openaiCodexObservationMu.Lock()
		defer s.openaiCodexObservationMu.Unlock()
		space := 1024 - len(s.openaiCodexEvents)
		if len(batch) > space {
			s.openaiCodexEventsDropped += int64(len(batch) - space)
			batch = batch[:space]
		}
		s.openaiCodexEvents = append(batch, s.openaiCodexEvents...)
	}
}
func (s *OpenAIGatewayService) GetCodexTicketEvents(ctx context.Context, id int64, model string, before int64) ([]CodexTicketTraceEvent, error) {
	if _, err := s.codexTicketAccountByID(ctx, id); err != nil {
		return nil, err
	}
	if repo, ok := s.accountRepo.(codexTicketEventRepository); ok {
		return repo.ListCodexTicketEvents(ctx, id, model, before, 100)
	}
	return []CodexTicketTraceEvent{}, nil
}
