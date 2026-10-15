package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Undelivered requests (design §8.2).

// UsageBillingModeUndeliveredAbuse marks usage billed at upstream cost by
// the undelivered abuse guard.
const UsageBillingModeUndeliveredAbuse = "undelivered_abuse"

// UndeliveredRequest is one request whose client left before any semantic
// output.
type UndeliveredRequest struct {
	ID                    int64           `json:"id"`
	RequestID             string          `json:"request_id"`
	ClientRequestID       string          `json:"client_request_id"`
	UserID                *int64          `json:"user_id,omitempty"`
	APIKeyID              *int64          `json:"api_key_id,omitempty"`
	GroupID               *int64          `json:"group_id,omitempty"`
	AccountID             *int64          `json:"account_id,omitempty"`
	Platform              string          `json:"platform"`
	Model                 string          `json:"model"`
	InboundEndpoint       string          `json:"inbound_endpoint"`
	DisconnectStage       string          `json:"disconnect_stage"`
	DurationMs            int64           `json:"duration_ms"`
	UpstreamUsage         json.RawMessage `json:"upstream_usage,omitempty"`
	EstimatedUpstreamCost float64         `json:"estimated_upstream_cost"`
	Billed                bool            `json:"billed"`
	CreatedAt             time.Time       `json:"created_at"`
}

// UndeliveredFilter selects undelivered records for the admin list.
type UndeliveredFilter struct {
	UserID   int64
	APIKeyID int64
	GroupID  int64
	Since    time.Time
	Until    time.Time
}

// UndeliveredRepository stores undelivered records.
type UndeliveredRepository interface {
	InsertUndelivered(ctx context.Context, rec *UndeliveredRequest) error
	ListUndelivered(ctx context.Context, filter UndeliveredFilter, page pagination.PaginationParams) ([]UndeliveredRequest, *pagination.PaginationResult, error)
	DeleteUndeliveredBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// resilienceUndeliveredUsage applies the undelivered rules to a usage record
// about to be billed. It returns true when the request must not be billed
// at all (no usage_logs row). Under the abuse guard it rewrites the charge
// to the upstream cost and marks the billing mode.
func resilienceUndeliveredUsage(ctx context.Context, usageLog *UsageLog, cost *CostBreakdown, accountRate float64) bool {
	st := resilience.StateFrom(ctx)
	if st == nil || usageLog == nil || !resilience.Current().DisconnectOn() {
		return false
	}
	disconnected, delivered, abuse := st.Disconnect()
	if !disconnected || delivered {
		return false
	}
	upstreamCost := usageLog.TotalCost * accountRate
	if usageLog.AccountStatsCost != nil {
		upstreamCost = *usageLog.AccountStatsCost
	}
	usageJSON, _ := json.Marshal(map[string]any{
		"model":                 usageLog.Model,
		"input_tokens":          usageLog.InputTokens,
		"output_tokens":         usageLog.OutputTokens,
		"cache_creation_tokens": usageLog.CacheCreationTokens,
		"cache_read_tokens":     usageLog.CacheReadTokens,
		"total_cost":            usageLog.TotalCost,
	})
	if !abuse {
		st.NoteUndeliveredUsage(usageJSON, upstreamCost, false)
		return true
	}
	usageLog.ActualCost = upstreamCost
	mode := UsageBillingModeUndeliveredAbuse
	usageLog.BillingMode = &mode
	if cost != nil {
		cost.ActualCost = upstreamCost
	}
	st.NoteUndeliveredUsage(usageJSON, upstreamCost, true)
	return false
}

// ConfigureUndelivered attaches the undelivered repository, registers the
// abuse counter and the retention cleanup.
func (r *ResilienceService) ConfigureUndelivered(repo UndeliveredRepository) {
	if r == nil {
		return
	}
	r.undelivered = repo
	undeliveredRecorder.Store(r)
	resilience.SetUndeliveredAbuseCheck(r.undeliveredAbuse)
	if repo == nil {
		return
	}
	r.AddBackgroundLoop(func(ctx context.Context) {
		t := time.NewTicker(undeliveredCleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, cancel := context.WithTimeout(ctx, time.Minute)
				if n, err := repo.DeleteUndeliveredBefore(cctx, time.Now().Add(-undeliveredRetention)); err != nil {
					slog.Warn("resilience.undelivered_cleanup_failed", "error", err)
				} else if n > 0 {
					slog.Info("resilience.undelivered_cleanup", "deleted", n)
				}
				cancel()
			}
		}
	})
}

const (
	undeliveredRetention       = 30 * 24 * time.Hour
	undeliveredCleanupInterval = 6 * time.Hour
)

// undeliveredAbuse counts an undelivered disconnect of a key in the current
// UTC hour and reports whether the key exceeded the allowance.
func (r *ResilienceService) undeliveredAbuse(apiKeyID int64) bool {
	cfg := resilience.Current()
	guard := cfg.Disconnect.AbuseGuard
	if r == nil || r.store == nil || !guard.Enabled || guard.ThresholdPerHour <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	n, err := r.store.IncrUndelivered(ctx, apiKeyID, time.Now().UTC().Format("2006010215"))
	if err != nil {
		slog.Warn("resilience.undelivered_count_failed", "api_key_id", apiKeyID, "error", err)
		return false
	}
	return n > int64(guard.ThresholdPerHour)
}

// undeliveredRecorder is the service that stores undelivered records; the
// gateway middleware has no reference to it.
var undeliveredRecorder atomic.Pointer[ResilienceService]

// RecordUndeliveredRequest stores an undelivered record in the background.
func RecordUndeliveredRequest(rec *UndeliveredRequest) {
	undeliveredRecorder.Load().RecordUndelivered(rec)
}

// RecordUndelivered stores an undelivered record in the background.
func (r *ResilienceService) RecordUndelivered(rec *UndeliveredRequest) {
	if r == nil || r.undelivered == nil || rec == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.undelivered.InsertUndelivered(ctx, rec); err != nil {
			slog.Warn("resilience.undelivered_record_failed", "request_id", rec.RequestID, "error", err)
		}
	}()
}

// ListUndelivered returns undelivered records for administrators.
func (r *ResilienceService) ListUndelivered(ctx context.Context, filter UndeliveredFilter, page pagination.PaginationParams) ([]UndeliveredRequest, *pagination.PaginationResult, error) {
	if r == nil || r.undelivered == nil {
		return nil, &pagination.PaginationResult{Page: page.Page, PageSize: page.PageSize}, nil
	}
	return r.undelivered.ListUndelivered(ctx, filter, page)
}
