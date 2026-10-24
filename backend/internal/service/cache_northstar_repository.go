package service

import (
	"context"
	"time"
)

// Queries expose only aggregate records. Usage rows are read exclusively by the
// background aggregation implementation, behind the administrator policy gate.
type CacheNorthstarRepository interface {
	GetCacheNorthstarState(context.Context) (*CacheNorthstarState, error)
	AggregateCacheNorthstar(context.Context, time.Time, time.Time, CacheNorthstarPolicy, time.Time, bool) error
	ReadCacheNorthstar(context.Context, string, time.Time, time.Time, string) (CacheNorthstarData, error)
	CleanupCacheNorthstar(context.Context, time.Time, time.Time) error
	SaveCacheTraceHours(context.Context, []CacheTraceHour) error
	ListCachePanelEvents(context.Context, time.Time, time.Time) ([]CachePanelEvent, error)
	RecordCachePanelStart(context.Context, string, time.Time, string, bool) error
}
type CacheNorthstarState struct {
	PolicyVersion   string
	Policy          CacheNorthstarPolicy
	PolicyChangedAt time.Time
	AggregatedUntil time.Time
	BackfilledFrom  time.Time
	ReconciledDay   *time.Time
}
type CacheNorthstarHour struct {
	BucketStart        time.Time
	MediaRequests      int64
	ZeroPromptRequests int64
	ProbeGroupRequests int64
}
type CacheNorthstarData struct {
	Rows  []CacheNorthstarRow
	Hours []CacheNorthstarHour
}
type CachePanelEvent struct {
	At        time.Time  `json:"at"`
	Kind      string     `json:"kind"`
	Label     string     `json:"label"`
	Version   string     `json:"version,omitempty"`
	ColdUntil *time.Time `json:"cold_until,omitempty"`
	PolicyKey string     `json:"policy_key,omitempty"`
}
type CacheTraceHour struct {
	BucketStart       time.Time
	ProcessID         string
	Platform          string
	Transport         string
	RelayFamily       string
	PCKClassOut       string
	LossClass         string
	ModelGen          string
	ForkKind          string
	Requests          int64
	PromptTokens      int64
	ReadTokens        int64
	OpportunityTokens int64
}
