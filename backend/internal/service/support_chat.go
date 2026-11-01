package service

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const (
	SupportChatRoleUser            = "user"
	SupportChatRoleAdmin           = "admin"
	SupportChatRoleSystem          = "system"
	SupportChatRoleBot             = "bot"
	SupportChatStatusOpen          = "open"
	SupportChatStatusResolved      = "resolved"
	SupportChatBodyMaxRunes        = 5000
	SupportChatMessagesPerMinute   = 20
	SupportChatMessagesPerDay      = 300
	SupportChatImagesPerDay        = 30
	SupportChatImageBytesPerDay    = 20 * 1024 * 1024
	SupportChatImageMaxBytes       = 1572864
	SupportChatRequestMaxBytes     = 2097152
	SupportChatImageMaxSide        = 4096
	SupportChatImageMaxPixels      = 16777216
	SupportChatBlockReasonMaxRunes = 200
	supportChatDefaultPageSize     = 30
	supportChatMaxPageSize         = 50
)

var (
	ErrSupportChatDisabled        = infraerrors.Forbidden("SUPPORT_CHAT_DISABLED", "support chat is disabled")
	ErrSupportChatNotFound        = infraerrors.NotFound("SUPPORT_CHAT_NOT_FOUND", "support chat not found")
	ErrSupportChatMessageNotFound = infraerrors.NotFound("SUPPORT_CHAT_MESSAGE_NOT_FOUND", "support chat message not found")
	ErrSupportChatBodyRequired    = infraerrors.BadRequest("SUPPORT_CHAT_BODY_REQUIRED", "message body or image is required")
	ErrSupportChatBodyTooLong     = infraerrors.BadRequest("SUPPORT_CHAT_BODY_TOO_LONG", "message body is too long")
	ErrSupportChatClientIDInvalid = infraerrors.BadRequest("SUPPORT_CHAT_CLIENT_ID_INVALID", "client_msg_id is invalid")
	ErrSupportChatTooFast         = infraerrors.TooManyRequests("SUPPORT_CHAT_TOO_FAST", "messages are sent too quickly")
	ErrSupportChatDailyLimit      = infraerrors.TooManyRequests("SUPPORT_CHAT_DAILY_LIMIT", "daily message limit exceeded")
	ErrSupportChatImageDailyLimit = infraerrors.TooManyRequests("SUPPORT_CHAT_IMAGE_DAILY_LIMIT", "daily image limit exceeded")
	ErrSupportChatImageInvalid    = infraerrors.BadRequest("SUPPORT_CHAT_IMAGE_INVALID", "invalid image")
	ErrSupportChatImageTooLarge   = infraerrors.New(httpStatusRequestEntityTooLarge, "SUPPORT_CHAT_IMAGE_TOO_LARGE", "image is too large")
	ErrSupportChatImageGone       = infraerrors.New(httpStatusGone, "SUPPORT_CHAT_IMAGE_GONE", "image is no longer available")
	ErrSupportChatBlocked         = infraerrors.Forbidden("SUPPORT_CHAT_BLOCKED", "support chat is blocked")
	ErrSupportChatChanged         = infraerrors.Conflict("SUPPORT_CHAT_CHANGED", "support chat changed")
	ErrSupportChatStatusInvalid   = infraerrors.BadRequest("SUPPORT_CHAT_STATUS_INVALID", "invalid support chat status")
)

// Kept local so the service package does not depend on net/http just for codes.
const (
	httpStatusRequestEntityTooLarge = 413
	httpStatusGone                  = 410
)

type SupportChatImage struct {
	ID          int64  `json:"id"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ByteSize    int    `json:"byte_size"`
	Purged      bool   `json:"purged"`
}
type SupportChatMessage struct {
	ID          int64             `json:"id"`
	AuthorRole  string            `json:"author_role"`
	Body        string            `json:"body"`
	Image       *SupportChatImage `json:"image"`
	Deleted     bool              `json:"deleted"`
	ClientMsgID string            `json:"client_msg_id,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	AuthorName  string            `json:"author_name,omitempty"`
	// ChatID is set on send results only; the handler returns it beside the message.
	ChatID int64 `json:"-"`
}
type SupportChat struct {
	ID                 int64              `json:"id"`
	UserID             int64              `json:"user_id,omitempty"`
	Status             string             `json:"status"`
	WaitingSince       *time.Time         `json:"waiting_since"`
	LastMessageAt      time.Time          `json:"last_message_at"`
	LastMessageRole    string             `json:"last_message_role"`
	LastMessageID      int64              `json:"last_message_id"`
	LastUserMessageID  int64              `json:"last_user_message_id,omitempty"`
	LastStaffMessageID int64              `json:"last_staff_message_id,omitempty"`
	ReadMessageID      int64              `json:"read_message_id"`
	PeerReadMessageID  int64              `json:"peer_read_message_id"`
	ContentVersion     int64              `json:"content_version"`
	MessageCount       int                `json:"message_count"`
	Blocked            bool               `json:"blocked"`
	BlockedReason      string             `json:"blocked_reason,omitempty"`
	ResolvedAt         *time.Time         `json:"resolved_at"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	Revision           int64              `json:"revision"`
	Unread             bool               `json:"unread,omitempty"`
	Preview            string             `json:"preview,omitempty"`
	PreviewKind        string             `json:"preview_kind,omitempty"`
	User               *SupportTicketUser `json:"user,omitempty"`
	UserReadMessageID  int64              `json:"-"`
	StaffReadMessageID int64              `json:"-"`
}
type SupportChatThreadPage struct {
	// ChatID is 0 when the user has no chat; a different id means the chat was replaced.
	ChatID            int64                `json:"chat_id"`
	Items             []SupportChatMessage `json:"items"`
	HasMore           bool                 `json:"has_more"`
	Status            string               `json:"status"`
	Blocked           bool                 `json:"blocked"`
	ReadMessageID     int64                `json:"read_message_id"`
	PeerReadMessageID int64                `json:"peer_read_message_id"`
	ContentVersion    int64                `json:"content_version"`
	UnreadCount       int64                `json:"unread_count"`
}
type SupportChatSummary struct {
	ChatWaiting         int64 `json:"chat_waiting"`
	TicketPending       int64 `json:"ticket_pending"`
	LatestUserMessageID int64 `json:"latest_user_message_id"`
	Version             int64 `json:"version"`
}
type SupportChatUserSummary struct {
	Chat              *SupportChatUserChat `json:"chat"`
	UnreadCount       int64                `json:"unread_count"`
	TicketUnreadCount int64                `json:"ticket_unread_count"`
	Welcome           string               `json:"welcome"`
}
type SupportChatUserChat struct {
	ID                int64  `json:"id"`
	Status            string `json:"status"`
	Blocked           bool   `json:"blocked"`
	Waiting           bool   `json:"waiting"`
	LastMessageID     int64  `json:"last_message_id"`
	ReadMessageID     int64  `json:"read_message_id"`
	PeerReadMessageID int64  `json:"peer_read_message_id"`
	ContentVersion    int64  `json:"content_version"`
}
type SupportChatSendInput struct {
	ChatID       int64
	UserID       int64
	AuthorRole   string
	AuthorUserID int64
	Body         string
	ClientMsgID  string
	Image        *SupportChatImageInput
}
type SupportChatImageInput struct {
	Data        []byte
	ContentType string
	Width       int
	Height      int
	AuthorRole  string
}
type SupportChatListFilter struct {
	Filter, Keyword string
	Page, PageSize  int
}
type SupportChatStatusInput struct {
	ChatID            int64
	Status            string
	LastSeenMessageID int64
}
type SupportChatBlockInput struct {
	ChatID  int64
	Blocked bool
	Reason  string
}
type SupportChatMessagePageInput struct {
	ChatID, OwnerID, AfterID, BeforeID int64
	Limit                              int
	Admin                              bool
}
type SupportChatImageResult struct {
	Image SupportChatImage
	Data  []byte
}

type SupportChatRepository interface {
	GetUserChat(context.Context, int64) (*SupportChat, error)
	GetChat(context.Context, int64, bool) (*SupportChat, error)
	ListChats(context.Context, SupportChatListFilter) ([]SupportChat, int64, error)
	ListMessages(context.Context, SupportChatMessagePageInput) ([]SupportChatMessage, bool, *SupportChat, error)
	SendMessage(context.Context, SupportChatSendInput) (*SupportChatMessage, bool, error)
	MarkRead(context.Context, int64, bool, int64) (int64, error)
	SetStatus(context.Context, SupportChatStatusInput) error
	SetBlocked(context.Context, SupportChatBlockInput) error
	DeleteMessage(context.Context, int64, int64) (*SupportChatMessage, error)
	DeleteChat(context.Context, int64) error
	GetImage(context.Context, int64, int64, bool) (*SupportChatImageResult, error)
	Summary(context.Context) (SupportChatSummary, error)
	UnreadCount(context.Context, int64, int64) (int64, error)
	PurgeImages(context.Context, int, time.Time) (int64, int64, error)
}

type supportChatSettings interface {
	GetSupportChatRuntime(context.Context) (SupportChatRuntime, error)
}
type SupportChatService struct {
	repo     SupportChatRepository
	settings supportChatSettings
	tickets  *SupportTicketService
	cacheMu  sync.Mutex
	cached   SupportChatRuntime
	cacheAt  time.Time
}

func NewSupportChatService(repo SupportChatRepository, settings *SettingService, tickets *SupportTicketService) *SupportChatService {
	return NewSupportChatServiceWithSettings(repo, settings, tickets)
}

func NewSupportChatServiceWithSettings(repo SupportChatRepository, settings supportChatSettings, tickets *SupportTicketService) *SupportChatService {
	return &SupportChatService{repo: repo, settings: settings, tickets: tickets}
}

// mapChatViewer applies the cursor orientation required by the API viewer.
func mapChatViewer(chat *SupportChat, admin bool) *SupportChat {
	if chat == nil {
		return nil
	}
	chat.ReadMessageID = chat.UserReadMessageID
	chat.PeerReadMessageID = chat.StaffReadMessageID
	if admin {
		chat.ReadMessageID = chat.StaffReadMessageID
		chat.PeerReadMessageID = chat.UserReadMessageID
	}
	return chat
}
func (s *SupportChatService) runtime(ctx context.Context, cached bool) (SupportChatRuntime, error) {
	if cached {
		s.cacheMu.Lock()
		c, at := s.cached, s.cacheAt
		s.cacheMu.Unlock()
		if time.Since(at) < 3*time.Second && !at.IsZero() {
			return c, nil
		}
	}
	r, err := s.settings.GetSupportChatRuntime(ctx)
	if err != nil {
		return SupportChatRuntime{}, err
	}
	if cached {
		s.cacheMu.Lock()
		s.cached = r
		s.cacheAt = time.Now()
		s.cacheMu.Unlock()
	}
	return r, nil
}
func (s *SupportChatService) enabled(ctx context.Context, cached bool) error {
	r, err := s.runtime(ctx, cached)
	if err != nil || !r.Enabled {
		return ErrSupportChatDisabled
	}
	return nil
}
func (s *SupportChatService) UserSummary(ctx context.Context, uid int64) (*SupportChatUserSummary, error) {
	r, err := s.runtime(ctx, true)
	if err != nil || !r.Enabled {
		return nil, ErrSupportChatDisabled
	}
	c, err := s.repo.GetUserChat(ctx, uid)
	if err != nil && !errors.Is(err, ErrSupportChatNotFound) {
		return nil, err
	}
	out := &SupportChatUserSummary{Welcome: r.Config.ChatWelcome}
	if c != nil {
		mapChatViewer(c, false)
		out.Chat = &SupportChatUserChat{ID: c.ID, Status: c.Status, Blocked: c.Blocked, Waiting: c.Status == SupportChatStatusOpen && c.WaitingSince != nil, LastMessageID: c.LastMessageID, ReadMessageID: c.ReadMessageID, PeerReadMessageID: c.PeerReadMessageID, ContentVersion: c.ContentVersion}
		out.UnreadCount, err = s.unread(ctx, c.ID, uid)
		if err != nil {
			return nil, err
		}
	}
	if s.tickets != nil {
		out.TicketUnreadCount, _ = s.tickets.repo.CountUnread(ctx, uid)
	}
	return out, nil
}

// ReadAsUser resolves and advances the caller's own chat cursor.
func (s *SupportChatService) ReadAsUser(ctx context.Context, userID, lastMessageID int64) (int64, error) {
	if err := s.enabled(ctx, false); err != nil {
		return 0, err
	}
	chat, err := s.repo.GetUserChat(ctx, userID)
	if errors.Is(err, ErrSupportChatNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return s.repo.MarkRead(ctx, chat.ID, false, lastMessageID)
}
func (s *SupportChatService) unread(ctx context.Context, chatID, uid int64) (int64, error) {
	return s.repo.UnreadCount(ctx, chatID, uid)
}
func (s *SupportChatService) Messages(ctx context.Context, in SupportChatMessagePageInput) (*SupportChatThreadPage, error) {
	if err := s.enabled(ctx, true); err != nil {
		return nil, err
	}
	if in.OwnerID > 0 && in.ChatID == 0 {
		c, err := s.repo.GetUserChat(ctx, in.OwnerID)
		if errors.Is(err, ErrSupportChatNotFound) {
			return &SupportChatThreadPage{Items: []SupportChatMessage{}}, nil
		}
		if err != nil {
			return nil, err
		}
		in.ChatID = c.ID
	}
	if in.Limit < 1 {
		in.Limit = supportChatDefaultPageSize
	}
	if in.Limit > supportChatMaxPageSize {
		in.Limit = supportChatMaxPageSize
	}
	items, more, c, err := s.repo.ListMessages(ctx, in)
	if err != nil {
		return nil, err
	}
	p := &SupportChatThreadPage{Items: items, HasMore: more}
	if c != nil {
		p.ChatID = in.ChatID
		p.Status = c.Status
		p.Blocked = c.Blocked
		mapChatViewer(c, in.Admin)
		p.ReadMessageID = c.ReadMessageID
		p.PeerReadMessageID = c.PeerReadMessageID
		p.ContentVersion = c.ContentVersion
		if !in.Admin && in.OwnerID > 0 {
			p.UnreadCount, _ = s.repo.UnreadCount(ctx, in.ChatID, in.OwnerID)
		}
	}
	return p, nil
}
func (s *SupportChatService) Send(ctx context.Context, in SupportChatSendInput) (*SupportChatMessage, bool, error) {
	if err := s.enabled(ctx, false); err != nil {
		return nil, false, err
	}
	in.Body = cleanSupportTicketText(in.Body)
	if utf8.RuneCountInString(in.Body) > SupportChatBodyMaxRunes {
		return nil, false, ErrSupportChatBodyTooLong
	}
	if in.Body == "" && in.Image == nil {
		return nil, false, ErrSupportChatBodyRequired
	}
	if !clientIDRE.MatchString(in.ClientMsgID) {
		return nil, false, ErrSupportChatClientIDInvalid
	}
	if in.Image != nil {
		v, err := ValidateSupportChatImage(in.Image.Data)
		if err != nil {
			return nil, false, err
		}
		in.Image.Data = v.Data
		in.Image.ContentType = v.ContentType
		in.Image.Width = v.Width
		in.Image.Height = v.Height
		in.Image.AuthorRole = in.AuthorRole
	}
	m, dup, err := s.repo.SendMessage(ctx, in)
	if err != nil {
		return nil, false, chatLimitError(err)
	}
	return m, dup, nil
}
func (s *SupportChatService) Read(ctx context.Context, chatID int64, admin bool, n int64) (int64, error) {
	if err := s.enabled(ctx, false); err != nil {
		return 0, err
	}
	return s.repo.MarkRead(ctx, chatID, admin, n)
}
func (s *SupportChatService) Image(ctx context.Context, id, owner int64, admin bool) (*SupportChatImageResult, error) {
	if err := s.enabled(ctx, true); err != nil {
		return nil, err
	}
	return s.repo.GetImage(ctx, id, owner, admin)
}
func (s *SupportChatService) AdminSummary(ctx context.Context) (SupportChatSummary, error) {
	if err := s.enabled(ctx, true); err != nil {
		return SupportChatSummary{}, err
	}
	out, err := s.repo.Summary(ctx)
	if err == nil && s.tickets != nil {
		out.TicketPending, _ = s.tickets.repo.CountPending(ctx)
	}
	return out, err
}
func (s *SupportChatService) AdminList(ctx context.Context, in SupportChatListFilter) ([]SupportChat, int64, error) {
	if err := s.enabled(ctx, true); err != nil {
		return nil, 0, err
	}
	if in.Page < 1 {
		in.Page = 1
	}
	if in.PageSize < 1 {
		in.PageSize = supportChatDefaultPageSize
	}
	if in.PageSize > supportChatMaxPageSize {
		in.PageSize = supportChatMaxPageSize
	}
	items, total, err := s.repo.ListChats(ctx, in)
	if err != nil {
		return nil, 0, err
	}
	for index := range items {
		mapChatViewer(&items[index], true)
	}
	return items, total, nil
}
func (s *SupportChatService) AdminGet(ctx context.Context, id int64) (*SupportChat, error) {
	if err := s.enabled(ctx, true); err != nil {
		return nil, err
	}
	c, err := s.repo.GetChat(ctx, id, true)
	if err != nil {
		return nil, err
	}
	return mapChatViewer(c, true), nil
}
func (s *SupportChatService) AdminStatus(ctx context.Context, in SupportChatStatusInput) (*SupportChat, error) {
	if err := s.enabled(ctx, false); err != nil {
		return nil, err
	}
	if in.Status != "open" && in.Status != "resolved" {
		return nil, ErrSupportChatStatusInvalid
	}
	if err := s.repo.SetStatus(ctx, in); err != nil {
		return nil, err
	}
	c, err := s.repo.GetChat(ctx, in.ChatID, true)
	if err != nil {
		return nil, err
	}
	return mapChatViewer(c, true), nil
}
func (s *SupportChatService) AdminBlock(ctx context.Context, in SupportChatBlockInput) (*SupportChat, error) {
	if err := s.enabled(ctx, false); err != nil {
		return nil, err
	}
	in.Reason = cleanSupportTicketText(in.Reason)
	if utf8.RuneCountInString(in.Reason) > SupportChatBlockReasonMaxRunes {
		in.Reason = string([]rune(in.Reason)[:SupportChatBlockReasonMaxRunes])
	}
	if err := s.repo.SetBlocked(ctx, in); err != nil {
		return nil, err
	}
	c, err := s.repo.GetChat(ctx, in.ChatID, true)
	if err != nil {
		return nil, err
	}
	return mapChatViewer(c, true), nil
}
func (s *SupportChatService) AdminDeleteMessage(ctx context.Context, cid, mid int64) (*SupportChatMessage, error) {
	if err := s.enabled(ctx, false); err != nil {
		return nil, err
	}
	return s.repo.DeleteMessage(ctx, cid, mid)
}
func (s *SupportChatService) AdminDeleteChat(ctx context.Context, id int64) error {
	if err := s.enabled(ctx, false); err != nil {
		return err
	}
	return s.repo.DeleteChat(ctx, id)
}

var clientIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func chatLimitError(err error) error {
	switch {
	case errors.Is(err, ErrSupportChatTooFast):
		return ErrSupportChatTooFast.WithMetadata(map[string]string{"max": strconv.Itoa(SupportChatMessagesPerMinute)})
	case errors.Is(err, ErrSupportChatDailyLimit):
		return ErrSupportChatDailyLimit.WithMetadata(map[string]string{"max": strconv.Itoa(SupportChatMessagesPerDay)})
	case errors.Is(err, ErrSupportChatImageDailyLimit):
		return ErrSupportChatImageDailyLimit.WithMetadata(map[string]string{"max_count": strconv.Itoa(SupportChatImagesPerDay), "max_bytes": strconv.Itoa(SupportChatImageBytesPerDay)})
	}
	return err
}
