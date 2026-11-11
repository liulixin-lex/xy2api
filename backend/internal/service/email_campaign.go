package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/yuin/goldmark"
	goldhtml "github.com/yuin/goldmark/renderer/html"
)

var (
	ErrEmailCampaignNotFound        = infraerrors.NotFound("EMAIL_CAMPAIGN_NOT_FOUND", "群发不存在")
	ErrEmailCampaignInvalidState    = infraerrors.Conflict("EMAIL_CAMPAIGN_INVALID_STATE", "当前状态不能执行此操作")
	ErrEmailCampaignActiveExists    = infraerrors.Conflict("EMAIL_CAMPAIGN_ACTIVE_EXISTS", "请先完成或取消正在进行的群发")
	ErrEmailCampaignInvalidInput    = infraerrors.BadRequest("EMAIL_CAMPAIGN_INVALID_INPUT", "邮件或圈人条件不合法")
	ErrEmailCampaignAudienceEmpty   = infraerrors.BadRequest("EMAIL_CAMPAIGN_AUDIENCE_EMPTY", "没有符合条件的收件人")
	ErrEmailCampaignSiteURLRequired = infraerrors.BadRequest("EMAIL_CAMPAIGN_SITE_URL_REQUIRED", "请配置 HTTPS 站点地址")
)

const EmailCampaignLimitsKey = "email_campaign_send_limits"

var campaignEmailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func NormalizeCampaignEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
func ValidCampaignEmail(email string) bool {
	return campaignEmailPattern.MatchString(email) && !strings.HasSuffix(strings.ToLower(email), ".invalid")
}

type EmailCampaignAudience struct {
	RegisteredFrom *string  `json:"registered_from"`
	RegisteredTo   *string  `json:"registered_to"`
	Timezone       string   `json:"timezone"`
	InactiveDays   *int     `json:"inactive_days"`
	Exclude        []string `json:"exclude,omitempty"`
}
type EmailCampaignSkipped struct {
	NoValidEmail int64 `json:"no_valid_email"`
	Suppressed   int64 `json:"suppressed"`
	Excluded     int64 `json:"excluded"`
	Duplicate    int64 `json:"duplicate"`
}
type EmailCampaignAudienceSnapshot struct {
	EmailCampaignAudience
	ExcludeUserIDs []int64              `json:"exclude_user_ids"`
	Skipped        EmailCampaignSkipped `json:"skipped"`
	From           *time.Time           `json:"-"`
	To             *time.Time           `json:"-"`
	Cutoff         *time.Time           `json:"-"`
}
type EmailCampaignAudienceQuery struct {
	EmailCampaignAudienceSnapshot
}
type EmailCampaignCounts struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Sending   int64 `json:"sending"`
	Sent      int64 `json:"sent"`
	Failed    int64 `json:"failed"`
	Skipped   int64 `json:"skipped"`
	Uncertain int64 `json:"uncertain"`
}
type EmailCampaignCreator struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}
type EmailCampaign struct {
	ID                  int64                         `json:"id"`
	Subject             string                        `json:"subject"`
	Status              string                        `json:"status"`
	PauseReason         string                        `json:"pause_reason"`
	LastError           string                        `json:"last_error"`
	ConsecutiveFailures int                           `json:"consecutive_failures"`
	WaitingForLimit     string                        `json:"waiting_for_limit"`
	Audience            EmailCampaignAudienceSnapshot `json:"audience"`
	Counts              EmailCampaignCounts           `json:"counts"`
	CreatedBy           *EmailCampaignCreator         `json:"created_by"`
	CreatedAt           time.Time                     `json:"created_at"`
	StartedAt           time.Time                     `json:"started_at"`
	FinishedAt          *time.Time                    `json:"finished_at"`
	UpdatedAt           time.Time                     `json:"updated_at"`
	BodyMarkdown        string                        `json:"-"`
}
type EmailCampaignDetail struct {
	*EmailCampaign
	BodyMarkdown string `json:"body_markdown"`
	PreviewHTML  string `json:"preview_html"`
}
type EmailCampaignRecipient struct {
	UserID       int64      `json:"user_id"`
	Email        string     `json:"email"`
	Status       string     `json:"status"`
	LastError    string     `json:"last_error"`
	Uncertain    bool       `json:"uncertain"`
	ClaimedAt    *time.Time `json:"claimed_at"`
	SentAt       *time.Time `json:"sent_at"`
	ClaimVersion int64      `json:"-"`
	CampaignID   int64      `json:"-"`
}
type EmailCampaignAudienceUser struct {
	UserID         int64      `json:"user_id"`
	Email          string     `json:"email"`
	Username       string     `json:"username"`
	CreatedAt      time.Time  `json:"created_at"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}
type EmailCampaignPage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int64 `json:"pages"`
}

func CampaignPage[T any](items []T, total int64, page, size int) EmailCampaignPage[T] {
	if items == nil {
		items = []T{}
	}
	return EmailCampaignPage[T]{items, total, page, size, (total + int64(size) - 1) / int64(size)}
}
func CampaignPagination(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	if page > 1000000 {
		page = 1000000
	}
	return page, size
}

type EmailCampaignPreview struct {
	EmailCampaignPage[EmailCampaignAudienceUser]
	Matched         int64                `json:"matched"`
	Skipped         EmailCampaignSkipped `json:"skipped"`
	ExcludeResolved int                  `json:"exclude_resolved"`
	ExcludeNotFound []string             `json:"exclude_not_found"`
}
type EmailCampaignSuppression struct {
	ID              int64                 `json:"id"`
	Email           string                `json:"email"`
	UserID          *int64                `json:"user_id"`
	Username        string                `json:"username"`
	Source          string                `json:"source"`
	Note            string                `json:"note"`
	CampaignID      *int64                `json:"campaign_id"`
	CampaignSubject string                `json:"campaign_subject"`
	CreatedBy       *EmailCampaignCreator `json:"created_by"`
	CreatedAt       time.Time             `json:"created_at"`
}
type EmailCampaignSuppressionResult struct {
	Added    int64    `json:"added"`
	Already  int64    `json:"already"`
	NotFound []string `json:"not_found"`
}
type EmailCampaignLimits struct {
	PerMinute int `json:"per_minute"`
	PerDay    int `json:"per_day"`
}

func (l EmailCampaignLimits) Valid() bool {
	return l.PerMinute >= 1 && l.PerMinute <= 120 && l.PerDay >= 1 && l.PerDay <= 100000
}
func DefaultEmailCampaignLimits() EmailCampaignLimits { return EmailCampaignLimits{20, 500} }

type EmailCampaignSettings struct {
	EmailCampaignLimits
	SMTPConfigured bool   `json:"smtp_configured"`
	SiteURL        string `json:"site_url"`
	SiteURLOK      bool   `json:"site_url_ok"`
	SentLastMinute int64  `json:"sent_last_minute"`
	SentLast24h    int64  `json:"sent_last_24h"`
}
type EmailCampaignContent struct {
	Subject      string `json:"subject"`
	BodyMarkdown string `json:"body_markdown"`
}
type EmailCampaignCreate struct {
	EmailCampaignContent
	Audience EmailCampaignAudience `json:"audience"`
}
type EmailCampaignRendered struct {
	Subject        string `json:"subject"`
	HTML           string `json:"html"`
	Text           string `json:"text"`
	UnsubscribeURL string `json:"-"`
}
type EmailCampaignClaim struct {
	Campaign  *EmailCampaign
	Recipient EmailCampaignRecipient
}
type EmailCampaignOutcome struct {
	Kind  string
	Error string
	Auth  bool
}

type EmailCampaignRepository interface {
	Secret(context.Context) (string, error)
	ReadSecret(context.Context) (string, error)
	Preview(context.Context, EmailCampaignAudienceQuery, int, int) (*EmailCampaignPreview, error)
	Create(context.Context, EmailCampaignCreate, EmailCampaignAudienceQuery, int64) (*EmailCampaign, error)
	Get(context.Context, int64) (*EmailCampaign, error)
	List(context.Context, string, int, int) (EmailCampaignPage[EmailCampaign], error)
	Recipients(context.Context, int64, string, string, int, int) (EmailCampaignPage[EmailCampaignRecipient], error)
	Action(context.Context, int64, string) (*EmailCampaign, int64, error)
	Suppressions(context.Context, string, int, int) (EmailCampaignPage[EmailCampaignSuppression], error)
	AddSuppressions(context.Context, []string, string, int64) (*EmailCampaignSuppressionResult, error)
	RemoveSuppression(context.Context, int64) (bool, error)
	Unsubscribe(context.Context, string, int64) error
	SendStats(context.Context) (int64, int64, error)
	ReserveTestSend(context.Context) (bool, error)
	Claim(context.Context) (*EmailCampaignClaim, error)
	Eligible(context.Context, EmailCampaignRecipient) (string, string, error)
	Finish(context.Context, EmailCampaignRecipient, EmailCampaignOutcome) error
}
type EmailCampaignService struct {
	repo         EmailCampaignRepository
	settings     SettingRepository
	email        *EmailService
	notification *NotificationEmailService
	users        UserRepository
}

func NewEmailCampaignService(repo EmailCampaignRepository, settings SettingRepository, email *EmailService, notification *NotificationEmailService, users UserRepository) *EmailCampaignService {
	return &EmailCampaignService{repo, settings, email, notification, users}
}

func ValidateEmailCampaignAudience(a EmailCampaignAudience, now time.Time) (EmailCampaignAudienceQuery, error) {
	q := EmailCampaignAudienceQuery{EmailCampaignAudienceSnapshot: EmailCampaignAudienceSnapshot{EmailCampaignAudience: a, ExcludeUserIDs: []int64{}}}
	if a.Timezone == "" || len(a.Exclude) > 1000 {
		return q, ErrEmailCampaignInvalidInput
	}
	loc, err := time.LoadLocation(a.Timezone)
	if err != nil {
		return q, ErrEmailCampaignInvalidInput
	}
	for _, v := range a.Exclude {
		if len(v) > 320 {
			return q, ErrEmailCampaignInvalidInput
		}
	}
	if a.RegisteredFrom != nil {
		v, e := time.ParseInLocation("2006-01-02", *a.RegisteredFrom, loc)
		if e != nil {
			return q, ErrEmailCampaignInvalidInput
		}
		q.From = &v
	}
	if a.RegisteredTo != nil {
		v, e := time.ParseInLocation("2006-01-02", *a.RegisteredTo, loc)
		if e != nil {
			return q, ErrEmailCampaignInvalidInput
		}
		v = v.AddDate(0, 0, 1)
		q.To = &v
	}
	if q.From != nil && q.To != nil && !q.From.Before(*q.To) {
		return q, ErrEmailCampaignInvalidInput
	}
	if a.InactiveDays != nil {
		if *a.InactiveDays < 1 || *a.InactiveDays > 3650 {
			return q, ErrEmailCampaignInvalidInput
		}
		cut := now.Add(-time.Duration(*a.InactiveDays) * 24 * time.Hour)
		q.Cutoff = &cut
	}
	return q, nil
}
func validateCampaignContent(c EmailCampaignContent) error {
	if !utf8.ValidString(c.Subject) || !utf8.ValidString(c.BodyMarkdown) || strings.TrimSpace(sanitizeEmailHeader(c.Subject)) == "" || utf8.RuneCountInString(c.Subject) > 200 || len(c.BodyMarkdown) > 100000 || strings.TrimSpace(c.BodyMarkdown) == "" {
		return ErrEmailCampaignInvalidInput
	}
	return nil
}
func campaignHTTPS(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func (s *EmailCampaignService) Limits(ctx context.Context) (EmailCampaignLimits, error) {
	limits := DefaultEmailCampaignLimits()
	raw, e := s.settings.GetValue(ctx, EmailCampaignLimitsKey)
	if errors.Is(e, ErrSettingNotFound) {
		return limits, nil
	}
	if e != nil {
		return limits, e
	}
	if e = json.Unmarshal([]byte(raw), &limits); e != nil || !limits.Valid() {
		return limits, ErrEmailCampaignInvalidInput
	}
	return limits, nil
}
func (s *EmailCampaignService) Settings(ctx context.Context) (*EmailCampaignSettings, error) {
	l, e := s.Limits(ctx)
	if e != nil {
		return nil, e
	}
	minute, day, e := s.repo.SendStats(ctx)
	if e != nil {
		return nil, e
	}
	_, smtpErr := s.email.GetSMTPConfig(ctx)
	if smtpErr != nil && !errors.Is(smtpErr, ErrEmailNotConfigured) {
		return nil, smtpErr
	}
	base := s.notification.baseURL(ctx)
	return &EmailCampaignSettings{l, smtpErr == nil, base, campaignHTTPS(base), minute, day}, nil
}
func (s *EmailCampaignService) UpdateSettings(ctx context.Context, l EmailCampaignLimits) (*EmailCampaignSettings, error) {
	if !l.Valid() {
		return nil, ErrEmailCampaignInvalidInput
	}
	b, _ := json.Marshal(l)
	if e := s.settings.Set(ctx, EmailCampaignLimitsKey, string(b)); e != nil {
		return nil, e
	}
	return s.Settings(ctx)
}
func (s *EmailCampaignService) Preview(ctx context.Context, a EmailCampaignAudience, page, size int) (*EmailCampaignPreview, error) {
	q, e := ValidateEmailCampaignAudience(a, time.Now())
	if e != nil {
		return nil, e
	}
	page, size = CampaignPagination(page, size)
	return s.repo.Preview(ctx, q, page, size)
}
func (s *EmailCampaignService) Create(ctx context.Context, in EmailCampaignCreate, uid int64) (*EmailCampaign, error) {
	if e := validateCampaignContent(in.EmailCampaignContent); e != nil {
		return nil, e
	}
	q, e := ValidateEmailCampaignAudience(in.Audience, time.Now())
	if e != nil {
		return nil, e
	}
	if !campaignHTTPS(s.notification.baseURL(ctx)) {
		return nil, ErrEmailCampaignSiteURLRequired
	}
	if _, e = s.email.GetSMTPConfig(ctx); e != nil {
		return nil, e
	}
	if _, e = s.repo.Secret(ctx); e != nil {
		return nil, e
	}
	in.Subject = sanitizeEmailHeader(in.Subject)
	return s.repo.Create(ctx, in, q, uid)
}
func (s *EmailCampaignService) List(ctx context.Context, status string, page, size int) (EmailCampaignPage[EmailCampaign], error) {
	if status != "" && status != "sending" && status != "paused" && status != "completed" && status != "canceled" {
		return EmailCampaignPage[EmailCampaign]{}, ErrEmailCampaignInvalidInput
	}
	page, size = CampaignPagination(page, size)
	return s.repo.List(ctx, status, page, size)
}
func (s *EmailCampaignService) Detail(ctx context.Context, id, admin int64) (*EmailCampaignDetail, error) {
	c, e := s.repo.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	r, e := s.RenderForAdmin(ctx, EmailCampaignContent{c.Subject, c.BodyMarkdown}, admin, id)
	if e != nil {
		return nil, e
	}
	return &EmailCampaignDetail{c, c.BodyMarkdown, r.HTML}, nil
}
func (s *EmailCampaignService) Recipients(ctx context.Context, id int64, status, search string, page, size int) (EmailCampaignPage[EmailCampaignRecipient], error) {
	if status != "" && status != "pending" && status != "sending" && status != "sent" && status != "failed" && status != "skipped" {
		return EmailCampaignPage[EmailCampaignRecipient]{}, ErrEmailCampaignInvalidInput
	}
	page, size = CampaignPagination(page, size)
	return s.repo.Recipients(ctx, id, status, search, page, size)
}
func (s *EmailCampaignService) Action(ctx context.Context, id int64, action string) (*EmailCampaign, int64, error) {
	return s.repo.Action(ctx, id, action)
}
func (s *EmailCampaignService) Suppressions(ctx context.Context, search string, page, size int) (EmailCampaignPage[EmailCampaignSuppression], error) {
	page, size = CampaignPagination(page, size)
	return s.repo.Suppressions(ctx, search, page, size)
}
func (s *EmailCampaignService) AddSuppressions(ctx context.Context, entries []string, note string, admin int64) (*EmailCampaignSuppressionResult, error) {
	if len(entries) < 1 || len(entries) > 500 || utf8.RuneCountInString(note) > 200 {
		return nil, ErrEmailCampaignInvalidInput
	}
	for _, v := range entries {
		if len(v) > 320 {
			return nil, ErrEmailCampaignInvalidInput
		}
	}
	return s.repo.AddSuppressions(ctx, entries, note, admin)
}
func (s *EmailCampaignService) RemoveSuppression(ctx context.Context, id int64) (bool, error) {
	return s.repo.RemoveSuppression(ctx, id)
}

type EmailCampaignToken struct {
	Email      string `json:"e"`
	CampaignID int64  `json:"c"`
}

func campaignToken(secret, email string, id int64) string {
	b, _ := json.Marshal(EmailCampaignToken{NormalizeCampaignEmail(email), id})
	payload := base64.RawURLEncoding.EncodeToString(b)
	return payload + "." + signNotificationEmailToken(secret, "email_campaign:"+payload)
}
func parseCampaignToken(secret, token string) (EmailCampaignToken, error) {
	var c EmailCampaignToken
	parts := strings.Split(token, ".")
	if len(token) > 2048 || len(parts) != 2 || !hmac.Equal([]byte(signNotificationEmailToken(secret, "email_campaign:"+parts[0])), []byte(parts[1])) {
		return c, ErrEmailCampaignInvalidInput
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return c, ErrEmailCampaignInvalidInput
	}
	if e = json.Unmarshal(b, &c); e != nil || !ValidCampaignEmail(c.Email) || c.Email != NormalizeCampaignEmail(c.Email) || c.CampaignID < 0 {
		return c, ErrEmailCampaignInvalidInput
	}
	return c, nil
}
func (s *EmailCampaignService) ParseUnsubscribe(ctx context.Context, token string) (EmailCampaignToken, error) {
	secret, e := s.repo.ReadSecret(ctx)
	if e != nil {
		return EmailCampaignToken{}, e
	}
	return parseCampaignToken(secret, token)
}
func (s *EmailCampaignService) Unsubscribe(ctx context.Context, token string) (EmailCampaignToken, error) {
	c, e := s.ParseUnsubscribe(ctx, token)
	if e != nil {
		return c, e
	}
	return c, s.repo.Unsubscribe(ctx, c.Email, c.CampaignID)
}
func (s *EmailCampaignService) RenderForAdmin(ctx context.Context, c EmailCampaignContent, uid, campaign int64) (*EmailCampaignRendered, error) {
	u, e := s.users.GetByID(ctx, uid)
	if e != nil {
		return nil, e
	}
	return s.Render(ctx, c, u.Email, u.Username, campaign)
}
func (s *EmailCampaignService) Render(ctx context.Context, c EmailCampaignContent, email, username string, campaign int64) (*EmailCampaignRendered, error) {
	if e := validateCampaignContent(c); e != nil {
		return nil, e
	}
	base := s.notification.baseURL(ctx)
	if !campaignHTTPS(base) {
		return nil, ErrEmailCampaignSiteURLRequired
	}
	secret, e := s.repo.Secret(ctx)
	if e != nil {
		return nil, e
	}
	link := base + "/api/v1/email-campaigns/unsubscribe?token=" + url.QueryEscape(campaignToken(secret, email, campaign))
	if !campaignHTTPS(strings.SplitN(link, "?", 2)[0]) {
		return nil, ErrEmailCampaignSiteURLRequired
	}
	if username == "" {
		username = emailRecipientName(email)
	}
	name := s.notification.siteName(ctx)
	replace := func(v string, escape bool) string {
		u, n := username, name
		if escape {
			u, n = html.EscapeString(u), html.EscapeString(n)
		}
		return strings.NewReplacer("{{username}}", u, "{{site_name}}", n).Replace(v)
	}
	var body bytes.Buffer
	md := goldmark.New(goldmark.WithRendererOptions(goldhtml.WithUnsafe()))
	if e = md.Convert([]byte(c.BodyMarkdown), &body); e != nil {
		return nil, e
	}
	subject := sanitizeEmailHeader(replace(c.Subject, false))
	footer := `<hr><p>不想接收营销邮件？<a href="` + html.EscapeString(link) + `">退订 / Unsubscribe</a><br>To stop receiving marketing emails, unsubscribe here. 不影响验证码及系统通知。</p>`
	card := notificationEmailCard("#4f46e5", html.EscapeString(subject), replace(body.String(), true)+footer)
	// Replace the card's own site placeholder separately; replacement values are never expanded again.
	card = strings.ReplaceAll(card, "This email was sent by {{site_name}}.", "This email was sent by "+html.EscapeString(name)+".")
	return &EmailCampaignRendered{subject, card, replace(c.BodyMarkdown, false) + "\n\n退订营销邮件 / Unsubscribe from marketing emails: " + link + "\n不影响验证码及系统通知。\n", link}, nil
}
func (s *EmailCampaignService) TestSend(ctx context.Context, c EmailCampaignContent, to string) (string, error) {
	addr, e := parseSMTPAddress(to, "recipient")
	if e != nil || !ValidCampaignEmail(addr.Address) {
		return "", ErrEmailCampaignInvalidInput
	}
	r, e := s.Render(ctx, c, addr.Address, "", 0)
	if e != nil {
		return "", e
	}
	cfg, e := s.email.GetSMTPConfig(ctx)
	if e != nil {
		return "", e
	}
	msg, e := buildCampaignSMTPMessage(cfg, addr.Address, r)
	if e != nil {
		return "", e
	}
	ok, e := s.repo.ReserveTestSend(ctx)
	if e != nil {
		return "", e
	}
	if !ok {
		return "", infraerrors.Conflict("EMAIL_CAMPAIGN_INVALID_STATE", "营销邮件已达发送限速，请稍后重试")
	}
	if e = s.email.sendSMTPMessage(ctx, cfg, msg); e != nil {
		return "", e
	}
	return addr.Address, nil
}
