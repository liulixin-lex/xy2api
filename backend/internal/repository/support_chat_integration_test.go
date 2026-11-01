//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type supportChatFixture struct {
	repo  service.SupportChatRepository
	user  *service.User
	admin *service.User
}

func newSupportChatFixture(t *testing.T) *supportChatFixture {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email:    "support-chat-user-" + uuid.NewString() + "@example.com",
		Username: "chat-user",
	})
	admin := mustCreateUser(t, client, &service.User{
		Email: "support-chat-admin-" + uuid.NewString() + "@example.com",
		Role:  service.RoleAdmin,
	})
	fixture := &supportChatFixture{
		repo:  NewSupportChatRepository(integrationDB),
		user:  user,
		admin: admin,
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = ANY($1)`, pq.Array([]int64{user.ID, admin.ID}))
	})
	return fixture
}

func sendSupportChatMessage(t *testing.T, repo service.SupportChatRepository, input service.SupportChatSendInput) *service.SupportChatMessage {
	t.Helper()
	message, duplicate, err := repo.SendMessage(context.Background(), input)
	require.NoError(t, err)
	require.False(t, duplicate)
	var chatID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT chat_id FROM support_chat_messages WHERE id=$1`, message.ID).Scan(&chatID))
	require.Equal(t, chatID, message.ChatID)
	return message
}

func sendSupportChatText(t *testing.T, fixture *supportChatFixture, body, clientID string) *service.SupportChatMessage {
	t.Helper()
	return sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
		UserID:       fixture.user.ID,
		AuthorUserID: fixture.user.ID,
		AuthorRole:   service.SupportChatRoleUser,
		ClientMsgID:  clientID,
		Body:         body,
	})
}

func sendSupportChatStaffText(t *testing.T, fixture *supportChatFixture, body, clientID string) *service.SupportChatMessage {
	t.Helper()
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	return sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
		ChatID:       chat.ID,
		AuthorUserID: fixture.admin.ID,
		AuthorRole:   service.SupportChatRoleAdmin,
		ClientMsgID:  clientID,
		Body:         body,
	})
}

func sendSupportChatRoleText(t *testing.T, fixture *supportChatFixture, chatID int64, role, body, clientID string) *service.SupportChatMessage {
	t.Helper()
	return sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
		ChatID:       chatID,
		AuthorUserID: fixture.admin.ID,
		AuthorRole:   role,
		ClientMsgID:  clientID,
		Body:         body,
	})
}

func sendSupportChatImage(t *testing.T, fixture *supportChatFixture, body, clientID string) *service.SupportChatMessage {
	t.Helper()
	return sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
		UserID:       fixture.user.ID,
		AuthorUserID: fixture.user.ID,
		AuthorRole:   service.SupportChatRoleUser,
		ClientMsgID:  clientID,
		Body:         body,
		Image: &service.SupportChatImageInput{
			Data:        tinySupportChatPNG(),
			ContentType: "image/png",
			Width:       2,
			Height:      2,
		},
	})
}

func tinySupportChatPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52,
		0, 0, 0, 2, 0, 0, 0, 2, 8, 6, 0, 0, 0, 0, 0, 0,
	}
}

func TestSupportChatRepositoryLifecycle(t *testing.T) {
	t.Run("concurrent first sends create one chat and ordered ids", testSupportChatConcurrentFirstSends)
	t.Run("row lock preserves commit order for after polling", testSupportChatOrderingInvariant)
	t.Run("client id duplicate preserves image and bypasses limits", testSupportChatClientIDDuplicate)
	t.Run("caps and blocked moderation", testSupportChatCapsAndBlocked)
	t.Run("waiting since lifecycle", testSupportChatWaitingSince)
	t.Run("read cursor monotonic clamped and unread scoped", testSupportChatReadCursor)
	t.Run("newest before and after paging", testSupportChatPaging)
	t.Run("tombstone keeps row and purges image", testSupportChatTombstone)
	t.Run("user recall window, staff recall and staff identity", testSupportChatRecall)
	t.Run("image ownership and admin access", testSupportChatImageOwnership)
	t.Run("list filters ordering keywords preview and cards", testSupportChatListChats)
	t.Run("summary counters and revision", testSupportChatSummary)
	t.Run("chat and raw user deletes cascade", testSupportChatDeletes)
	t.Run("purge batches and soft deleted users", testSupportChatPurge)
}

func testSupportChatConcurrentFirstSends(t *testing.T) {
	fixture := newSupportChatFixture(t)
	const messageCount = 10
	start := make(chan struct{})
	ids := make(chan int64, messageCount)
	errors := make(chan error, messageCount)
	var group sync.WaitGroup
	for index := 0; index < messageCount; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			message, duplicate, sendErr := fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
				UserID:       fixture.user.ID,
				AuthorUserID: fixture.user.ID,
				AuthorRole:   service.SupportChatRoleUser,
				ClientMsgID:  fmt.Sprintf("concurrent-%03d", index),
				Body:         fmt.Sprintf("message-%d", index),
			})
			if sendErr != nil {
				errors <- sendErr
				return
			}
			if duplicate {
				errors <- fmt.Errorf("concurrent send unexpectedly returned duplicate")
				return
			}
			ids <- message.ID
		}(index)
	}
	close(start)
	group.Wait()
	close(ids)
	close(errors)
	for sendErr := range errors {
		require.NoError(t, sendErr)
	}

	collected := make([]int64, 0, messageCount)
	for id := range ids {
		collected = append(collected, id)
	}
	require.Len(t, collected, messageCount)
	seenIDs := make(map[int64]struct{}, len(collected))
	for _, id := range collected {
		_, alreadySeen := seenIDs[id]
		require.False(t, alreadySeen, "each concurrent send must create one message id")
		seenIDs[id] = struct{}{}
	}

	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	var chatCount, messageRows int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chats WHERE user_id=$1`, fixture.user.ID).Scan(&chatCount))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_messages WHERE chat_id=$1`, chat.ID).Scan(&messageRows))
	require.Equal(t, 1, chatCount)
	require.Equal(t, messageCount, messageRows)
	items, more, _, err := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: chat.ID, OwnerID: fixture.user.ID, Limit: messageCount})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, items, messageCount)
	for index := 1; index < len(items); index++ {
		require.Less(t, items[index-1].ID, items[index].ID)
	}
	var nullableMessageID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		INSERT INTO support_chat_messages(chat_id,author_role,body,client_msg_id)
		VALUES($1,'system','nullable scan check',NULL)
		RETURNING id`, chat.ID).Scan(&nullableMessageID))
	items, _, _, err = fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: chat.ID, OwnerID: fixture.user.ID, Limit: 50})
	require.NoError(t, err)
	var nullableMessage *service.SupportChatMessage
	for index := range items {
		if items[index].ID == nullableMessageID {
			nullableMessage = &items[index]
			break
		}
	}
	require.NotNil(t, nullableMessage)
	require.Empty(t, nullableMessage.ClientMsgID)
	require.Empty(t, nullableMessage.AuthorName)
}

func testSupportChatOrderingInvariant(t *testing.T) {
	fixture := newSupportChatFixture(t)
	previous := sendSupportChatText(t, fixture, "before", "order-before")
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)

	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	_, err = lockSupportChatRow(context.Background(), tx, chat.ID)
	require.NoError(t, err)
	var staffID int64
	require.NoError(t, tx.QueryRowContext(context.Background(), `
		INSERT INTO support_chat_messages(chat_id,author_role,author_user_id,body,client_msg_id)
		VALUES($1,'admin',$2,$3,$4)
		RETURNING id`, chat.ID, fixture.admin.ID, "staff first", "order-staff").Scan(&staffID))
	_, err = tx.ExecContext(context.Background(), `
		UPDATE support_chats
		SET last_message_id=$2,last_staff_message_id=$2,last_message_role='admin',
		    message_count=message_count+1,revision=revision+1,last_message_at=NOW(),updated_at=NOW()
		WHERE id=$1`, chat.ID, staffID)
	require.NoError(t, err)

	userDone := make(chan error, 1)
	go func() {
		_, _, sendErr := fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
			UserID:       fixture.user.ID,
			AuthorUserID: fixture.user.ID,
			AuthorRole:   service.SupportChatRoleUser,
			ClientMsgID:  "order-user",
			Body:         "user second",
		})
		userDone <- sendErr
	}()
	select {
	case sendErr := <-userDone:
		t.Fatalf("user send completed while the chat row was locked: %v", sendErr)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	require.NoError(t, <-userDone)

	items, more, _, err := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{
		ChatID:  chat.ID,
		OwnerID: fixture.user.ID,
		AfterID: previous.ID,
		Limit:   30,
	})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, items, 2)
	require.Equal(t, staffID, items[0].ID)
	require.Equal(t, "staff first", items[0].Body)
	require.Equal(t, "user second", items[1].Body)
	require.Less(t, items[0].ID, items[1].ID)
}

func testSupportChatClientIDDuplicate(t *testing.T) {
	fixture := newSupportChatFixture(t)
	first := sendSupportChatImage(t, fixture, "image", "image-dup-1")
	second, duplicate, err := fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
		UserID:       fixture.user.ID,
		AuthorUserID: fixture.user.ID,
		AuthorRole:   service.SupportChatRoleUser,
		ClientMsgID:  "image-dup-1",
		Body:         "different body",
		Image: &service.SupportChatImageInput{
			Data:        tinySupportChatPNG(),
			ContentType: "image/png",
			Width:       2,
			Height:      2,
		},
	})
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, first.ChatID, second.ChatID)
	require.Equal(t, first.Image.ID, second.Image.ID)
	var imageRows int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_images WHERE chat_id=(SELECT chat_id FROM support_chat_messages WHERE id=$1)`, first.ID).Scan(&imageRows))
	require.Equal(t, 1, imageRows)
	for index := 0; index < service.SupportChatMessagesPerMinute-2; index++ {
		sendSupportChatText(t, fixture, "follow-up", fmt.Sprintf("dup-follow-%03d", index))
	}
	_, _, err = fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
		UserID: fixture.user.ID, AuthorUserID: fixture.user.ID, AuthorRole: service.SupportChatRoleUser,
		ClientMsgID: "duplicate-limit-check", Body: "the twentieth message is allowed",
	})
	require.NoError(t, err)
}

func testSupportChatCapsAndBlocked(t *testing.T) {
	t.Run("daily message cap", func(t *testing.T) {
		fixture := newSupportChatFixture(t)
		sendSupportChatText(t, fixture, "first", "daily-first")
		chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), `
			INSERT INTO support_chat_messages(chat_id,author_role,author_user_id,body,created_at)
			SELECT $1,'user',$2,'seed',NOW()-interval '1 hour' FROM generate_series(1,$3)`, chat.ID, fixture.user.ID, service.SupportChatMessagesPerDay)
		require.NoError(t, err)
		_, _, err = fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
			UserID: fixture.user.ID, AuthorUserID: fixture.user.ID, AuthorRole: service.SupportChatRoleUser,
			ClientMsgID: "daily-cap-check", Body: "over",
		})
		require.ErrorIs(t, err, service.ErrSupportChatDailyLimit)
	})

	t.Run("minute cap and unrestricted staff message", func(t *testing.T) {
		fixture := newSupportChatFixture(t)
		sendSupportChatText(t, fixture, "first", "minute-first")
		chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), `
			INSERT INTO support_chat_messages(chat_id,author_role,author_user_id,body,created_at)
			SELECT $1,'user',$2,'seed',NOW() FROM generate_series(1,$3)`, chat.ID, fixture.user.ID, service.SupportChatMessagesPerMinute-1)
		require.NoError(t, err)
		_, _, err = fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
			UserID: fixture.user.ID, AuthorUserID: fixture.user.ID, AuthorRole: service.SupportChatRoleUser,
			ClientMsgID: "minute-cap-check", Body: "over",
		})
		require.ErrorIs(t, err, service.ErrSupportChatTooFast)
		staff := sendSupportChatStaffText(t, fixture, "staff is never rate limited", "minute-staff")
		require.Equal(t, service.SupportChatRoleAdmin, staff.AuthorRole)
	})

	t.Run("image count and byte caps", func(t *testing.T) {
		t.Run("count", func(t *testing.T) {
			fixture := newSupportChatFixture(t)
			sendSupportChatText(t, fixture, "first", "image-count-first")
			chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(context.Background(), `
				INSERT INTO support_chat_images(chat_id,author_role,content_type,byte_size,width,height,data)
				SELECT $1,'user','image/png',1,1,1,decode('00','hex') FROM generate_series(1,$2)`, chat.ID, service.SupportChatImagesPerDay)
			require.NoError(t, err)
			_, _, err = fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
				UserID: fixture.user.ID, AuthorUserID: fixture.user.ID, AuthorRole: service.SupportChatRoleUser,
				ClientMsgID: "image-count-check", Body: "image", Image: &service.SupportChatImageInput{Data: tinySupportChatPNG(), ContentType: "image/png", Width: 2, Height: 2},
			})
			require.ErrorIs(t, err, service.ErrSupportChatImageDailyLimit)
		})
		t.Run("bytes", func(t *testing.T) {
			fixture := newSupportChatFixture(t)
			sendSupportChatText(t, fixture, "first", "image-bytes-first")
			chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(context.Background(), `
				INSERT INTO support_chat_images(chat_id,author_role,content_type,byte_size,width,height,data)
				SELECT $1,'user','image/png',$2,1,1,decode('00','hex') FROM generate_series(1,14)`, chat.ID, service.SupportChatImageMaxBytes)
			require.NoError(t, err)
			_, _, err = fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
				UserID: fixture.user.ID, AuthorUserID: fixture.user.ID, AuthorRole: service.SupportChatRoleUser,
				ClientMsgID: "image-bytes-check", Body: "image", Image: &service.SupportChatImageInput{Data: tinySupportChatPNG(), ContentType: "image/png", Width: 2, Height: 2},
			})
			require.ErrorIs(t, err, service.ErrSupportChatImageDailyLimit)
		})
	})

	t.Run("blocked users cannot send but staff can", func(t *testing.T) {
		fixture := newSupportChatFixture(t)
		sendSupportChatText(t, fixture, "first", "blocked-first")
		chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
		require.NoError(t, err)
		require.NoError(t, fixture.repo.SetBlocked(context.Background(), service.SupportChatBlockInput{ChatID: chat.ID, Blocked: true, Reason: "abuse"}))
		_, _, err = fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{
			UserID: fixture.user.ID, AuthorUserID: fixture.user.ID, AuthorRole: service.SupportChatRoleUser,
			ClientMsgID: "blocked-user", Body: "blocked",
		})
		require.ErrorIs(t, err, service.ErrSupportChatBlocked)
		sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
			ChatID: chat.ID, AuthorUserID: fixture.admin.ID, AuthorRole: service.SupportChatRoleAdmin,
			ClientMsgID: "blocked-staff", Body: "staff reply",
		})
	})
}

func testSupportChatWaitingSince(t *testing.T) {
	fixture := newSupportChatFixture(t)
	sendSupportChatText(t, fixture, "first", "waiting-first")
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.NotNil(t, chat.WaitingSince)
	firstWaiting := *chat.WaitingSince
	sendSupportChatText(t, fixture, "follow-up", "waiting-follow-up")
	chat, err = fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.NotNil(t, chat.WaitingSince)
	require.Equal(t, firstWaiting, *chat.WaitingSince)
	sendSupportChatRoleText(t, fixture, chat.ID, service.SupportChatRoleSystem, "system note", "waiting-system")
	chat, err = fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.Equal(t, firstWaiting, *chat.WaitingSince)
	sendSupportChatStaffText(t, fixture, "staff answer", "waiting-staff")
	chat, err = fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.Nil(t, chat.WaitingSince)
	adminMessages, _, _, err := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: chat.ID, Admin: true, Limit: 30})
	require.NoError(t, err)
	var staffMessage *service.SupportChatMessage
	for index := range adminMessages {
		if adminMessages[index].ClientMsgID == "waiting-staff" {
			staffMessage = &adminMessages[index]
			break
		}
	}
	require.NotNil(t, staffMessage)
	require.Equal(t, fixture.admin.Email, staffMessage.AuthorName)
	require.Equal(t, fixture.admin.ID, staffMessage.StaffID)
	lastUserID := chat.LastUserMessageID
	require.NoError(t, fixture.repo.SetStatus(context.Background(), service.SupportChatStatusInput{ChatID: chat.ID, Status: service.SupportChatStatusResolved, LastSeenMessageID: lastUserID}))
	chat, err = fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.Equal(t, service.SupportChatStatusResolved, chat.Status)
	require.Nil(t, chat.WaitingSince)
	beforeReopen := time.Now()
	sendSupportChatText(t, fixture, "new question", "waiting-reopen")
	chat, err = fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.Equal(t, service.SupportChatStatusOpen, chat.Status)
	require.NotNil(t, chat.WaitingSince)
	require.True(t, !chat.WaitingSince.Before(beforeReopen))
}

func testSupportChatReadCursor(t *testing.T) {
	fixture := newSupportChatFixture(t)
	userMessage := sendSupportChatText(t, fixture, "user", "read-user")
	staffMessage := sendSupportChatStaffText(t, fixture, "staff", "read-staff")
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	unread, err := fixture.repo.UnreadCount(context.Background(), chat.ID, fixture.user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), unread)
	userCursor, err := fixture.repo.MarkRead(context.Background(), chat.ID, false, 999999)
	require.NoError(t, err)
	require.Equal(t, staffMessage.ID, userCursor)
	adminCursor, err := fixture.repo.MarkRead(context.Background(), chat.ID, true, 999999)
	require.NoError(t, err)
	require.Equal(t, staffMessage.ID, adminCursor)
	backwards, err := fixture.repo.MarkRead(context.Background(), chat.ID, false, userMessage.ID)
	require.NoError(t, err)
	require.Equal(t, userCursor, backwards)
	chat, err = fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.Equal(t, userCursor, chat.UserReadMessageID)
	require.Equal(t, adminCursor, chat.StaffReadMessageID)
	var revisionBefore, revisionAfter int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT revision FROM support_chats WHERE id=$1`, chat.ID).Scan(&revisionBefore))
	_, err = fixture.repo.MarkRead(context.Background(), chat.ID, false, userCursor)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT revision FROM support_chats WHERE id=$1`, chat.ID).Scan(&revisionAfter))
	require.Equal(t, revisionBefore, revisionAfter)

	newStaff := sendSupportChatStaffText(t, fixture, "new staff", "read-staff-2")
	unread, err = fixture.repo.UnreadCount(context.Background(), chat.ID, fixture.user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), unread)
	_, err = fixture.repo.DeleteMessage(context.Background(), chat.ID, newStaff.ID)
	require.NoError(t, err)
	unread, err = fixture.repo.UnreadCount(context.Background(), chat.ID, fixture.user.ID)
	require.NoError(t, err)
	require.Zero(t, unread)
}

func testSupportChatPaging(t *testing.T) {
	fixture := newSupportChatFixture(t)
	messageIDs := make([]int64, 0, 75)
	for index := 0; index < 75; index++ {
		message := sendSupportChatText(t, fixture, fmt.Sprintf("message-%02d", index), fmt.Sprintf("page-%03d", index))
		messageIDs = append(messageIDs, message.ID)
		_, err := integrationDB.ExecContext(context.Background(), `UPDATE support_chat_messages SET created_at=NOW()-interval '2 minutes' WHERE chat_id=(SELECT id FROM support_chats WHERE user_id=$1)`, fixture.user.ID)
		require.NoError(t, err)
	}
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	newest, more, _, err := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: chat.ID, OwnerID: fixture.user.ID, Limit: 30})
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, newest, 30)
	require.Equal(t, messageIDs[45], newest[0].ID)
	require.Equal(t, "message-74", newest[len(newest)-1].Body)

	allOlder := make([]service.SupportChatMessage, 0, 45)
	beforeID := newest[0].ID
	for {
		page, hasMore, _, pageErr := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: chat.ID, OwnerID: fixture.user.ID, BeforeID: beforeID, Limit: 30})
		require.NoError(t, pageErr)
		allOlder = append(page, allOlder...)
		if !hasMore {
			break
		}
		require.NotEmpty(t, page)
		beforeID = page[0].ID
	}
	require.Len(t, allOlder, 45)
	require.Equal(t, messageIDs[0], allOlder[0].ID)
	require.Equal(t, messageIDs[44], allOlder[len(allOlder)-1].ID)
	for index := 1; index < len(allOlder); index++ {
		require.Less(t, allOlder[index-1].ID, allOlder[index].ID)
	}
	after, hasMore, _, err := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: chat.ID, OwnerID: fixture.user.ID, AfterID: messageIDs[44], Limit: 30})
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Len(t, after, 30)
	require.Equal(t, messageIDs[45], after[0].ID)
	require.Equal(t, messageIDs[74], after[len(after)-1].ID)
}

func testSupportChatTombstone(t *testing.T) {
	fixture := newSupportChatFixture(t)
	message := sendSupportChatImage(t, fixture, "image body", "tombstone-image")
	sendSupportChatStaffText(t, fixture, "staff cursor", "tombstone-staff")
	chatBefore, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	_, err = fixture.repo.MarkRead(context.Background(), chatBefore.ID, false, chatBefore.LastMessageID)
	require.NoError(t, err)
	_, err = fixture.repo.MarkRead(context.Background(), chatBefore.ID, true, chatBefore.LastMessageID)
	require.NoError(t, err)
	chatBefore, err = fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	deleted, err := fixture.repo.DeleteMessage(context.Background(), chatBefore.ID, message.ID)
	require.NoError(t, err)
	require.True(t, deleted.Deleted)
	var imageDataNull, imagePurged bool
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT data IS NULL,purged_at IS NOT NULL FROM support_chat_images WHERE id=$1`, message.Image.ID).Scan(&imageDataNull, &imagePurged))
	require.True(t, imageDataNull)
	require.True(t, imagePurged)
	chatAfter, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.Equal(t, chatBefore.UserReadMessageID, chatAfter.UserReadMessageID)
	require.Equal(t, chatBefore.StaffReadMessageID, chatAfter.StaffReadMessageID)
	require.Equal(t, chatBefore.ContentVersion+1, chatAfter.ContentVersion)
	require.Equal(t, chatBefore.Revision+1, chatAfter.Revision)
	deletedAgain, err := fixture.repo.DeleteMessage(context.Background(), chatAfter.ID, message.ID)
	require.NoError(t, err)
	require.True(t, deletedAgain.Deleted)
	_, err = fixture.repo.GetImage(context.Background(), message.Image.ID, fixture.user.ID, false)
	require.ErrorIs(t, err, service.ErrSupportChatImageGone)
	items, _, _, err := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: chatAfter.ID, OwnerID: fixture.user.ID, Limit: 30})
	require.NoError(t, err)
	var tombstone *service.SupportChatMessage
	for index := range items {
		if items[index].ID == message.ID {
			tombstone = &items[index]
			break
		}
	}
	require.NotNil(t, tombstone)
	require.True(t, tombstone.Deleted)
	require.False(t, tombstone.Recalled, "staff removing a user's message is a delete, not a recall")
	require.Empty(t, tombstone.Body)
	require.Nil(t, tombstone.Image)
}

func testSupportChatRecall(t *testing.T) {
	ctx := context.Background()
	fixture := newSupportChatFixture(t)
	message := sendSupportChatImage(t, fixture, "pasted sk-secret", "recall-image")
	before := mustGetSupportChat(t, fixture.repo, fixture.user.ID)
	require.NotNil(t, before.WaitingSince)

	// A user takes back a fresh message: text and image go, and the wait it started ends.
	recalled, err := fixture.repo.RecallMessage(ctx, before.ID, message.ID, service.SupportChatRecallWindow)
	require.NoError(t, err)
	require.True(t, recalled.Deleted)
	require.True(t, recalled.Recalled)
	require.Empty(t, recalled.Body)
	require.Nil(t, recalled.Image)
	var dataNull bool
	var deletedBy string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT data IS NULL FROM support_chat_images WHERE id=$1`, message.Image.ID).Scan(&dataNull))
	require.True(t, dataNull)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT deleted_by_role FROM support_chat_messages WHERE id=$1`, message.ID).Scan(&deletedBy))
	require.Equal(t, service.SupportChatRoleUser, deletedBy)
	after := mustGetSupportChat(t, fixture.repo, fixture.user.ID)
	require.Nil(t, after.WaitingSince)
	require.Equal(t, before.ContentVersion+1, after.ContentVersion)
	require.Equal(t, before.Revision+1, after.Revision)
	again, err := fixture.repo.RecallMessage(ctx, before.ID, message.ID, service.SupportChatRecallWindow)
	require.NoError(t, err)
	require.True(t, again.Recalled)
	require.Equal(t, after.ContentVersion, mustGetSupportChat(t, fixture.repo, fixture.user.ID).ContentVersion, "a repeated recall changes nothing")
	detail, err := fixture.repo.GetChat(ctx, before.ID, true)
	require.NoError(t, err)
	require.Equal(t, "recalled", detail.PreviewKind)
	listed, _, err := fixture.repo.ListChats(ctx, service.SupportChatListFilter{Filter: "all", Keyword: fixture.user.Email, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "recalled", listed[0].PreviewKind)

	// Users cannot recall staff messages or messages of another chat.
	staff := sendSupportChatStaffText(t, fixture, "staff reply", "recall-staff")
	require.Equal(t, fixture.admin.ID, staff.StaffID, "the send result names the staff account")
	_, err = fixture.repo.RecallMessage(ctx, before.ID, staff.ID, service.SupportChatRecallWindow)
	require.ErrorIs(t, err, service.ErrSupportChatMessageNotFound)
	other := newSupportChatFixture(t)
	foreign := sendSupportChatText(t, other, "not yours", "recall-foreign")
	_, err = fixture.repo.RecallMessage(ctx, before.ID, foreign.ID, service.SupportChatRecallWindow)
	require.ErrorIs(t, err, service.ErrSupportChatMessageNotFound)

	// Staff removing a staff message reads as a recall.
	staffGone, err := fixture.repo.DeleteMessage(ctx, before.ID, staff.ID)
	require.NoError(t, err)
	require.True(t, staffGone.Recalled)

	// Past the window the message stays; a wait kept open by an older message continues from it.
	first := sendSupportChatText(t, fixture, "first", "recall-first")
	second := sendSupportChatText(t, fixture, "second", "recall-second")
	_, err = integrationDB.ExecContext(ctx, `UPDATE support_chat_messages SET created_at=created_at-INTERVAL '3 minutes' WHERE id=$1`, first.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE support_chats SET waiting_since=waiting_since-INTERVAL '3 minutes' WHERE id=$1`, before.ID)
	require.NoError(t, err)
	waiting := mustGetSupportChat(t, fixture.repo, fixture.user.ID).WaitingSince
	require.NotNil(t, waiting)
	_, err = fixture.repo.RecallMessage(ctx, before.ID, first.ID, service.SupportChatRecallWindow)
	require.ErrorIs(t, err, service.ErrSupportChatRecallExpired)
	_, err = fixture.repo.RecallMessage(ctx, before.ID, second.ID, service.SupportChatRecallWindow)
	require.NoError(t, err)
	stillWaiting := mustGetSupportChat(t, fixture.repo, fixture.user.ID).WaitingSince
	require.NotNil(t, stillWaiting)
	require.True(t, waiting.Equal(*stillWaiting))

	// created_at is transaction-start time, so a later message (by id) can carry an
	// earlier timestamp than the one that started the wait. Recalling the starter
	// must not end the wait while that message is unanswered.
	sendSupportChatStaffText(t, fixture, "answer both", "recall-answer")
	starter := sendSupportChatText(t, fixture, "starter", "recall-starter")
	raced := sendSupportChatText(t, fixture, "raced", "recall-raced")
	_, err = integrationDB.ExecContext(ctx, `UPDATE support_chat_messages SET created_at=(SELECT created_at FROM support_chat_messages WHERE id=$1)-INTERVAL '1 second' WHERE id=$2`, starter.ID, raced.ID)
	require.NoError(t, err)
	startedAt := mustGetSupportChat(t, fixture.repo, fixture.user.ID).WaitingSince
	require.NotNil(t, startedAt)
	_, err = fixture.repo.RecallMessage(ctx, before.ID, starter.ID, service.SupportChatRecallWindow)
	require.NoError(t, err)
	afterRace := mustGetSupportChat(t, fixture.repo, fixture.user.ID).WaitingSince
	require.NotNil(t, afterRace, "the raced message is still unanswered")
	require.True(t, startedAt.Equal(*afterRace))
	_, err = fixture.repo.RecallMessage(ctx, before.ID, raced.ID, service.SupportChatRecallWindow)
	require.NoError(t, err)
	require.Nil(t, mustGetSupportChat(t, fixture.repo, fixture.user.ID).WaitingSince, "nothing unanswered is left")

	// Users see the staff account id but never the staff name.
	visible := sendSupportChatStaffText(t, fixture, "visible reply", "recall-visible")
	items, _, _, err := fixture.repo.ListMessages(ctx, service.SupportChatMessagePageInput{ChatID: before.ID, OwnerID: fixture.user.ID, Limit: 50})
	require.NoError(t, err)
	byID := make(map[int64]service.SupportChatMessage, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	require.Equal(t, fixture.admin.ID, byID[visible.ID].StaffID)
	require.Empty(t, byID[visible.ID].AuthorName)
	require.True(t, byID[staff.ID].Recalled)
	require.Equal(t, fixture.admin.ID, byID[staff.ID].StaffID)
	require.True(t, byID[message.ID].Recalled)
	require.Zero(t, byID[message.ID].StaffID)
	require.False(t, byID[first.ID].Deleted)
}

func testSupportChatImageOwnership(t *testing.T) {
	fixture := newSupportChatFixture(t)
	other := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-other-" + uuid.NewString() + "@example.com"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, other.ID)
	})
	message := sendSupportChatImage(t, fixture, "owner image", "owner-image")
	image, err := fixture.repo.GetImage(context.Background(), message.Image.ID, fixture.user.ID, false)
	require.NoError(t, err)
	require.Equal(t, tinySupportChatPNG(), image.Data)
	_, err = fixture.repo.GetImage(context.Background(), message.Image.ID, other.ID, false)
	require.ErrorIs(t, err, service.ErrSupportChatMessageNotFound)
	image, err = fixture.repo.GetImage(context.Background(), message.Image.ID, 0, true)
	require.NoError(t, err)
	require.Equal(t, tinySupportChatPNG(), image.Data)
}

func testSupportChatListChats(t *testing.T) {
	fixture := newSupportChatFixture(t)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE users SET balance=42.5 WHERE id=$1`, fixture.user.ID)
	require.NoError(t, err)
	sendSupportChatText(t, fixture, "a preview with more than enough words to remain under the eighty rune limit", "list-text")
	textChat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)

	imageUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-image-" + uuid.NewString() + "@example.com", Username: "image-user"})
	deletedUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-deleted-" + uuid.NewString() + "@example.com", Username: "deleted-user"})
	resolvedUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-resolved-" + uuid.NewString() + "@example.com", Username: "resolved-user"})
	openUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-open-" + uuid.NewString() + "@example.com", Username: "open-user"})
	softUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-soft-" + uuid.NewString() + "@example.com", Username: "soft-user"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = ANY($1)`, pq.Array([]int64{imageUser.ID, deletedUser.ID, resolvedUser.ID, openUser.ID, softUser.ID}))
	})
	imageChat := createSupportChatForUser(t, imageUser)
	imageMessage := sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
		ChatID: imageChat.ID, UserID: imageUser.ID, AuthorUserID: imageUser.ID, AuthorRole: service.SupportChatRoleUser,
		ClientMsgID: "list-image", Image: &service.SupportChatImageInput{Data: tinySupportChatPNG(), ContentType: "image/png", Width: 2, Height: 2},
	})
	deletedChat := createSupportChatForUser(t, deletedUser)
	deletedMessage := sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
		ChatID: deletedChat.ID, UserID: deletedUser.ID, AuthorUserID: deletedUser.ID, AuthorRole: service.SupportChatRoleUser,
		ClientMsgID: "list-deleted", Body: "to delete",
	})
	_, err = fixture.repo.DeleteMessage(context.Background(), deletedChat.ID, deletedMessage.ID)
	require.NoError(t, err)
	resolvedChat := createSupportChatForUser(t, resolvedUser)
	resolvedMessage := sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{
		ChatID: resolvedChat.ID, UserID: resolvedUser.ID, AuthorUserID: resolvedUser.ID, AuthorRole: service.SupportChatRoleUser,
		ClientMsgID: "list-resolved", Body: "resolved",
	})
	require.NoError(t, fixture.repo.SetStatus(context.Background(), service.SupportChatStatusInput{ChatID: resolvedChat.ID, Status: service.SupportChatStatusResolved, LastSeenMessageID: resolvedMessage.ID}))
	openChat := createSupportChatForUser(t, openUser)
	sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{ChatID: openChat.ID, UserID: openUser.ID, AuthorUserID: openUser.ID, AuthorRole: service.SupportChatRoleUser, ClientMsgID: "list-open", Body: "open"})
	sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{ChatID: openChat.ID, AuthorUserID: fixture.admin.ID, AuthorRole: service.SupportChatRoleAdmin, ClientMsgID: "list-open-admin", Body: "answered"})
	softDeletedChat := createSupportChatForUser(t, softUser)
	sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{ChatID: softDeletedChat.ID, UserID: softUser.ID, AuthorUserID: softUser.ID, AuthorRole: service.SupportChatRoleUser, ClientMsgID: "list-soft", Body: "soft waiting"})
	_, err = integrationDB.ExecContext(context.Background(), `
		UPDATE support_chats
		SET waiting_since=CASE id
			WHEN $1 THEN NOW()-interval '4 minutes'
			WHEN $2 THEN NOW()-interval '3 minutes'
			WHEN $3 THEN NOW()-interval '2 minutes'
			WHEN $4 THEN NOW()-interval '1 minute'
		END
		WHERE id = ANY($5)`, textChat.ID, imageChat.ID, deletedChat.ID, softDeletedChat.ID, pq.Array([]int64{textChat.ID, imageChat.ID, deletedChat.ID, softDeletedChat.ID}))
	require.NoError(t, err)
	beforeSoftDelete, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE users SET deleted_at=NOW() WHERE id=$1`, softUser.ID)
	require.NoError(t, err)
	afterSoftDelete, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.Equal(t, beforeSoftDelete.ChatWaiting-1, afterSoftDelete.ChatWaiting)

	items, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "all", Page: 1, PageSize: 50})
	require.NoError(t, err)
	byID := make(map[int64]service.SupportChat, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, chatID := range []int64{textChat.ID, imageChat.ID, deletedChat.ID, resolvedChat.ID, openChat.ID, softDeletedChat.ID} {
		require.Contains(t, byID, chatID)
	}
	require.Equal(t, "text", byID[textChat.ID].PreviewKind)
	require.LessOrEqual(t, len([]rune(byID[textChat.ID].Preview)), 80)
	require.Equal(t, "image", byID[imageChat.ID].PreviewKind)
	require.Equal(t, imageMessage.ID, byID[imageChat.ID].LastMessageID)
	require.Equal(t, "deleted", byID[deletedChat.ID].PreviewKind)
	require.Equal(t, fixture.user.Email, byID[textChat.ID].User.Email)
	require.Equal(t, "chat-user", byID[textChat.ID].User.Username)
	require.Equal(t, 42.5, byID[textChat.ID].User.Balance)
	require.False(t, byID[textChat.ID].User.Deleted)
	require.False(t, byID[textChat.ID].User.CreatedAt.IsZero())
	require.True(t, byID[softDeletedChat.ID].User.Deleted)

	waiting, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "waiting", Page: 1, PageSize: 50})
	require.NoError(t, err)
	for index, item := range waiting {
		require.NotEqual(t, softDeletedChat.ID, item.ID)
		require.Equal(t, service.SupportChatStatusOpen, item.Status)
		require.NotNil(t, item.WaitingSince)
		if index > 0 {
			require.False(t, waiting[index-1].WaitingSince.After(*item.WaitingSince))
		}
	}
	open, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "open", Page: 1, PageSize: 50})
	require.NoError(t, err)
	resolved, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "resolved", Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Contains(t, chatIDs(open), textChat.ID)
	require.Contains(t, chatIDs(open), imageChat.ID)
	require.NotContains(t, chatIDs(open), resolvedChat.ID)
	require.Equal(t, []int64{resolvedChat.ID}, chatIDs(resolved))
	byEmail, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "all", Keyword: imageUser.Email, Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Equal(t, []int64{imageChat.ID}, chatIDs(byEmail))
	byUserID, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "all", Keyword: "#" + fmt.Sprint(resolvedUser.ID), Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Equal(t, []int64{resolvedChat.ID}, chatIDs(byUserID))
}

func createSupportChatForUser(t *testing.T, user *service.User) service.SupportChat {
	t.Helper()
	repo := NewSupportChatRepository(integrationDB)
	message := sendSupportChatMessage(t, repo, service.SupportChatSendInput{UserID: user.ID, AuthorUserID: user.ID, AuthorRole: service.SupportChatRoleUser, ClientMsgID: "create-" + uuid.NewString()[:20], Body: "create"})
	chat, err := repo.GetUserChat(context.Background(), user.ID)
	require.NoError(t, err)
	require.Equal(t, message.ID, chat.LastMessageID)
	return *chat
}

func chatIDs(items []service.SupportChat) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func testSupportChatSummary(t *testing.T) {
	fixture := newSupportChatFixture(t)
	previous, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	userMessage := sendSupportChatText(t, fixture, "summary", "summary-user")
	current, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.Greater(t, current.Version, previous.Version)
	require.Greater(t, current.LatestUserMessageID, previous.LatestUserMessageID)
	require.Equal(t, previous.ChatWaiting+1, current.ChatWaiting)
	previous = current
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.SetStatus(context.Background(), service.SupportChatStatusInput{ChatID: chat.ID, Status: service.SupportChatStatusResolved, LastSeenMessageID: userMessage.ID}))
	current, err = fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.Greater(t, current.Version, previous.Version)
	require.Zero(t, current.ChatWaiting)
	previous = current
	require.NoError(t, fixture.repo.SetBlocked(context.Background(), service.SupportChatBlockInput{ChatID: chat.ID, Blocked: true, Reason: "moderation"}))
	current, err = fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.Greater(t, current.Version, previous.Version)
	previous = current
	_, err = fixture.repo.MarkRead(context.Background(), chat.ID, true, chat.LastMessageID)
	require.NoError(t, err)
	current, err = fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.Greater(t, current.Version, previous.Version)
}

func testSupportChatDeletes(t *testing.T) {
	fixture := newSupportChatFixture(t)
	message := sendSupportChatImage(t, fixture, "delete me", "delete-chat-image")
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.DeleteChat(context.Background(), chat.ID))
	var rows int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_messages WHERE chat_id=$1`, chat.ID).Scan(&rows))
	require.Zero(t, rows)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_images WHERE chat_id=$1`, chat.ID).Scan(&rows))
	require.Zero(t, rows)
	require.NotZero(t, message.Image.ID)
	blockedUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-blocked-delete-" + uuid.NewString() + "@example.com"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, blockedUser.ID)
	})
	blockedFixture := &supportChatFixture{repo: fixture.repo, user: blockedUser, admin: fixture.admin}
	blockedMessage := sendSupportChatImage(t, blockedFixture, "blocked content", "blocked-delete-image")
	blockedChat, err := fixture.repo.GetUserChat(context.Background(), blockedUser.ID)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.SetBlocked(context.Background(), service.SupportChatBlockInput{ChatID: blockedChat.ID, Blocked: true, Reason: "abuse"}))
	require.NoError(t, fixture.repo.DeleteChat(context.Background(), blockedChat.ID))
	_, _, err = fixture.repo.SendMessage(context.Background(), service.SupportChatSendInput{UserID: blockedUser.ID, AuthorUserID: blockedUser.ID, AuthorRole: service.SupportChatRoleUser, ClientMsgID: "blocked-after-delete", Body: "blocked"})
	require.ErrorIs(t, err, service.ErrSupportChatBlocked)
	placeholder, err := fixture.repo.GetUserChat(context.Background(), blockedUser.ID)
	require.NoError(t, err)
	require.Equal(t, service.SupportChatStatusResolved, placeholder.Status)
	require.Zero(t, placeholder.LastMessageID)
	require.True(t, placeholder.Blocked)
	adminDetail, err := fixture.repo.GetChat(context.Background(), placeholder.ID, true)
	require.NoError(t, err)
	require.Equal(t, "none", adminDetail.PreviewKind)
	adminMessages, hasMore, _, err := fixture.repo.ListMessages(context.Background(), service.SupportChatMessagePageInput{ChatID: placeholder.ID, Admin: true, Limit: 30})
	require.NoError(t, err)
	require.Empty(t, adminMessages)
	require.False(t, hasMore)
	readCursor, err := fixture.repo.MarkRead(context.Background(), placeholder.ID, true, 999)
	require.NoError(t, err)
	require.Zero(t, readCursor)
	allChats, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "all", Keyword: blockedUser.Email, Page: 1, PageSize: 30})
	require.NoError(t, err)
	require.Len(t, allChats, 1)
	require.Equal(t, placeholder.ID, allChats[0].ID)
	require.Equal(t, "none", allChats[0].PreviewKind)
	waitingChats, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "waiting", Keyword: blockedUser.Email, Page: 1, PageSize: 30})
	require.NoError(t, err)
	require.Empty(t, waitingChats)
	resolvedChats, _, err := fixture.repo.ListChats(context.Background(), service.SupportChatListFilter{Filter: "resolved", Keyword: blockedUser.Email, Page: 1, PageSize: 30})
	require.NoError(t, err)
	require.Len(t, resolvedChats, 1)
	require.Equal(t, placeholder.ID, resolvedChats[0].ID)
	summaryBefore, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.NoError(t, fixture.repo.SetStatus(context.Background(), service.SupportChatStatusInput{ChatID: placeholder.ID, Status: service.SupportChatStatusOpen, LastSeenMessageID: 0}))
	require.NoError(t, fixture.repo.SetStatus(context.Background(), service.SupportChatStatusInput{ChatID: placeholder.ID, Status: service.SupportChatStatusResolved, LastSeenMessageID: 0}))
	summaryAfter, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.Equal(t, summaryBefore.ChatWaiting, summaryAfter.ChatWaiting)
	var oldRows int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_messages WHERE id=$1`, blockedMessage.ID).Scan(&oldRows))
	require.Zero(t, oldRows)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_images WHERE chat_id=$1`, blockedChat.ID).Scan(&oldRows))
	require.Zero(t, oldRows)
	// Deleting a blocked placeholder again, even an untouched one, moves the summary version.
	require.NoError(t, fixture.repo.DeleteChat(context.Background(), placeholder.ID))
	placeholder = mustGetSupportChat(t, fixture.repo, blockedUser.ID)
	require.True(t, placeholder.Blocked)
	versionBefore, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.NoError(t, fixture.repo.DeleteChat(context.Background(), placeholder.ID))
	placeholder = mustGetSupportChat(t, fixture.repo, blockedUser.ID)
	require.True(t, placeholder.Blocked)
	versionAfter, err := fixture.repo.Summary(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, versionBefore.Version, versionAfter.Version)
	require.NoError(t, fixture.repo.SetBlocked(context.Background(), service.SupportChatBlockInput{ChatID: placeholder.ID, Blocked: false}))
	accepted := sendSupportChatMessage(t, fixture.repo, service.SupportChatSendInput{UserID: blockedUser.ID, AuthorUserID: blockedUser.ID, AuthorRole: service.SupportChatRoleUser, ClientMsgID: "unblocked-after-delete", Body: "accepted"})
	require.Equal(t, placeholder.ID, mustGetSupportChat(t, fixture.repo, blockedUser.ID).ID)
	require.Equal(t, placeholder.ID, accepted.ChatID)
	require.NotZero(t, accepted.ID)
	noRowUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-unblocked-delete-" + uuid.NewString() + "@example.com"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, noRowUser.ID)
	})
	createSupportChatForUser(t, noRowUser)
	noRowChat, err := fixture.repo.GetUserChat(context.Background(), noRowUser.ID)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.DeleteChat(context.Background(), noRowChat.ID))
	_, err = fixture.repo.GetUserChat(context.Background(), noRowUser.ID)
	require.ErrorIs(t, err, service.ErrSupportChatNotFound)

	rawUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-raw-" + uuid.NewString() + "@example.com"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, rawUser.ID)
	})
	rawFixture := &supportChatFixture{repo: fixture.repo, user: rawUser, admin: fixture.admin}
	rawMessage := sendSupportChatImage(t, rawFixture, "raw delete", "raw-delete")
	rawChat, err := fixture.repo.GetUserChat(context.Background(), rawUser.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, rawUser.ID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_messages WHERE id=$1`, rawMessage.ID).Scan(&rows))
	require.Zero(t, rows)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM support_chat_images WHERE chat_id=$1`, rawChat.ID).Scan(&rows))
	require.Zero(t, rows)
}

func mustGetSupportChat(t *testing.T, repo service.SupportChatRepository, userID int64) *service.SupportChat {
	t.Helper()
	chat, err := repo.GetUserChat(context.Background(), userID)
	require.NoError(t, err)
	return chat
}

func testSupportChatPurge(t *testing.T) {
	fixture := newSupportChatFixture(t)
	sendSupportChatText(t, fixture, "purge anchor", "purge-anchor")
	chat, err := fixture.repo.GetUserChat(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(context.Background(), `
		INSERT INTO support_chat_images(chat_id,author_role,content_type,byte_size,width,height,data,created_at)
		SELECT $1,'user','image/png',1,1,1,decode('00','hex'),NOW()-interval '8 days' FROM generate_series(1,1200)`, chat.ID)
	require.NoError(t, err)
	var recentID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		INSERT INTO support_chat_images(chat_id,author_role,content_type,byte_size,width,height,data)
		VALUES($1,'user','image/png',1,1,1,decode('00','hex')) RETURNING id`, chat.ID).Scan(&recentID))
	softUser := mustCreateUser(t, testEntClient(t), &service.User{Email: "support-chat-purge-soft-" + uuid.NewString() + "@example.com"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, softUser.ID)
	})
	softFixture := &supportChatFixture{repo: fixture.repo, user: softUser, admin: fixture.admin}
	sendSupportChatText(t, softFixture, "soft", "purge-soft-anchor")
	softChat, err := fixture.repo.GetUserChat(context.Background(), softUser.ID)
	require.NoError(t, err)
	var softID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		INSERT INTO support_chat_images(chat_id,author_role,content_type,byte_size,width,height,data)
		VALUES($1,'user','image/png',1,1,1,decode('00','hex')) RETURNING id`, softChat.ID).Scan(&softID))
	_, err = integrationDB.ExecContext(context.Background(), `UPDATE users SET deleted_at=NOW() WHERE id=$1`, softUser.ID)
	require.NoError(t, err)
	rows, bytesFreed, err := fixture.repo.PurgeImages(context.Background(), 7, time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(1201), rows)
	require.Equal(t, int64(1201), bytesFreed)
	var purgedAt interface{}
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT purged_at FROM support_chat_images WHERE id=$1`, recentID).Scan(&purgedAt))
	require.Nil(t, purgedAt)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT purged_at FROM support_chat_images WHERE id=$1`, softID).Scan(&purgedAt))
	require.NotNil(t, purgedAt)
}
