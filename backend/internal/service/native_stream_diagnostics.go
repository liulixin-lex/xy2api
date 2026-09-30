package service

import (
	"context"
	"strings"
	"sync"
	"time"
)

type NativeDiagnosticScope struct{ UserID, APIKeyID, GroupID int64 }
type NativeStreamDiagnostics struct {
	RequestID               string                   `json:"request_id"`
	ResponseID              string                   `json:"response_id,omitempty"`
	SchedulingRequestID     string                   `json:"scheduling_request_id"`
	PolicyVersion           int64                    `json:"policy_version"`
	MetricVersion           string                   `json:"metric_version"`
	StartedAt               time.Time                `json:"started_at"`
	FirstFlushAt            *time.Time               `json:"first_flush_at,omitempty"`
	GatewayReadToFlushMS    *float64                 `json:"gateway_read_to_flush_ms,omitempty"`
	GatewayMaxReadToFlushMS *float64                 `json:"gateway_max_read_to_flush_ms,omitempty"`
	FlushCount              int64                    `json:"flush_count"`
	HTTPCommitted           bool                     `json:"http_committed"`
	AttemptCommitted        bool                     `json:"attempt_committed"`
	SemanticSeen            bool                     `json:"semantic_seen"`
	Attempts                []SchedulingAttemptTrace `json:"attempts"`
	RecordedAt              time.Time                `json:"recorded_at"`
}
type nativeDiagnosticKey struct {
	Scope NativeDiagnosticScope
	ID    string
}
type nativeDiagnosticRecord struct {
	Value   NativeStreamDiagnostics
	Expires time.Time
}

var nativeDiagnostics = struct {
	sync.Mutex
	rows map[nativeDiagnosticKey]nativeDiagnosticRecord
}{rows: make(map[nativeDiagnosticKey]nativeDiagnosticRecord)}

// This metadata-only, bounded cache does not store prompts, deltas or credentials
// and does not make request IDs usable as public response recovery identities.
func RecordNativeStreamDiagnostics(ctx context.Context, requestID string, scope NativeDiagnosticScope, responseID string) {
	if !NativeStreamDeliveryEnabled(ctx) || strings.TrimSpace(requestID) == "" || len(requestID) > 256 || scope.UserID <= 0 || scope.APIKeyID <= 0 {
		return
	}
	r := controlledRequest(ctx)
	if r == nil {
		return
	}
	now := time.Now()
	d := NativeStreamDiagnostics{RequestID: requestID, ResponseID: responseID, PolicyVersion: NativeStreamPolicyFromContext(ctx).Version, MetricVersion: "native_stream_v1", RecordedAt: now}
	r.mu.Lock()
	d.SchedulingRequestID = r.ID
	d.StartedAt = r.Started
	d.HTTPCommitted = r.httpCommitted
	d.AttemptCommitted = r.attemptCommitted
	d.SemanticSeen = !r.semanticAt.IsZero()
	d.Attempts = append([]SchedulingAttemptTrace{}, r.history...)
	d.FlushCount = r.flushCount
	if r.firstReadToFlushMS != nil {
		v := *r.firstReadToFlushMS
		d.GatewayReadToFlushMS = &v
		v2 := r.maxReadToFlushMS
		d.GatewayMaxReadToFlushMS = &v2
	}
	if !r.firstFlushAt.IsZero() {
		v := r.firstFlushAt
		d.FirstFlushAt = &v
	}
	r.mu.Unlock()
	nativeDiagnostics.Lock()
	defer nativeDiagnostics.Unlock()
	for key, record := range nativeDiagnostics.rows {
		if !now.Before(record.Expires) {
			delete(nativeDiagnostics.rows, key)
		}
	}
	key := nativeDiagnosticKey{scope, requestID}
	if _, ok := nativeDiagnostics.rows[key]; !ok && len(nativeDiagnostics.rows) >= 5000 {
		var oldest nativeDiagnosticKey
		var expires time.Time
		for k, record := range nativeDiagnostics.rows {
			if expires.IsZero() || record.Expires.Before(expires) {
				oldest = k
				expires = record.Expires
			}
		}
		delete(nativeDiagnostics.rows, oldest)
	}
	nativeDiagnostics.rows[key] = nativeDiagnosticRecord{d, now.Add(5 * time.Minute)}
}

func LookupNativeStreamDiagnostics(scope NativeDiagnosticScope, requestID string) (NativeStreamDiagnostics, bool) {
	nativeDiagnostics.Lock()
	defer nativeDiagnostics.Unlock()
	key := nativeDiagnosticKey{scope, requestID}
	record, ok := nativeDiagnostics.rows[key]
	if !ok {
		return NativeStreamDiagnostics{}, false
	}
	if !time.Now().Before(record.Expires) {
		delete(nativeDiagnostics.rows, key)
		return NativeStreamDiagnostics{}, false
	}
	v := record.Value
	v.Attempts = append([]SchedulingAttemptTrace{}, v.Attempts...)
	return v, true
}
