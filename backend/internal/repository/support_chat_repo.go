package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

type supportChatRepository struct{ db *sql.DB }

func NewSupportChatRepository(db *sql.DB) service.SupportChatRepository {
	return &supportChatRepository{db: db}
}

const supportChatCols = `c.id,c.user_id,c.status,c.waiting_since,c.last_message_at,c.last_message_role,c.last_message_id,c.last_user_message_id,c.last_staff_message_id,c.user_read_message_id,c.staff_read_message_id,c.message_count,c.revision,c.content_version,c.blocked_at,c.blocked_reason,c.resolved_at,c.created_at,c.updated_at`

const supportChatMessageSelect = `SELECT m.id,m.author_role,m.body,m.image_id,m.client_msg_id,m.deleted_at,m.created_at,CASE WHEN m.author_role='admin' THEN COALESCE(NULLIF(a.username,''),a.email,'') ELSE '' END AS author_name,i.content_type,i.width,i.height,i.byte_size,(i.purged_at IS NOT NULL OR i.data IS NULL) FROM support_chat_messages m LEFT JOIN users a ON a.id=m.author_user_id LEFT JOIN support_chat_images i ON i.id=m.image_id `

type chatRow struct {
	c                     service.SupportChat
	lastUser, lastStaff   int64
	userRead, staffRead   int64
	wait, block, resolved sql.NullTime
	reason                string
}

func scanChat(row interface{ Scan(...any) error }) (*chatRow, error) {
	var x chatRow
	err := row.Scan(&x.c.ID, &x.c.UserID, &x.c.Status, &x.wait, &x.c.LastMessageAt, &x.c.LastMessageRole, &x.c.LastMessageID, &x.lastUser, &x.lastStaff, &x.userRead, &x.staffRead, &x.c.MessageCount, &x.c.Revision, &x.c.ContentVersion, &x.block, &x.reason, &x.resolved, &x.c.CreatedAt, &x.c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if x.wait.Valid {
		x.c.WaitingSince = &x.wait.Time
	}
	if x.block.Valid {
		x.c.Blocked = true
	}
	x.c.BlockedReason = x.reason
	if x.resolved.Valid {
		x.c.ResolvedAt = &x.resolved.Time
	}
	x.c.UserReadMessageID = x.userRead
	x.c.StaffReadMessageID = x.staffRead
	return &x, nil
}

type nullableSupportChatMessage struct {
	message     service.SupportChatMessage
	imageID     sql.NullInt64
	clientMsgID sql.NullString
	authorName  sql.NullString
	deletedAt   sql.NullTime
	contentType sql.NullString
	width       sql.NullInt64
	height      sql.NullInt64
	byteSize    sql.NullInt64
	purged      sql.NullBool
}

// scanSupportChatMessage handles nullable columns from LEFT JOIN images/users.
func scanSupportChatMessage(row interface{ Scan(...any) error }, admin bool) (*service.SupportChatMessage, error) {
	var scanned nullableSupportChatMessage
	if err := row.Scan(
		&scanned.message.ID,
		&scanned.message.AuthorRole,
		&scanned.message.Body,
		&scanned.imageID,
		&scanned.clientMsgID,
		&scanned.deletedAt,
		&scanned.message.CreatedAt,
		&scanned.authorName,
		&scanned.contentType,
		&scanned.width,
		&scanned.height,
		&scanned.byteSize,
		&scanned.purged,
	); err != nil {
		return nil, err
	}
	scanned.message.ClientMsgID = scanned.clientMsgID.String
	scanned.message.AuthorName = scanned.authorName.String
	scanned.message.Deleted = scanned.deletedAt.Valid
	if !admin {
		scanned.message.AuthorName = ""
	}
	if scanned.imageID.Valid && !scanned.message.Deleted {
		scanned.message.Image = &service.SupportChatImage{
			ID:          scanned.imageID.Int64,
			ContentType: scanned.contentType.String,
			Width:       int(scanned.width.Int64),
			Height:      int(scanned.height.Int64),
			ByteSize:    int(scanned.byteSize.Int64),
			Purged:      scanned.purged.Valid && scanned.purged.Bool,
		}
	}
	return &scanned.message, nil
}
func (r *supportChatRepository) GetUserChat(ctx context.Context, uid int64) (*service.SupportChat, error) {
	x, err := scanChat(r.db.QueryRowContext(ctx, `SELECT `+supportChatCols+` FROM support_chats c WHERE c.user_id=$1`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupportChatNotFound
	}
	if err != nil {
		return nil, err
	}
	x.c.LastUserMessageID = x.lastUser
	x.c.LastStaffMessageID = x.lastStaff
	return &x.c, nil
}
func (r *supportChatRepository) GetChat(ctx context.Context, id int64, withUser bool) (*service.SupportChat, error) {
	q := `SELECT ` + supportChatCols + ` FROM support_chats c WHERE c.id=$1`
	x, err := scanChat(r.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupportChatNotFound
	}
	if err != nil {
		return nil, err
	}
	x.c.LastUserMessageID = x.lastUser
	x.c.LastStaffMessageID = x.lastStaff
	var body sql.NullString
	var deleted sql.NullTime
	var imageID sql.NullInt64
	err = r.db.QueryRowContext(ctx, `SELECT body,deleted_at,image_id FROM support_chat_messages WHERE chat_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&body, &deleted, &imageID)
	if errors.Is(err, sql.ErrNoRows) {
		x.c.PreviewKind = "none"
	} else if err != nil {
		return nil, err
	}
	if deleted.Valid {
		x.c.PreviewKind = "deleted"
	} else if strings.TrimSpace(body.String) != "" {
		x.c.PreviewKind = "text"
		rr := []rune(body.String)
		if len(rr) > 80 {
			rr = rr[:80]
		}
		x.c.Preview = string(rr)
	} else if imageID.Valid {
		x.c.PreviewKind = "image"
	} else {
		x.c.PreviewKind = "none"
	}
	if withUser {
		x.c.User = &service.SupportTicketUser{ID: x.c.UserID}
		if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(email,''),COALESCE(username,''),COALESCE(status,''),COALESCE(balance,0)::float8,(deleted_at IS NOT NULL),created_at FROM users WHERE id=$1`, x.c.UserID).Scan(&x.c.User.Email, &x.c.User.Username, &x.c.User.Status, &x.c.User.Balance, &x.c.User.Deleted, &x.c.User.CreatedAt); err != nil {
			return nil, err
		}
	}
	return &x.c, nil
}

func (r *supportChatRepository) ListChats(ctx context.Context, in service.SupportChatListFilter) ([]service.SupportChat, int64, error) {
	where := []string{"TRUE"}
	args := []any{}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	switch in.Filter {
	case "waiting":
		where = append(where, "c.status='open' AND c.waiting_since IS NOT NULL AND u.deleted_at IS NULL")
	case "open":
		where = append(where, "c.status='open'")
	case "resolved":
		where = append(where, "c.status='resolved'")
	case "all", "":
	default:
		where = append(where, "FALSE")
	}
	if k := strings.TrimSpace(in.Keyword); k != "" {
		p := arg("%" + escapeLikeWildcards(k) + "%")
		conds := []string{"u.email ILIKE " + p + ` ESCAPE '\'`, `u.username ILIKE ` + p + ` ESCAPE '\'`}
		if id, e := strconv.ParseInt(strings.TrimPrefix(k, "#"), 10, 64); e == nil && id > 0 {
			conds = append(conds, "c.user_id = "+arg(id))
		}
		where = append(where, "("+strings.Join(conds, " OR ")+")")
	}
	from := ` FROM support_chats c LEFT JOIN users u ON u.id=c.user_id LEFT JOIN support_chat_messages lm ON lm.id=c.last_message_id WHERE ` + strings.Join(where, " AND ")
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*)"+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := ` ORDER BY c.waiting_since ASC NULLS LAST,c.last_message_at DESC,c.id DESC`
	if in.Filter == "waiting" {
		order = ` ORDER BY c.waiting_since ASC,c.id ASC`
	}
	if in.Filter == "resolved" {
		order = ` ORDER BY c.resolved_at DESC NULLS LAST,c.id DESC`
	}
	if in.Page < 1 {
		in.Page = 1
	}
	if in.PageSize < 1 {
		in.PageSize = 30
	}
	q := `SELECT ` + supportChatCols + `,
		u.id,COALESCE(u.email,''),COALESCE(u.username,''),COALESCE(u.status,''),
		COALESCE(u.balance,0)::float8,(u.deleted_at IS NOT NULL),u.created_at,
		lm.body,lm.deleted_at,lm.image_id` + from + order + " LIMIT " + arg(in.PageSize) + " OFFSET " + arg((in.Page-1)*in.PageSize)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.SupportChat{}
	for rows.Next() {
		x, e := scanChatWithUser(rows)
		if e != nil {
			return nil, 0, e
		}
		x.c.Unread = x.lastUser > x.staffRead
		out = append(out, x.c)
	}
	return out, total, rows.Err()
}

func scanChatWithUser(row interface{ Scan(...any) error }) (*chatRow, error) {
	x := &chatRow{}
	var user service.SupportTicketUser
	var userCreatedAt sql.NullTime
	var previewBody sql.NullString
	var previewDeleted sql.NullTime
	var previewImage sql.NullInt64
	if err := row.Scan(
		&x.c.ID, &x.c.UserID, &x.c.Status, &x.wait, &x.c.LastMessageAt, &x.c.LastMessageRole, &x.c.LastMessageID, &x.lastUser, &x.lastStaff, &x.userRead, &x.staffRead, &x.c.MessageCount, &x.c.Revision, &x.c.ContentVersion, &x.block, &x.reason, &x.resolved, &x.c.CreatedAt, &x.c.UpdatedAt,
		&user.ID, &user.Email, &user.Username, &user.Status, &user.Balance, &user.Deleted, &userCreatedAt, &previewBody, &previewDeleted, &previewImage,
	); err != nil {
		return nil, err
	}
	if x.wait.Valid {
		x.c.WaitingSince = &x.wait.Time
	}
	if x.block.Valid {
		x.c.Blocked = true
	}
	x.c.BlockedReason = x.reason
	if x.resolved.Valid {
		x.c.ResolvedAt = &x.resolved.Time
	}
	x.c.LastUserMessageID = x.lastUser
	x.c.LastStaffMessageID = x.lastStaff
	if userCreatedAt.Valid {
		user.CreatedAt = userCreatedAt.Time
	}
	x.c.User = &user
	x.c.UserReadMessageID = x.userRead
	x.c.StaffReadMessageID = x.staffRead
	if previewDeleted.Valid {
		x.c.PreviewKind = "deleted"
	} else if strings.TrimSpace(previewBody.String) != "" {
		x.c.PreviewKind = "text"
		runes := []rune(previewBody.String)
		if len(runes) > 80 {
			runes = runes[:80]
		}
		x.c.Preview = string(runes)
	} else if previewImage.Valid {
		x.c.PreviewKind = "image"
	} else {
		x.c.PreviewKind = "none"
	}
	return x, nil
}

func (r *supportChatRepository) ListMessages(ctx context.Context, in service.SupportChatMessagePageInput) ([]service.SupportChatMessage, bool, *service.SupportChat, error) {
	chat, err := r.GetChat(ctx, in.ChatID, false)
	if err != nil {
		return nil, false, nil, err
	}
	if in.OwnerID > 0 && chat.UserID != in.OwnerID {
		return nil, false, nil, service.ErrSupportChatNotFound
	}
	q := supportChatMessageSelect + `WHERE m.chat_id=$1`
	args := []any{in.ChatID}
	if in.OwnerID > 0 {
		q += ` AND EXISTS(SELECT 1 FROM support_chats WHERE id=$1 AND user_id=$2)`
		args = append(args, in.OwnerID)
	}
	if in.AfterID > 0 {
		q += ` AND m.id>$` + strconv.Itoa(len(args)+1)
		args = append(args, in.AfterID)
	} else if in.BeforeID > 0 {
		q += ` AND m.id<$` + strconv.Itoa(len(args)+1)
		args = append(args, in.BeforeID)
	}
	if in.AfterID > 0 {
		q += ` ORDER BY m.id ASC`
	} else {
		q += ` ORDER BY m.id DESC`
	}
	q += ` LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, in.Limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.SupportChatMessage{}
	for rows.Next() {
		m, err := scanSupportChatMessage(rows, in.Admin)
		if err != nil {
			return nil, false, nil, err
		}
		out = append(out, *m)
	}
	more := len(out) > in.Limit
	if more {
		out = out[:in.Limit]
	}
	if in.AfterID == 0 {
		for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
			out[left], out[right] = out[right], out[left]
		}
	}
	return out, more, chat, rows.Err()
}

func lockSupportChatRow(ctx context.Context, tx *sql.Tx, id int64) (*chatRow, error) {
	x, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+supportChatCols+` FROM support_chats c WHERE c.id=$1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupportChatNotFound
	}
	return x, err
}
func (r *supportChatRepository) SendMessage(ctx context.Context, in service.SupportChatSendInput) (*service.SupportChatMessage, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if in.AuthorRole == "user" {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockHash(fmt.Sprintf("support_chat:user:%d", in.UserID))); err != nil {
			return nil, false, err
		}
		if err = tx.QueryRowContext(ctx, `INSERT INTO support_chats(user_id) VALUES($1) ON CONFLICT(user_id) DO UPDATE SET updated_at=support_chats.updated_at RETURNING id`, in.UserID).Scan(&in.ChatID); err != nil {
			return nil, false, err
		}
	}
	row, err := lockSupportChatRow(ctx, tx, in.ChatID)
	if err != nil {
		return nil, false, err
	}
	if row.c.Blocked && in.AuthorRole == "user" {
		return nil, false, service.ErrSupportChatBlocked
	}
	if in.ClientMsgID != "" {
		var id int64
		if e := tx.QueryRowContext(ctx, `SELECT id FROM support_chat_messages WHERE chat_id=$1 AND client_msg_id=$2`, in.ChatID, in.ClientMsgID).Scan(&id); e == nil {
			message, scanErr := scanSupportChatMessage(tx.QueryRowContext(ctx, supportChatMessageSelect+`WHERE m.id=$1`, id), in.AuthorRole == service.SupportChatRoleAdmin)
			if scanErr != nil {
				return nil, false, scanErr
			}
			message.ChatID = row.c.ID
			return message, true, nil
		}
	}
	if in.AuthorRole == "user" {
		var n int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM support_chat_messages WHERE chat_id=$1 AND author_role='user' AND created_at>NOW()-INTERVAL '1 minute'`, in.ChatID).Scan(&n); err != nil {
			return nil, false, err
		}
		if n >= service.SupportChatMessagesPerMinute {
			return nil, false, service.ErrSupportChatTooFast
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM support_chat_messages WHERE chat_id=$1 AND author_role='user' AND created_at>NOW()-INTERVAL '24 hours'`, in.ChatID).Scan(&n); err != nil {
			return nil, false, err
		}
		if n >= service.SupportChatMessagesPerDay {
			return nil, false, service.ErrSupportChatDailyLimit
		}
	}
	var imageID any
	if in.AuthorRole == "user" && in.Image != nil {
		var imageCount, imageBytes int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(byte_size),0) FROM support_chat_images WHERE chat_id=$1 AND author_role='user' AND created_at>NOW()-INTERVAL '24 hours'`, in.ChatID).Scan(&imageCount, &imageBytes); err != nil {
			return nil, false, err
		}
		if imageCount >= service.SupportChatImagesPerDay || imageBytes+int64(len(in.Image.Data)) > service.SupportChatImageBytesPerDay {
			return nil, false, service.ErrSupportChatImageDailyLimit
		}
	}
	if in.Image != nil {
		var id int64
		err = tx.QueryRowContext(ctx, `INSERT INTO support_chat_images(chat_id,author_role,content_type,byte_size,width,height,data) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, in.ChatID, in.AuthorRole, in.Image.ContentType, len(in.Image.Data), in.Image.Width, in.Image.Height, in.Image.Data).Scan(&id)
		if err != nil {
			return nil, false, err
		}
		imageID = id
	}
	var m service.SupportChatMessage
	err = tx.QueryRowContext(ctx, `INSERT INTO support_chat_messages(chat_id,author_role,author_user_id,body,image_id,client_msg_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at`, in.ChatID, in.AuthorRole, nullableID(in.AuthorUserID), in.Body, imageID, in.ClientMsgID).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		if in.ClientMsgID != "" && strings.Contains(err.Error(), "idx_support_chat_messages_client_id") {
			_ = tx.Rollback()
			duplicate, readErr := r.readDuplicateMessage(ctx, in.ChatID, in.ClientMsgID, in.AuthorRole == service.SupportChatRoleAdmin)
			if readErr != nil {
				return nil, false, readErr
			}
			duplicate.ChatID = row.c.ID
			return duplicate, true, nil
		}
		return nil, false, err
	}
	m.AuthorRole = in.AuthorRole
	m.Body = in.Body
	m.ClientMsgID = in.ClientMsgID
	if in.Image != nil {
		imageID64, _ := imageID.(int64)
		m.Image = &service.SupportChatImage{ID: imageID64, ContentType: in.Image.ContentType, Width: in.Image.Width, Height: in.Image.Height, ByteSize: len(in.Image.Data)}
	}
	var statusExpr string
	switch in.AuthorRole {
	case "user":
		statusExpr = `status='open',waiting_since=COALESCE(waiting_since,NOW()),last_user_message_id=$2`
	case "admin":
		statusExpr = `waiting_since=NULL,last_staff_message_id=$2`
	default:
		statusExpr = `status=status`
	}
	if _, err = tx.ExecContext(ctx, `UPDATE support_chats SET `+statusExpr+`,last_message_id=$2,last_message_role=$3,message_count=message_count+1,revision=revision+1,last_message_at=NOW(),updated_at=NOW() WHERE id=$1`, in.ChatID, m.ID, in.AuthorRole); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	m.ChatID = row.c.ID
	return &m, false, nil
}

func (r *supportChatRepository) readDuplicateMessage(ctx context.Context, chatID int64, clientID string, admin bool) (*service.SupportChatMessage, error) {
	return scanSupportChatMessage(r.db.QueryRowContext(ctx, supportChatMessageSelect+`WHERE m.chat_id=$1 AND m.client_msg_id=$2`, chatID, clientID), admin)
}

func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}
func (r *supportChatRepository) MarkRead(ctx context.Context, id int64, admin bool, n int64) (int64, error) {
	col := "user_read_message_id"
	if admin {
		col = "staff_read_message_id"
	}
	var out int64
	err := r.db.QueryRowContext(ctx, `UPDATE support_chats SET `+col+`=GREATEST(`+col+`,LEAST($2,last_message_id)),revision=revision+CASE WHEN `+col+`<>GREATEST(`+col+`,LEAST($2,last_message_id)) THEN 1 ELSE 0 END WHERE id=$1 RETURNING `+col, id, n).Scan(&out)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, service.ErrSupportChatNotFound
	}
	return out, err
}
func (r *supportChatRepository) SetStatus(ctx context.Context, in service.SupportChatStatusInput) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	x, err := lockSupportChatRow(ctx, tx, in.ChatID)
	if err != nil {
		return err
	}
	if in.Status == "resolved" {
		if x.lastUser > in.LastSeenMessageID {
			return service.ErrSupportChatChanged
		}
		_, err = tx.ExecContext(ctx, `UPDATE support_chats SET status='resolved',waiting_since=NULL,resolved_at=NOW(),revision=revision+1,updated_at=NOW() WHERE id=$1`, in.ChatID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE support_chats SET status='open',resolved_at=NULL,revision=revision+1,updated_at=NOW() WHERE id=$1`, in.ChatID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *supportChatRepository) SetBlocked(ctx context.Context, in service.SupportChatBlockInput) error {
	reason := in.Reason
	if !in.Blocked {
		reason = ""
	}
	result, err := r.db.ExecContext(ctx, `UPDATE support_chats SET blocked_at=CASE WHEN $2 THEN NOW() ELSE NULL END,blocked_reason=$3,revision=revision+1,updated_at=NOW() WHERE id=$1`, in.ChatID, in.Blocked, reason)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return service.ErrSupportChatNotFound
	}
	return nil
}
func (r *supportChatRepository) DeleteMessage(ctx context.Context, cid, mid int64) (*service.SupportChatMessage, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = lockSupportChatRow(ctx, tx, cid); err != nil {
		return nil, err
	}
	var m service.SupportChatMessage
	var imageID sql.NullInt64
	err = tx.QueryRowContext(ctx, `UPDATE support_chat_messages SET body='',deleted_at=NOW(),deleted_by_role='admin' WHERE id=$1 AND chat_id=$2 AND deleted_at IS NULL RETURNING id,image_id`, mid, cid).Scan(&m.ID, &imageID)
	if errors.Is(err, sql.ErrNoRows) {
		var existingImageID sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT image_id FROM support_chat_messages WHERE id=$1 AND chat_id=$2 AND deleted_at IS NOT NULL`, mid, cid).Scan(&existingImageID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrSupportChatMessageNotFound
		}
		if err != nil {
			return nil, err
		}
		if existingImageID.Valid {
			if _, err = tx.ExecContext(ctx, `UPDATE support_chat_images SET data=NULL,purged_at=COALESCE(purged_at,NOW()) WHERE id=$1`, existingImageID.Int64); err != nil {
				return nil, err
			}
		}
		message, scanErr := scanSupportChatMessage(tx.QueryRowContext(ctx, supportChatMessageSelect+`WHERE m.id=$1 AND m.chat_id=$2`, mid, cid), true)
		if scanErr != nil {
			if errors.Is(scanErr, sql.ErrNoRows) {
				return nil, service.ErrSupportChatMessageNotFound
			}
			return nil, scanErr
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return message, nil
	}
	if err != nil {
		return nil, err
	}
	if imageID.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE support_chat_images SET data=NULL,purged_at=COALESCE(purged_at,NOW()) WHERE id=$1`, imageID.Int64); err != nil {
			return nil, err
		}
	}
	message, scanErr := scanSupportChatMessage(tx.QueryRowContext(ctx, supportChatMessageSelect+`WHERE m.id=$1`, mid), true)
	if scanErr != nil {
		return nil, scanErr
	}
	m = *message
	_, err = tx.ExecContext(ctx, `UPDATE support_chats SET content_version=content_version+1,revision=revision+1,updated_at=NOW() WHERE id=$1`, cid)
	if err != nil {
		return nil, err
	}
	m.Deleted = true
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &m, nil
}
func (r *supportChatRepository) DeleteChat(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var userID, revision int64
	var blockedAt sql.NullTime
	var blockedReason string
	err = tx.QueryRowContext(ctx, `SELECT user_id,blocked_at,blocked_reason,revision FROM support_chats WHERE id=$1 FOR UPDATE`, id).Scan(&userID, &blockedAt, &blockedReason, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrSupportChatNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM support_chats WHERE id=$1`, id); err != nil {
		return err
	}
	if blockedAt.Valid {
		// revision+1 keeps the staff summary version (SUM(revision)) moving, even
		// when the deleted row was itself an untouched placeholder.
		if _, err = tx.ExecContext(ctx, `INSERT INTO support_chats(user_id,status,blocked_at,blocked_reason,revision) VALUES($1,'resolved',$2,$3,$4)`, userID, blockedAt.Time, blockedReason, revision+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (r *supportChatRepository) GetImage(ctx context.Context, id, owner int64, admin bool) (*service.SupportChatImageResult, error) {
	q := `SELECT i.id,i.content_type,i.width,i.height,i.byte_size,i.data,i.purged_at,c.user_id,m.deleted_at FROM support_chat_images i JOIN support_chats c ON c.id=i.chat_id LEFT JOIN support_chat_messages m ON m.image_id=i.id WHERE i.id=$1`
	args := []any{id}
	if !admin {
		q += ` AND c.user_id=$2`
		args = append(args, owner)
	}
	var out service.SupportChatImageResult
	var data []byte
	var purged, deleted sql.NullTime
	var chatUserID int64
	err := r.db.QueryRowContext(ctx, q, args...).Scan(&out.Image.ID, &out.Image.ContentType, &out.Image.Width, &out.Image.Height, &out.Image.ByteSize, &data, &purged, &chatUserID, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupportChatMessageNotFound
	}
	if err != nil {
		return nil, err
	}
	if purged.Valid || deleted.Valid || data == nil {
		return nil, service.ErrSupportChatImageGone
	}
	out.Data = data
	return &out, nil
}
func (r *supportChatRepository) Summary(ctx context.Context) (service.SupportChatSummary, error) {
	var s service.SupportChatSummary
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE c.status='open' AND c.waiting_since IS NOT NULL AND u.deleted_at IS NULL),COALESCE(MAX(c.last_user_message_id),0),COALESCE(SUM(c.revision),0) FROM support_chats c LEFT JOIN users u ON u.id=c.user_id`).Scan(&s.ChatWaiting, &s.LatestUserMessageID, &s.Version)
	return s, err
}
func (r *supportChatRepository) PurgeImages(ctx context.Context, days int, now time.Time) (int64, int64, error) {
	var rows, bytes int64
	for {
		var n, b int64
		err := r.db.QueryRowContext(ctx, `WITH picked AS (SELECT i.id,i.byte_size FROM support_chat_images i JOIN support_chats c ON c.id=i.chat_id LEFT JOIN users u ON u.id=c.user_id WHERE i.purged_at IS NULL AND (i.created_at < $1 OR u.deleted_at IS NOT NULL) ORDER BY i.id LIMIT 500), gone AS (UPDATE support_chat_images i SET data=NULL,purged_at=NOW() FROM picked p WHERE i.id=p.id RETURNING p.byte_size) SELECT COUNT(*),COALESCE(SUM(byte_size),0) FROM gone`, now.Add(-time.Duration(days)*24*time.Hour)).Scan(&n, &b)
		if err != nil {
			return rows, bytes, err
		}
		rows += n
		bytes += b
		if n < 500 {
			return rows, bytes, nil
		}
	}
}

func (r *supportChatRepository) UnreadCount(ctx context.Context, chatID, uid int64) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM support_chat_messages m JOIN support_chats c ON c.id=m.chat_id WHERE m.chat_id=$1 AND c.user_id=$2 AND m.deleted_at IS NULL AND m.author_role <> 'user' AND m.id > c.user_read_message_id`, chatID, uid).Scan(&n)
	return n, err
}
