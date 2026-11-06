// Package mcp implements the authenticated, read-only diagnostic MCP surface.
// OAuth control-plane storage and diagnostic readers are separate dependencies.
package mcp

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
)

// Actor is rebuilt from the current local user and operator permissions. It is
// never decoded from untrusted role claims or accepted as a tool argument.
type Actor struct {
	UserID       int64
	Role         string
	TokenVersion int64
	Scope        *operatoraccess.Scope
	ClientID     string
	GrantID      string
	GrantedTools []string
}

// QueryInput is the union of the fixed tools' parameters. The registry exposes
// a narrower JSON schema for each tool and rejects all additional properties.
type QueryInput struct {
	StartTime                string  `json:"start_time,omitempty"`
	EndTime                  string  `json:"end_time,omitempty"`
	Limit                    int     `json:"limit,omitempty"`
	Cursor                   string  `json:"cursor,omitempty"`
	RequestID                string  `json:"request_id,omitempty"`
	ClientRequestID          string  `json:"client_request_id,omitempty"`
	Platform                 string  `json:"platform,omitempty"`
	Model                    string  `json:"model,omitempty"`
	GroupID                  int64   `json:"group_id,omitempty"`
	AccountID                int64   `json:"account_id,omitempty"`
	UserID                   int64   `json:"user_id,omitempty"`
	APIKeyID                 int64   `json:"api_key_id,omitempty"`
	AccountIDs               []int64 `json:"account_ids,omitempty"`
	ErrorRowID               int64   `json:"error_row_id,omitempty"`
	UsageRowID               int64   `json:"usage_row_id,omitempty"`
	Status                   string  `json:"status,omitempty"`
	Search                   string  `json:"search,omitempty"`
	Reason                   string  `json:"reason,omitempty"`
	Level                    string  `json:"level,omitempty"`
	Component                string  `json:"component,omitempty"`
	Granularity              string  `json:"granularity,omitempty"`
	IncludeRecoveredUpstream bool    `json:"include_recovered_upstream,omitempty"`
	IncludePolicies          bool    `json:"include_policies,omitempty"`
}

type TimeWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type SourceCoverage struct {
	Source      string `json:"source"`
	Available   bool   `json:"available"`
	SampleCount int64  `json:"sample_count,omitempty"`
	Note        string `json:"note,omitempty"`
}

// QueryResult carries only fixed, reviewed scalar projections. Items must never
// contain credential objects, raw Entity values or unparsed JSON payloads.
type QueryResult struct {
	SchemaVersion        string           `json:"schema_version"`
	QueryID              string           `json:"query_id"`
	Error                *QueryError      `json:"error,omitempty"`
	Source               []string         `json:"source"`
	GeneratedAt          time.Time        `json:"generated_at"`
	Window               *TimeWindow      `json:"window,omitempty"`
	FiltersApplied       map[string]any   `json:"filters_applied,omitempty"`
	Coverage             []SourceCoverage `json:"coverage"`
	PartialReasons       []string         `json:"partial_reasons"`
	RetentionLimited     bool             `json:"retention_limited"`
	RetentionStatus      string           `json:"retention_status"`
	AggregationWatermark *time.Time       `json:"aggregation_watermark,omitempty"`
	StateScope           string           `json:"state_scope,omitempty"`
	Items                []map[string]any `json:"items"`
	NextCursor           string           `json:"next_cursor,omitempty"`
	SourceDisabled       bool             `json:"source_disabled,omitempty"`
}

// QueryError is a stable public error. Messages and recovery hints are fixed;
// database errors and arbitrary upstream text never enter this structure.
type QueryError struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	SuggestedAction string `json:"suggested_action,omitempty"`
}

type QueryRequest struct {
	Tool  string
	Actor Actor
	Input QueryInput
	// RequireSource is restricted to the current tool's optional source set.
	// A reader uses it after minimal row classification, before loading an
	// additionally protected source (notably recovered upstream attempts).
	RequireSource func(context.Context, string) error
	// Cursor is shared by the fixed readers; the auth server owns its key.
	Cursor *CursorCodec
}

type QueryReader interface {
	Query(context.Context, QueryRequest) (QueryResult, error)
}

// ReaderDB is a distinct Wire type; it is never the application's writer DB.
// A nil DB means MCP is disabled and readers must not open a fallback handle.
type ReaderDB struct{ DB *sql.DB }
type RuntimeReader interface {
	QueryRuntime(context.Context, QueryRequest) (QueryResult, error)
}

var (
	ErrInvalidInput   = errors.New("invalid MCP query parameters")
	ErrNotFound       = errors.New("MCP record not found")
	ErrForbidden      = errors.New("MCP permission denied")
	ErrUnavailable    = errors.New("MCP data source unavailable")
	ErrSourceDisabled = errors.New("MCP data source disabled")
)

type ToolPolicy struct {
	Name         string
	CatalogKeys  []string
	OptionalKeys []string
	UserOwned    bool
	Ops          bool
	Realtime     bool
	UserError    bool
	TargetUser   bool
	Runtime      bool
	Parameters   []string
	Required     []string
}

// ConsentPreview and Connection are the exact frontend API response contracts.
type ConsentPreview struct {
	RequestID          string    `json:"request_id"`
	ClientID           string    `json:"client_id"`
	ClientName         string    `json:"client_name"`
	ClientNameVerified bool      `json:"client_name_verified"`
	RedirectURI        string    `json:"redirect_uri"`
	Resource           string    `json:"resource"`
	Tools              []string  `json:"tools"`
	ExpiresAt          time.Time `json:"expires_at"`
}
type ConsentDecision struct {
	RequestID string   `json:"request_id"`
	Approve   bool     `json:"approve"`
	Tools     []string `json:"tools"`
}
type ConsentResult struct {
	RedirectURL string `json:"redirect_url"`
}
type Connection struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"client_id"`
	ClientName string     `json:"client_name"`
	Resource   string     `json:"resource"`
	Tools      []string   `json:"tools"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}
