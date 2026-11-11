package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type emailCampaignRepository struct {
	db     *sql.DB
	client *ent.Client
}

func NewEmailCampaignRepository(db *sql.DB, client *ent.Client) service.EmailCampaignRepository {
	return &emailCampaignRepository{db, client}
}
func (r *emailCampaignRepository) ReadSecret(ctx context.Context) (string, error) {
	var secret string
	e := r.db.QueryRowContext(ctx, `SELECT value FROM security_secrets WHERE key='email_campaign_unsubscribe_secret'`).Scan(&secret)
	if errors.Is(e, sql.ErrNoRows) {
		return "", service.ErrEmailCampaignInvalidInput
	}
	return secret, e
}
func (r *emailCampaignRepository) Secret(ctx context.Context) (string, error) {
	secret, _, e := getOrCreateGeneratedSecuritySecret(ctx, r.client, "email_campaign_unsubscribe_secret", 32)
	return secret, e
}
func campaignDBError(e error) error {
	var pg *pq.Error
	if errors.As(e, &pg) && pg.Code == "23505" && pg.Constraint == "email_campaigns_one_active" {
		return service.ErrEmailCampaignActiveExists
	}
	if errors.Is(e, sql.ErrNoRows) {
		return service.ErrEmailCampaignNotFound
	}
	return e
}

// Both preview and snapshot use these exact predicates. Deleted API keys still count as activity.
const campaignAudienceSQL = `WITH scoped AS (
 SELECT u.id,u.email,lower(btrim(u.email)) AS email_norm,u.username,u.created_at,
 GREATEST(u.last_active_at,u.last_login_at,(SELECT max(k.last_used_at) FROM api_keys k WHERE k.user_id=u.id)) AS last_activity_at,
 CASE WHEN btrim(u.email)='' OR lower(btrim(u.email)) LIKE '%.invalid' OR btrim(u.email) !~ '^[^@\s]+@[^@\s]+\.[^@\s]+$' THEN 'invalid'
 WHEN EXISTS (SELECT 1 FROM email_marketing_suppressions s WHERE s.email_norm=lower(btrim(u.email)) OR s.user_id=u.id) THEN 'suppressed'
 WHEN u.id=ANY($4::bigint[]) THEN 'excluded' ELSE '' END AS reason
 FROM users u WHERE u.deleted_at IS NULL AND u.status='active' AND u.role='user'
 AND ($1::timestamptz IS NULL OR u.created_at >= $1) AND ($2::timestamptz IS NULL OR u.created_at < $2)
 AND ($3::timestamptz IS NULL OR (u.created_at <= $3 AND (u.last_active_at IS NULL OR u.last_active_at < $3)
 AND (u.last_login_at IS NULL OR u.last_login_at < $3) AND NOT EXISTS (SELECT 1 FROM api_keys k WHERE k.user_id=u.id AND k.last_used_at >= $3)))
), ranked AS (SELECT *,row_number() OVER (PARTITION BY email_norm,reason ORDER BY id) AS n FROM scoped),
 audience AS (SELECT DISTINCT ON (email_norm) * FROM ranked WHERE reason='' ORDER BY email_norm,id) `

func campaignAudienceArgs(q service.EmailCampaignAudienceQuery) []any {
	return []any{q.From, q.To, q.Cutoff, pq.Array(q.ExcludeUserIDs)}
}

// Resolve all email and ID entries in one scan, shared by exclusions and suppressions.
func campaignLookup(ctx context.Context, tx *sql.Tx, entries []string) (map[string][]int64, map[int64]string, error) {
	emails, ids := []string{}, []int64{}
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if strings.Contains(entry, "@") {
			emails = append(emails, service.NormalizeCampaignEmail(entry))
		} else if id, err := strconv.ParseInt(entry, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	byEmail, byID := map[string][]int64{}, map[int64]string{}
	if len(emails) == 0 && len(ids) == 0 {
		return byEmail, byID, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,lower(btrim(email)) FROM users WHERE lower(btrim(email))=ANY($1::text[]) OR id=ANY($2::bigint[]) ORDER BY id`, pq.Array(emails), pq.Array(ids))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var email string
		if err = rows.Scan(&id, &email); err != nil {
			return nil, nil, err
		}
		byEmail[email] = append(byEmail[email], id)
		byID[id] = email
	}
	return byEmail, byID, rows.Err()
}
func campaignResolve(ctx context.Context, tx *sql.Tx, entries []string) ([]int64, []string, error) {
	byEmail, byID, err := campaignLookup(ctx, tx, entries)
	if err != nil {
		return nil, nil, err
	}
	ids, missing, seen := []int64{}, []string{}, map[int64]bool{}
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		found := byEmail[service.NormalizeCampaignEmail(entry)]
		if !strings.Contains(entry, "@") {
			id, _ := strconv.ParseInt(entry, 10, 64)
			if _, ok := byID[id]; ok {
				found = []int64{id}
			}
		}
		if len(found) == 0 {
			missing = append(missing, raw)
		}
		for _, id := range found {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, missing, nil
}
func campaignAudienceCounts(ctx context.Context, tx *sql.Tx, q service.EmailCampaignAudienceQuery) (service.EmailCampaignSkipped, int64, error) {
	var skip service.EmailCampaignSkipped
	var matched int64
	e := tx.QueryRowContext(ctx, campaignAudienceSQL+`SELECT count(*) FILTER (WHERE reason='invalid'),count(*) FILTER (WHERE reason='suppressed'),count(*) FILTER (WHERE reason='excluded'),count(*) FILTER (WHERE reason='' AND n>1),count(*) FILTER (WHERE reason='' AND n=1) FROM ranked`, campaignAudienceArgs(q)...).Scan(&skip.NoValidEmail, &skip.Suppressed, &skip.Excluded, &skip.Duplicate, &matched)
	return skip, matched, e
}
func (r *emailCampaignRepository) Preview(ctx context.Context, q service.EmailCampaignAudienceQuery, page, size int) (*service.EmailCampaignPreview, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback() }()
	ids, missing, e := campaignResolve(ctx, tx, q.Exclude)
	if e != nil {
		return nil, e
	}
	q.ExcludeUserIDs = ids
	skip, total, e := campaignAudienceCounts(ctx, tx, q)
	if e != nil {
		return nil, e
	}
	args := append(campaignAudienceArgs(q), size, (page-1)*size)
	rows, e := tx.QueryContext(ctx, campaignAudienceSQL+`SELECT id,btrim(email),username,created_at,last_activity_at FROM audience ORDER BY email_norm,id LIMIT $5 OFFSET $6`, args...)
	if e != nil {
		return nil, e
	}
	items := []service.EmailCampaignAudienceUser{}
	for rows.Next() {
		var v service.EmailCampaignAudienceUser
		if e = rows.Scan(&v.UserID, &v.Email, &v.Username, &v.CreatedAt, &v.LastActivityAt); e != nil {
			_ = rows.Close()
			return nil, e
		}
		items = append(items, v)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return &service.EmailCampaignPreview{EmailCampaignPage: service.CampaignPage(items, total, page, size), Matched: total, Skipped: skip, ExcludeResolved: len(ids), ExcludeNotFound: missing}, nil
}
func (r *emailCampaignRepository) Create(ctx context.Context, in service.EmailCampaignCreate, q service.EmailCampaignAudienceQuery, admin int64) (*service.EmailCampaign, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback() }()
	ids, _, e := campaignResolve(ctx, tx, q.Exclude)
	if e != nil {
		return nil, e
	}
	q.ExcludeUserIDs = ids
	skip, total, e := campaignAudienceCounts(ctx, tx, q)
	if e != nil {
		return nil, e
	}
	if total == 0 {
		return nil, service.ErrEmailCampaignAudienceEmpty
	}
	q.Skipped = skip
	q.Exclude = nil
	snapshot := struct {
		service.EmailCampaignAudienceSnapshot
		From   *time.Time `json:"resolved_from"`
		To     *time.Time `json:"resolved_to"`
		Cutoff *time.Time `json:"resolved_cutoff"`
	}{q.EmailCampaignAudienceSnapshot, q.From, q.To, q.Cutoff}
	data, e := json.Marshal(snapshot)
	if e != nil {
		return nil, e
	}
	var id int64
	e = tx.QueryRowContext(ctx, `INSERT INTO email_campaigns(subject,body_markdown,audience,status,created_by) VALUES($1,$2,$3,'sending',$4) RETURNING id`, in.Subject, in.BodyMarkdown, string(data), admin).Scan(&id)
	if e != nil {
		return nil, campaignDBError(e)
	}
	args := append(campaignAudienceArgs(q), id)
	_, e = tx.ExecContext(ctx, campaignAudienceSQL+`INSERT INTO email_campaign_recipients(campaign_id,user_id,email,email_norm) SELECT $5,id,btrim(email),email_norm FROM audience`, args...)
	if e != nil {
		return nil, e
	}
	c, e := campaignGet(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	if e = campaignWaiting(ctx, tx, c); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, campaignDBError(e)
	}
	return c, nil
}

type campaignQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const campaignColumns = `c.id,c.subject,c.body_markdown,c.status,c.pause_reason,c.last_error,c.consecutive_failures,c.audience,c.created_at,c.started_at,c.finished_at,c.updated_at,u.id,COALESCE(u.email,''),
 (SELECT json_build_object('total',count(*),'pending',count(*) FILTER(WHERE status='pending'),'sending',count(*) FILTER(WHERE status='sending'),'sent',count(*) FILTER(WHERE status='sent'),'failed',count(*) FILTER(WHERE status='failed'),'skipped',count(*) FILTER(WHERE status='skipped'),'uncertain',count(*) FILTER(WHERE status='failed' AND outcome_uncertain)) FROM email_campaign_recipients WHERE campaign_id=c.id)`

func scanCampaign(row interface{ Scan(...any) error }) (*service.EmailCampaign, error) {
	var c service.EmailCampaign
	var creator sql.NullInt64
	var email string
	var audience, counts []byte
	e := row.Scan(&c.ID, &c.Subject, &c.BodyMarkdown, &c.Status, &c.PauseReason, &c.LastError, &c.ConsecutiveFailures, &audience, &c.CreatedAt, &c.StartedAt, &c.FinishedAt, &c.UpdatedAt, &creator, &email, &counts)
	if e != nil {
		return nil, campaignDBError(e)
	}
	if e = json.Unmarshal(audience, &c.Audience); e != nil {
		return nil, e
	}
	if e = json.Unmarshal(counts, &c.Counts); e != nil {
		return nil, e
	}
	if creator.Valid {
		c.CreatedBy = &service.EmailCampaignCreator{ID: creator.Int64, Email: email}
	}
	return &c, nil
}
func campaignGet(ctx context.Context, q campaignQuerier, id int64) (*service.EmailCampaign, error) {
	return scanCampaign(q.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM email_campaigns c LEFT JOIN users u ON u.id=c.created_by WHERE c.id=$1`, id))
}
func campaignLimits(ctx context.Context, q campaignQuerier, lock bool) (service.EmailCampaignLimits, error) {
	l := service.DefaultEmailCampaignLimits()
	var raw string
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	e := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`+suffix, service.EmailCampaignLimitsKey).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return l, nil
	}
	if e != nil {
		return l, e
	}
	if e = json.Unmarshal([]byte(raw), &l); e != nil || !l.Valid() {
		return l, service.ErrEmailCampaignInvalidInput
	}
	return l, nil
}
func campaignStats(ctx context.Context, q campaignQuerier) (int64, int64, error) {
	var minute, day int64
	e := q.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE sent_at>now()-interval '60 seconds'),count(*) FROM email_send_log WHERE sent_at>now()-interval '24 hours'`).Scan(&minute, &day)
	return minute, day, e
}
func campaignWaiting(ctx context.Context, q campaignQuerier, c *service.EmailCampaign) error {
	if c.Status != "sending" {
		return nil
	}
	limits, e := campaignLimits(ctx, q, false)
	if e != nil {
		return e
	}
	minute, day, e := campaignStats(ctx, q)
	if e != nil {
		return e
	}
	if day >= int64(limits.PerDay) {
		c.WaitingForLimit = "day"
	} else if minute >= int64(limits.PerMinute) {
		c.WaitingForLimit = "minute"
	}
	return nil
}
func (r *emailCampaignRepository) Get(ctx context.Context, id int64) (*service.EmailCampaign, error) {
	c, e := campaignGet(ctx, r.db, id)
	if e != nil {
		return nil, e
	}
	return c, campaignWaiting(ctx, r.db, c)
}
func (r *emailCampaignRepository) List(ctx context.Context, status string, page, size int) (service.EmailCampaignPage[service.EmailCampaign], error) {
	out := service.CampaignPage[service.EmailCampaign](nil, 0, page, size)
	e := r.db.QueryRowContext(ctx, `SELECT count(*) FROM email_campaigns WHERE ($1='' OR status=$1)`, status).Scan(&out.Total)
	if e != nil {
		return out, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT `+campaignColumns+` FROM email_campaigns c LEFT JOIN users u ON u.id=c.created_by WHERE ($1='' OR c.status=$1) ORDER BY c.id DESC LIMIT $2 OFFSET $3`, status, size, (page-1)*size)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Items = append(out.Items, *c)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return out, e
	}
	for i := range out.Items {
		if e = campaignWaiting(ctx, r.db, &out.Items[i]); e != nil {
			return out, e
		}
	}
	return service.CampaignPage(out.Items, out.Total, page, size), nil
}
func (r *emailCampaignRepository) Recipients(ctx context.Context, id int64, status, search string, page, size int) (service.EmailCampaignPage[service.EmailCampaignRecipient], error) {
	out := service.CampaignPage[service.EmailCampaignRecipient](nil, 0, page, size)
	if _, e := campaignGet(ctx, r.db, id); e != nil {
		return out, e
	}
	const filter = ` FROM email_campaign_recipients WHERE campaign_id=$1 AND ($2='' OR status=$2) AND ($3='' OR email_norm LIKE '%'||lower($3)||'%')`
	e := r.db.QueryRowContext(ctx, `SELECT count(*)`+filter, id, status, search).Scan(&out.Total)
	if e != nil {
		return out, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT user_id,email,status,last_error,outcome_uncertain,claimed_at,sent_at`+filter+` ORDER BY user_id LIMIT $4 OFFSET $5`, id, status, search, size, (page-1)*size)
	if e != nil {
		return out, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v service.EmailCampaignRecipient
		if e = rows.Scan(&v.UserID, &v.Email, &v.Status, &v.LastError, &v.Uncertain, &v.ClaimedAt, &v.SentAt); e != nil {
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	return service.CampaignPage(out.Items, out.Total, page, size), rows.Err()
}
func campaignLock(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var status string
	e := tx.QueryRowContext(ctx, `SELECT status FROM email_campaigns WHERE id=$1 FOR UPDATE`, id).Scan(&status)
	return status, campaignDBError(e)
}
func (r *emailCampaignRepository) Action(ctx context.Context, id int64, action string) (*service.EmailCampaign, int64, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, 0, e
	}
	defer func() { _ = tx.Rollback() }()
	status, e := campaignLock(ctx, tx, id)
	if e != nil {
		return nil, 0, e
	}
	next, reason := "", ""
	var requeued int64
	switch action {
	case "pause":
		if status != "sending" {
			return nil, 0, service.ErrEmailCampaignInvalidState
		}
		next, reason = "paused", "manual"
	case "resume":
		if status != "paused" {
			return nil, 0, service.ErrEmailCampaignInvalidState
		}
		next = "sending"
	case "cancel":
		if status != "sending" && status != "paused" {
			return nil, 0, service.ErrEmailCampaignInvalidState
		}
		next = "canceled"
		_, e = tx.ExecContext(ctx, `UPDATE email_campaign_recipients SET status='skipped',last_error='群发已取消' WHERE campaign_id=$1 AND status='pending'`, id)
	case "retry-failed":
		if status != "paused" && status != "completed" {
			return nil, 0, service.ErrEmailCampaignInvalidState
		}
		next = "sending"
		var result sql.Result
		result, e = tx.ExecContext(ctx, `UPDATE email_campaign_recipients SET status='pending',outcome_uncertain=false,last_error='',claimed_at=NULL WHERE campaign_id=$1 AND status='failed'`, id)
		if e == nil {
			requeued, e = result.RowsAffected()
		}
		if e == nil && requeued == 0 {
			return nil, 0, service.ErrEmailCampaignInvalidState
		}
	default:
		return nil, 0, service.ErrEmailCampaignInvalidInput
	}
	if e != nil {
		return nil, 0, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE email_campaigns SET status=$2::text,pause_reason=$3,consecutive_failures=CASE WHEN $2::text='sending' THEN 0 ELSE consecutive_failures END,last_error=CASE WHEN $2::text='sending' THEN '' ELSE last_error END,finished_at=CASE WHEN $2::text='canceled' THEN now() ELSE NULL END,updated_at=now() WHERE id=$1`, id, next, reason)
	if e != nil {
		return nil, 0, campaignDBError(e)
	}
	c, e := campaignGet(ctx, tx, id)
	if e != nil {
		return nil, 0, e
	}
	if e = campaignWaiting(ctx, tx, c); e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, campaignDBError(e)
	}
	return c, requeued, nil
}

func (r *emailCampaignRepository) Suppressions(ctx context.Context, search string, page, size int) (service.EmailCampaignPage[service.EmailCampaignSuppression], error) {
	out := service.CampaignPage[service.EmailCampaignSuppression](nil, 0, page, size)
	const filter = ` FROM email_marketing_suppressions s LEFT JOIN users u ON u.id=s.user_id LEFT JOIN users a ON a.id=s.created_by LEFT JOIN email_campaigns c ON c.id=s.campaign_id WHERE ($1='' OR s.email_norm LIKE '%'||lower($1)||'%' OR COALESCE(u.username,'') ILIKE '%'||$1||'%')`
	e := r.db.QueryRowContext(ctx, `SELECT count(*)`+filter, search).Scan(&out.Total)
	if e != nil {
		return out, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT s.id,s.email_norm,s.user_id,COALESCE(u.username,''),s.source,s.note,s.campaign_id,COALESCE(c.subject,''),a.id,COALESCE(a.email,''),s.created_at`+filter+` ORDER BY s.id DESC LIMIT $2 OFFSET $3`, search, size, (page-1)*size)
	if e != nil {
		return out, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v service.EmailCampaignSuppression
		var admin *int64
		var email string
		if e = rows.Scan(&v.ID, &v.Email, &v.UserID, &v.Username, &v.Source, &v.Note, &v.CampaignID, &v.CampaignSubject, &admin, &email, &v.CreatedAt); e != nil {
			return out, e
		}
		if admin != nil {
			v.CreatedBy = &service.EmailCampaignCreator{ID: *admin, Email: email}
		}
		out.Items = append(out.Items, v)
	}
	return service.CampaignPage(out.Items, out.Total, page, size), rows.Err()
}
func (r *emailCampaignRepository) AddSuppressions(ctx context.Context, entries []string, note string, admin int64) (*service.EmailCampaignSuppressionResult, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback() }()
	byEmail, byID, e := campaignLookup(ctx, tx, entries)
	if e != nil {
		return nil, e
	}
	out := &service.EmailCampaignSuppressionResult{NotFound: []string{}}
	for _, raw := range entries {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		var uid *int64
		email := service.NormalizeCampaignEmail(value)
		if strings.Contains(value, "@") {
			if !service.ValidCampaignEmail(email) {
				out.NotFound = append(out.NotFound, raw)
				continue
			}
			if matches := byEmail[email]; len(matches) > 0 {
				uid = &matches[0]
			}
		} else {
			id, err := strconv.ParseInt(value, 10, 64)
			var ok bool
			email, ok = byID[id]
			if err != nil || !ok || !service.ValidCampaignEmail(email) {
				out.NotFound = append(out.NotFound, raw)
				continue
			}
			uid = &id
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO email_marketing_suppressions(email_norm,user_id,source,note,created_by) VALUES($1,$2,'admin',$3,$4) ON CONFLICT(email_norm) DO NOTHING`, email, uid, note, admin)
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		out.Added += n
		if n == 0 {
			out.Already++
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *emailCampaignRepository) RemoveSuppression(ctx context.Context, id int64) (bool, error) {
	result, e := r.db.ExecContext(ctx, `DELETE FROM email_marketing_suppressions WHERE id=$1`, id)
	if e != nil {
		return false, e
	}
	n, e := result.RowsAffected()
	return n > 0, e
}
func (r *emailCampaignRepository) Unsubscribe(ctx context.Context, email string, campaign int64) error {
	_, e := r.db.ExecContext(ctx, `INSERT INTO email_marketing_suppressions(email_norm,user_id,source,campaign_id) VALUES($1::text,(SELECT id FROM users WHERE lower(btrim(email))=$1::text ORDER BY id LIMIT 1),'unsubscribe',(SELECT id FROM email_campaigns WHERE id=$2)) ON CONFLICT(email_norm) DO UPDATE SET source='unsubscribe',campaign_id=COALESCE(EXCLUDED.campaign_id,email_marketing_suppressions.campaign_id),user_id=COALESCE(EXCLUDED.user_id,email_marketing_suppressions.user_id)`, email, campaign)
	return e
}
func (r *emailCampaignRepository) SendStats(ctx context.Context) (int64, int64, error) {
	return campaignStats(ctx, r.db)
}

// A settings row lock serializes campaign attempts and test sends across all instances.
// Campaign callers always acquire their campaign row first, then this rate lock.
func campaignReserve(ctx context.Context, tx *sql.Tx, campaign *int64) (string, error) {
	_, e := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,'{"per_minute":20,"per_day":500}',now()) ON CONFLICT(key) DO NOTHING`, service.EmailCampaignLimitsKey)
	if e != nil {
		return "", e
	}
	limits, e := campaignLimits(ctx, tx, true)
	if e != nil {
		return "", e
	}
	_, e = tx.ExecContext(ctx, `DELETE FROM email_send_log WHERE sent_at<now()-interval '48 hours'`)
	if e != nil {
		return "", e
	}
	minute, day, e := campaignStats(ctx, tx)
	if e != nil {
		return "", e
	}
	if day >= int64(limits.PerDay) {
		return "day", nil
	}
	if minute >= int64(limits.PerMinute) {
		return "minute", nil
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO email_send_log(campaign_id) VALUES($1)`, campaign)
	return "", e
}
func (r *emailCampaignRepository) ReserveTestSend(ctx context.Context) (string, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer func() { _ = tx.Rollback() }()
	limit, e := campaignReserve(ctx, tx, nil)
	if e != nil {
		return "", e
	}
	return limit, tx.Commit()
}
func (r *emailCampaignRepository) Claim(ctx context.Context) (*service.EmailCampaignClaim, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	var status string
	// Prefer stale claims, including canceled campaigns, so they cannot be starved by a new campaign.
	e = tx.QueryRowContext(ctx, `SELECT c.id,c.status FROM email_campaigns c WHERE c.status IN ('sending','paused') OR EXISTS(SELECT 1 FROM email_campaign_recipients r WHERE r.campaign_id=c.id AND r.status='sending' AND r.claimed_at<now()-interval '5 minutes') ORDER BY EXISTS(SELECT 1 FROM email_campaign_recipients r WHERE r.campaign_id=c.id AND r.status='sending' AND r.claimed_at<now()-interval '5 minutes') DESC,c.id LIMIT 1 FOR UPDATE OF c`).Scan(&id, &status)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	recovered, e := tx.ExecContext(ctx, `UPDATE email_campaign_recipients SET status='failed',outcome_uncertain=true,last_error='发送中断，可能已送达' WHERE campaign_id=$1 AND status='sending' AND claimed_at<now()-interval '5 minutes'`, id)
	if e != nil {
		return nil, e
	}
	recoveredCount, e := recovered.RowsAffected()
	if e != nil {
		return nil, e
	}
	if recoveredCount > 0 {
		status, e = campaignRecordFailure(ctx, tx, id, "发送中断，可能已送达", false, recoveredCount)
		if e != nil {
			return nil, e
		}
	}
	var pending, sending int64
	e = tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status='sending') FROM email_campaign_recipients WHERE campaign_id=$1`, id).Scan(&pending, &sending)
	if e != nil {
		return nil, e
	}
	if pending == 0 && sending == 0 && (status == "sending" || status == "paused") {
		_, e = tx.ExecContext(ctx, `UPDATE email_campaigns SET status='completed',pause_reason='',finished_at=now(),updated_at=now() WHERE id=$1`, id)
		if e != nil {
			return nil, e
		}
		return nil, tx.Commit()
	}
	if status != "sending" || pending == 0 {
		return nil, tx.Commit()
	}
	// One in-flight send globally guarantees pause/cancel can allow at most one final delivery.
	var inFlight bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM email_campaign_recipients WHERE status='sending')`).Scan(&inFlight)
	if e != nil {
		return nil, e
	}
	if inFlight {
		return nil, tx.Commit()
	}
	limit, e := campaignReserve(ctx, tx, &id)
	if e != nil {
		return nil, e
	}
	if limit != "" {
		return nil, tx.Commit()
	}
	var recipient service.EmailCampaignRecipient
	recipient.CampaignID = id
	e = tx.QueryRowContext(ctx, `UPDATE email_campaign_recipients SET status='sending',claim_version=claim_version+1,claimed_at=now(),outcome_uncertain=false WHERE campaign_id=$1 AND user_id=(SELECT user_id FROM email_campaign_recipients WHERE campaign_id=$1 AND status='pending' ORDER BY user_id LIMIT 1) RETURNING user_id,email,status,claim_version,claimed_at`, id).Scan(&recipient.UserID, &recipient.Email, &recipient.Status, &recipient.ClaimVersion, &recipient.ClaimedAt)
	if e != nil {
		return nil, e
	}
	c, e := campaignGet(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return &service.EmailCampaignClaim{Campaign: c, Recipient: recipient}, nil
}

// clock_timestamp checks the actual time, not a transaction's start time.
func (r *emailCampaignRepository) ClaimValid(ctx context.Context, recipient service.EmailCampaignRecipient) (bool, error) {
	var valid bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM email_campaign_recipients WHERE campaign_id=$1 AND user_id=$2 AND status='sending' AND claim_version=$3 AND claimed_at > clock_timestamp()-($4::double precision * interval '1 second'))`, recipient.CampaignID, recipient.UserID, recipient.ClaimVersion, service.EmailCampaignClaimLease.Seconds()).Scan(&valid)
	return valid, err
}
func (r *emailCampaignRepository) Eligible(ctx context.Context, recipient service.EmailCampaignRecipient) (string, string, error) {
	var username, reason string
	e := r.db.QueryRowContext(ctx, `SELECT username,CASE WHEN deleted_at IS NOT NULL THEN '用户已删除' WHEN status<>'active' THEN '账号已停用' WHEN role<>'user' THEN '用户角色已变更' WHEN lower(btrim(email))<>$2 THEN '邮箱已变更' WHEN EXISTS(SELECT 1 FROM email_marketing_suppressions s WHERE s.email_norm=$2 OR s.user_id=u.id) THEN '已退订或在不接收名单' ELSE '' END FROM users u WHERE id=$1`, recipient.UserID, service.NormalizeCampaignEmail(recipient.Email)).Scan(&username, &reason)
	if errors.Is(e, sql.ErrNoRows) {
		return "", "用户已删除", nil
	}
	return username, reason, e
}
func (r *emailCampaignRepository) Finish(ctx context.Context, recipient service.EmailCampaignRecipient, outcome service.EmailCampaignOutcome) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	status, e := campaignLock(ctx, tx, recipient.CampaignID)
	if e != nil {
		return e
	}
	next := "failed"
	uncertain := false
	campaignFailure := false
	switch outcome.Kind {
	case "sent":
		next = "sent"
	case "skipped":
		next = "skipped"
	case "recipient":
		next = "failed"
		campaignFailure = outcome.CountFailure
	case "uncertain":
		uncertain = true
		campaignFailure = true
	case "campaign", "retry":
		next = "pending"
		campaignFailure = outcome.Kind == "campaign"
		if status == "canceled" {
			next = "skipped"
		}
	default:
		return fmt.Errorf("unknown email campaign outcome %q", outcome.Kind)
	}
	result, e := tx.ExecContext(ctx, `UPDATE email_campaign_recipients SET status=$4::text,outcome_uncertain=$5,last_error=$6,sent_at=CASE WHEN $4::text='sent' THEN now() ELSE sent_at END WHERE campaign_id=$1 AND user_id=$2 AND status='sending' AND claim_version=$3`, recipient.CampaignID, recipient.UserID, recipient.ClaimVersion, next, uncertain, outcome.Error)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return tx.Commit()
	}
	if outcome.Kind == "sent" {
		_, e = tx.ExecContext(ctx, `UPDATE email_campaigns SET consecutive_failures=0,last_error='',updated_at=now() WHERE id=$1`, recipient.CampaignID)
	} else if campaignFailure {
		_, e = campaignRecordFailure(ctx, tx, recipient.CampaignID, outcome.Error, outcome.Auth, 1)
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}

// The caller already holds the campaign row lock. Stale claims are uncertain failures too.
func campaignRecordFailure(ctx context.Context, tx *sql.Tx, id int64, message string, auth bool, count int64) (string, error) {
	var status string
	e := tx.QueryRowContext(ctx, `UPDATE email_campaigns SET consecutive_failures=consecutive_failures+$4,last_error=$2,updated_at=now(),status=CASE WHEN status IN ('sending','paused') AND ($3 OR consecutive_failures+$4>=5) THEN 'paused' ELSE status END,pause_reason=CASE WHEN status IN ('sending','paused') AND $3 THEN 'auth' WHEN status IN ('sending','paused') AND consecutive_failures+$4>=5 THEN 'failures' ELSE pause_reason END WHERE id=$1 RETURNING status`, id, message, auth, count).Scan(&status)
	return status, e
}
