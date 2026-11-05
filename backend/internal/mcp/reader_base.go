package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lib/pq"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const readerVersion = "readonly-v1"

type diagnosticReader struct{ db *ReaderDB }

func NewQueryReader(db *ReaderDB) QueryReader { return &diagnosticReader{db: db} }

type readerPage struct {
	Window TimeWindow `json:"w"`
	At     time.Time  `json:"a"`
	ID     int64      `json:"i"`
	Source string     `json:"s"`
}
type readerQuery struct {
	request QueryRequest
	window  TimeWindow
	limit   int
	page    readerPage
	binding string
	result  QueryResult
}

func newReaderQuery(req QueryRequest, now time.Time) (*readerQuery, error) {
	in := req.Input
	known := false
	for _, policy := range ToolPolicies() {
		if policy.Name == req.Tool {
			known = true
			break
		}
	}
	if !known {
		return nil, ErrInvalidInput
	}
	if req.Actor.UserID <= 0 || in.Limit < 0 || in.Limit > 100 || in.GroupID < 0 || in.AccountID < 0 || in.UserID < 0 || in.APIKeyID < 0 || in.ErrorRowID < 0 || in.UsageRowID < 0 || len(in.AccountIDs) > 100 {
		return nil, ErrInvalidInput
	}
	for _, id := range in.AccountIDs {
		if id <= 0 {
			return nil, ErrInvalidInput
		}
	}
	for _, s := range []string{in.RequestID, in.ClientRequestID, in.Platform, in.Model, in.Status, in.Search, in.Reason, in.Level, in.Component, in.Granularity} {
		if len(s) > 256 || !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return nil, ErrInvalidInput
		}
	}
	maxWindow := 24 * time.Hour
	defaultWindow := time.Hour
	switch req.Tool {
	case "xy2api.ops.get_system_events":
		maxWindow = 6 * time.Hour
	case "xy2api.ops.get_realtime_snapshot":
		maxWindow = time.Hour
		defaultWindow = 5 * time.Minute
	case "xy2api.ops.get_error_breakdown", "xy2api.ops.get_trends", "xy2api.groups.get_usage_summary", "xy2api.channels.get_metrics", "xy2api.users.get_usage_summary":
		defaultWindow = 24 * time.Hour
	}
	q := &readerQuery{request: req, limit: in.Limit}
	if q.limit == 0 {
		q.limit = 20
	}
	if req.Tool == "xy2api.ops.get_concurrency_snapshot" {
		q.limit = 100
	}
	normalized := in
	normalized.Cursor = ""
	normalized.Limit = q.limit
	bindingBytes, err := json.Marshal(struct {
		Version, Tool, Role, Grant, Client string
		User, TokenVersion                 int64
		Input                              QueryInput
	}{readerVersion, req.Tool, req.Actor.Role, req.Actor.GrantID, req.Actor.ClientID, req.Actor.UserID, req.Actor.TokenVersion, normalized})
	if err != nil {
		return nil, ErrInvalidInput
	}
	sum := sha256.Sum256(bindingBytes)
	q.binding = hex.EncodeToString(sum[:])
	if in.Cursor != "" {
		if err := req.Cursor.Decode(in.Cursor, q.binding, &q.page); err != nil {
			return nil, err
		}
		q.window = q.page.Window
	} else {
		q.window.End = now.UTC()
		if in.EndTime != "" {
			q.window.End, err = time.Parse(time.RFC3339Nano, in.EndTime)
			if err != nil {
				return nil, ErrInvalidInput
			}
		}
		q.window.Start = q.window.End.Add(-defaultWindow)
		if in.StartTime != "" {
			q.window.Start, err = time.Parse(time.RFC3339Nano, in.StartTime)
			if err != nil {
				return nil, ErrInvalidInput
			}
		}
		q.window.Start = q.window.Start.UTC()
		q.window.End = q.window.End.UTC()
	}
	if !q.window.Start.Before(q.window.End) || q.window.End.Sub(q.window.Start) > maxWindow || q.window.End.After(now.Add(time.Minute)) {
		return nil, ErrInvalidInput
	}
	if in.StartTime != "" {
		t, e := time.Parse(time.RFC3339Nano, in.StartTime)
		if e != nil || !t.Equal(q.window.Start) {
			return nil, ErrInvalidInput
		}
	}
	if in.EndTime != "" {
		t, e := time.Parse(time.RFC3339Nano, in.EndTime)
		if e != nil || !t.Equal(q.window.End) {
			return nil, ErrInvalidInput
		}
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return nil, ErrUnavailable
	}
	filters := map[string]any{}
	b, _ := json.Marshal(normalized)
	_ = json.Unmarshal(b, &filters)
	delete(filters, "start_time")
	delete(filters, "end_time")
	q.result = QueryResult{QueryID: hex.EncodeToString(id), GeneratedAt: now.UTC(), Window: &q.window, FiltersApplied: filters, Source: []string{}, Coverage: []SourceCoverage{}, PartialReasons: []string{"retention_unknown"}, RetentionLimited: false, RetentionStatus: "unknown", Items: []map[string]any{}}
	return q, nil
}

func (r *diagnosticReader) Query(ctx context.Context, req QueryRequest) (QueryResult, error) {
	q, err := newReaderQuery(req, time.Now())
	if err != nil {
		return QueryResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return QueryResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	switch req.Tool {
	case "xy2api.requests.search":
		err = q.searchRequests(ctx, tx)
	case "xy2api.requests.get_detail":
		err = q.errorDetail(ctx, tx, false)
	case "xy2api.requests.get_attempts":
		err = q.attempts(ctx, tx)
	case "xy2api.requests.search_ingress_rejections":
		err = q.ingress(ctx, tx)
	case "xy2api.accounts.search", "xy2api.groups.search", "xy2api.channels.search", "xy2api.users.search":
		err = q.directory(ctx, tx)
	case "xy2api.groups.get_usage_summary":
		err = q.groupUsage(ctx, tx)
	case "xy2api.channels.get_metrics":
		err = q.channelMetrics(ctx, tx)
	case "xy2api.ops.get_error_breakdown", "xy2api.ops.get_trends", "xy2api.ops.get_alerts", "xy2api.ops.get_system_events", "xy2api.ops.get_realtime_snapshot", "xy2api.audit.search", "xy2api.security.search_prompt_audit":
		err = q.ops(ctx, tx)
	case "xy2api.users.get_usage_summary":
		err = q.userUsageSummary(ctx, tx)
	case "xy2api.users.get_subscriptions", "xy2api.me.get_subscription_status":
		err = q.subscriptions(ctx, tx)
	case "xy2api.me.get_profile":
		err = q.profile(ctx, tx)
	case "xy2api.me.search_usage", "xy2api.me.get_usage_detail":
		err = q.selfUsage(ctx, tx)
	case "xy2api.me.get_error_history", "xy2api.me.get_error_detail":
		err = q.errorDetail(ctx, tx, true)
	case "xy2api.me.get_quota_status":
		err = q.quotas(ctx, tx)
	default:
		err = ErrInvalidInput
	}
	if err != nil {
		return QueryResult{}, readerError(ctx, err)
	}
	if err = tx.Commit(); err != nil {
		return QueryResult{}, readerError(ctx, err)
	}
	return q.result, nil
}
func (r *diagnosticReader) begin(ctx context.Context) (*sql.Tx, error) {
	if r == nil || r.db == nil || r.db.DB == nil {
		return nil, ErrUnavailable
	}
	tx, err := r.db.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, readerError(ctx, err)
	}
	for _, s := range []string{"SET LOCAL search_path = pg_catalog, public", "SET LOCAL statement_timeout = '2s'", "SET LOCAL lock_timeout = '100ms'"} {
		if _, err = tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return nil, readerError(ctx, err)
		}
	}
	return tx, nil
}
func readerError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var pgError *pq.Error
	if errors.As(err, &pgError) && pgError.Code == "57014" {
		return context.DeadlineExceeded
	}
	for _, safe := range []error{ErrInvalidInput, ErrNotFound, ErrForbidden, ErrUnavailable, ErrSourceDisabled, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ErrUnavailable
}
func (q *readerQuery) source(names ...string) {
	for _, n := range names {
		q.result.Source = append(q.result.Source, n)
		q.result.Coverage = append(q.result.Coverage, SourceCoverage{Source: n, Available: true, Note: "retention_unknown"})
	}
}
func (q *readerQuery) noHistoricalRetention() {
	q.result.RetentionStatus = "not_applicable"
	q.result.RetentionLimited = false
	reasons := q.result.PartialReasons[:0]
	for _, reason := range q.result.PartialReasons {
		if reason != "retention_unknown" {
			reasons = append(reasons, reason)
		}
	}
	q.result.PartialReasons = reasons
	for i := range q.result.Coverage {
		if q.result.Coverage[i].Note == "retention_unknown" {
			q.result.Coverage[i].Note = "not_applicable"
		}
	}
}
func (q *readerQuery) knownCoverageLimit(reason string) {
	q.result.RetentionStatus = "limited"
	q.result.RetentionLimited = true
	q.result.PartialReasons = append(q.result.PartialReasons, reason)
}
func (q *readerQuery) require(ctx context.Context, key string) error {
	if q.request.RequireSource == nil {
		return ErrForbidden
	}
	return q.request.RequireSource(ctx, key)
}

// rowMaps only scans columns from the reviewed projections below. Raw JSON and
// complete domain/Ent values never cross this boundary.
func rowMaps(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		item := make(map[string]any, len(names))
		for i, n := range names {
			v := values[i]
			if b, ok := v.([]byte); ok {
				switch types[i].DatabaseTypeName() {
				case "NUMERIC", "DECIMAL", "FLOAT8", "FLOAT4":
					v, err = strconv.ParseFloat(string(b), 64)
					if err != nil {
						return nil, err
					}
				case "_INT8", "INT8[]":
					var ids pq.Int64Array
					if err = ids.Scan(b); err != nil {
						return nil, err
					}
					v = []int64(ids)
				default:
					v = string(b)
				}
			}
			if t, ok := v.(time.Time); ok {
				v = t.UTC()
			}
			item[n] = v
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (q *readerQuery) load(ctx context.Context, tx *sql.Tx, s string, args ...any) error {
	items, err := rowMaps(ctx, tx, s, args...)
	if err == nil {
		q.result.Items = items
	}
	return err
}
func (q *readerQuery) paged(ctx context.Context, tx *sql.Tx, s string, args []any) error {
	args = append(args, q.page.At, q.page.ID, q.page.Source, q.limit+1)
	query := `SELECT * FROM (` + s + `) AS page_rows WHERE ($` + strconv.Itoa(len(args)-3) + `::timestamptz = '0001-01-01T00:00:00Z'::timestamptz OR (_page_time,_page_id,_page_source) < ($` + strconv.Itoa(len(args)-3) + `,$` + strconv.Itoa(len(args)-2) + `,$` + strconv.Itoa(len(args)-1) + `)) ORDER BY _page_time DESC,_page_id DESC,_page_source DESC LIMIT $` + strconv.Itoa(len(args))
	items, err := rowMaps(ctx, tx, query, args...)
	if err != nil {
		return err
	}
	more := len(items) > q.limit
	if more {
		items = items[:q.limit]
	}
	if more {
		last := items[len(items)-1]
		at, ok := last["_page_time"].(time.Time)
		if !ok {
			return ErrUnavailable
		}
		id, ok := last["_page_id"].(int64)
		if !ok {
			return ErrUnavailable
		}
		source, ok := last["_page_source"].(string)
		if !ok {
			return ErrUnavailable
		}
		q.result.NextCursor, err = q.request.Cursor.Encode(q.binding, readerPage{q.window, at, id, source})
		if err != nil {
			return err
		}
	}
	for _, item := range items {
		delete(item, "_page_time")
		delete(item, "_page_id")
		delete(item, "_page_source")
	}
	q.result.Items = items
	return nil
}
func addEqual(where *[]string, args *[]any, column string, value any) {
	*args = append(*args, value)
	*where = append(*where, fmt.Sprintf("%s = $%d", column, len(*args)))
}
func (q *readerQuery) filters(alias string, args *[]any) []string {
	w := []string{}
	in := q.request.Input
	for _, f := range []struct {
		c string
		v string
	}{{"platform", in.Platform}, {"model", in.Model}, {"request_id", in.RequestID}, {"client_request_id", in.ClientRequestID}} {
		if f.v != "" {
			addEqual(&w, args, alias+f.c, f.v)
		}
	}
	for _, f := range []struct {
		c string
		v int64
	}{{"group_id", in.GroupID}, {"account_id", in.AccountID}, {"user_id", in.UserID}, {"api_key_id", in.APIKeyID}} {
		if f.v > 0 {
			addEqual(&w, args, alias+f.c, f.v)
		}
	}
	return w
}
func andWhere(w []string) string {
	if len(w) == 0 {
		return "TRUE"
	}
	return strings.Join(w, " AND ")
}
