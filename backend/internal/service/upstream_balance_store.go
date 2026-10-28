package service

import (
	"context"
	"time"
)

type UpstreamBalanceRecord struct {
	Version     int       `json:"v"`
	Fingerprint string    `json:"fp"`
	Kind        string    `json:"kind"`
	AmountUSD   *float64  `json:"amount_usd"`
	Unlimited   bool      `json:"unlimited"`
	Basis       string    `json:"basis,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

type UpstreamBalanceState struct {
	Version               int        `json:"v"`
	Fingerprint           string     `json:"fp"`
	LastAttemptAt         time.Time  `json:"last_attempt_at"`
	LastReason            string     `json:"last_reason,omitempty"`
	LastHTTPStatus        int        `json:"last_http_status,omitempty"`
	NextDueAt             time.Time  `json:"next_due_at"`
	RetryNotBefore        *time.Time `json:"retry_not_before,omitempty"`
	TransientStreak       int        `json:"transient_streak,omitempty"`
	AuthStreak            int        `json:"auth_streak,omitempty"`
	UnsupportedStreak     int        `json:"unsupported_streak,omitempty"`
	Slow                  bool       `json:"slow,omitempty"`
	FastStreak            int        `json:"fast_streak,omitempty"`
	TokenFailures         int        `json:"token_failures,omitempty"`
	TokenRetryAt          *time.Time `json:"token_retry_at,omitempty"`
	AuthEpisode           string     `json:"auth_episode,omitempty"`
	AuthEpisodeAt         *time.Time `json:"auth_episode_at,omitempty"`
	TokenShortcutRejected bool       `json:"token_shortcut_rejected,omitempty"`
}

type UpstreamBalanceKindRecord struct {
	Version      int       `json:"v"`
	Fingerprint  string    `json:"fp"`
	Kind         string    `json:"kind"`
	IdentifiedAt time.Time `json:"identified_at"`
}
type UpstreamBalanceSite struct {
	Version         int       `json:"v"`
	QuotaPerUnit    float64   `json:"quota_per_unit"`
	DisplayType     string    `json:"display_type"`
	USDExchangeRate float64   `json:"usd_exchange_rate,omitempty"`
	FetchedAt       time.Time `json:"fetched_at"`
}
type UpstreamBalanceEntries struct {
	Records map[int64]*UpstreamBalanceRecord
	States  map[int64]*UpstreamBalanceState
	Kinds   map[int64]*UpstreamBalanceKindRecord
}

type UpstreamBalanceStore interface {
	Load(context.Context, []int64) (*UpstreamBalanceEntries, error)
	SaveSuccess(context.Context, int64, *UpstreamBalanceRecord, *UpstreamBalanceState) error
	SaveAttempt(context.Context, int64, *UpstreamBalanceState) error
	SaveKind(context.Context, int64, *UpstreamBalanceKindRecord, time.Duration) error
	DeleteKind(context.Context, int64) error
	GetSite(context.Context, string) (*UpstreamBalanceSite, error)
	SaveSite(context.Context, string, *UpstreamBalanceSite, time.Duration) error
	TryLockOrigin(context.Context, string, string, time.Duration) (bool, error)
	GetCooldown(context.Context, string) (*time.Time, error)
	SaveCooldown(context.Context, string, time.Time) error
}
