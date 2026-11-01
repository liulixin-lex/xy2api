//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type supportChatTestSettings struct {
	runtime SupportChatRuntime
	err     error
}

func (s supportChatTestSettings) GetSupportChatRuntime(context.Context) (SupportChatRuntime, error) {
	return s.runtime, s.err
}

type supportChatTestRepo struct {
	chat         *SupportChat
	err          error
	calls        int
	recallChat   int64
	recallWindow time.Duration
}

func (r *supportChatTestRepo) GetUserChat(context.Context, int64) (*SupportChat, error) {
	r.calls++
	if r.chat == nil {
		return nil, ErrSupportChatNotFound
	}
	return r.chat, nil
}
func (r *supportChatTestRepo) GetChat(context.Context, int64, bool) (*SupportChat, error) {
	r.calls++
	return r.chat, r.err
}
func (r *supportChatTestRepo) ListChats(context.Context, SupportChatListFilter) ([]SupportChat, int64, error) {
	r.calls++
	return nil, 0, r.err
}
func (r *supportChatTestRepo) ListMessages(context.Context, SupportChatMessagePageInput) ([]SupportChatMessage, bool, *SupportChat, error) {
	r.calls++
	return nil, false, r.chat, r.err
}
func (r *supportChatTestRepo) SendMessage(context.Context, SupportChatSendInput) (*SupportChatMessage, bool, error) {
	r.calls++
	return nil, false, r.err
}
func (r *supportChatTestRepo) MarkRead(context.Context, int64, bool, int64) (int64, error) {
	r.calls++
	return 0, r.err
}
func (r *supportChatTestRepo) SetStatus(context.Context, SupportChatStatusInput) error {
	r.calls++
	return r.err
}
func (r *supportChatTestRepo) SetBlocked(context.Context, SupportChatBlockInput) error {
	r.calls++
	return r.err
}
func (r *supportChatTestRepo) DeleteMessage(context.Context, int64, int64) (*SupportChatMessage, error) {
	r.calls++
	return nil, r.err
}
func (r *supportChatTestRepo) DeleteChat(context.Context, int64) error { r.calls++; return r.err }
func (r *supportChatTestRepo) RecallMessage(_ context.Context, chatID, messageID int64, window time.Duration) (*SupportChatMessage, error) {
	r.calls++
	r.recallChat, r.recallWindow = chatID, window
	return &SupportChatMessage{ID: messageID, AuthorRole: SupportChatRoleUser, Deleted: true, Recalled: true}, r.err
}
func (r *supportChatTestRepo) GetImage(context.Context, int64, int64, bool) (*SupportChatImageResult, error) {
	r.calls++
	return nil, r.err
}
func (r *supportChatTestRepo) Summary(context.Context) (SupportChatSummary, error) {
	r.calls++
	return SupportChatSummary{}, r.err
}
func (r *supportChatTestRepo) UnreadCount(context.Context, int64, int64) (int64, error) {
	r.calls++
	return 0, r.err
}
func (r *supportChatTestRepo) PurgeImages(context.Context, int, time.Time) (int64, int64, error) {
	r.calls++
	return 0, 0, r.err
}

func disabledChatService() (*SupportChatService, *supportChatTestRepo) {
	repo := &supportChatTestRepo{}
	return NewSupportChatServiceWithSettings(repo, supportChatTestSettings{runtime: SupportChatRuntime{}}, nil), repo
}

func TestSupportChatDisabledEverywhere(t *testing.T) {
	ctx := context.Background()
	svc, repo := disabledChatService()
	_, err := svc.UserSummary(ctx, 1)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.Messages(ctx, SupportChatMessagePageInput{OwnerID: 1})
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, _, err = svc.Send(ctx, SupportChatSendInput{UserID: 1, AuthorRole: SupportChatRoleUser, Body: "x", ClientMsgID: "client-01"})
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.Read(ctx, 1, false, 1)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.ReadAsUser(ctx, 1, 1)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.Image(ctx, 1, 1, false)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.AdminSummary(ctx)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, _, err = svc.AdminList(ctx, SupportChatListFilter{})
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.AdminGet(ctx, 1)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.AdminStatus(ctx, SupportChatStatusInput{ChatID: 1, Status: SupportChatStatusOpen})
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.AdminBlock(ctx, SupportChatBlockInput{ChatID: 1})
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.AdminDeleteMessage(ctx, 1, 1)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	_, err = svc.Recall(ctx, 1, 1)
	require.ErrorIs(t, err, ErrSupportChatDisabled)
	require.ErrorIs(t, svc.AdminDeleteChat(ctx, 1), ErrSupportChatDisabled)
	require.Zero(t, repo.calls)
}

func TestSupportChatRecallUsesTheCallersChat(t *testing.T) {
	ctx := context.Background()
	repo := &supportChatTestRepo{}
	svc := NewSupportChatServiceWithSettings(repo, supportChatTestSettings{runtime: SupportChatRuntime{Enabled: true}}, nil)
	_, err := svc.Recall(ctx, 42, 5)
	require.ErrorIs(t, err, ErrSupportChatMessageNotFound, "no chat yet means nothing to recall")
	repo.chat = &SupportChat{ID: 9, UserID: 42}
	m, err := svc.Recall(ctx, 42, 5)
	require.NoError(t, err)
	require.True(t, m.Recalled)
	require.Equal(t, int64(9), repo.recallChat)
	require.Equal(t, 2*time.Minute, repo.recallWindow)
}

func TestSupportChatValidationAndMetadata(t *testing.T) {
	ctx := context.Background()
	repo := &supportChatTestRepo{}
	svc := NewSupportChatServiceWithSettings(repo, supportChatTestSettings{runtime: SupportChatRuntime{Enabled: true}}, nil)
	_, _, err := svc.Send(ctx, SupportChatSendInput{AuthorRole: SupportChatRoleUser, Body: "", ClientMsgID: "client-01"})
	require.ErrorIs(t, err, ErrSupportChatBodyRequired)
	_, _, err = svc.Send(ctx, SupportChatSendInput{AuthorRole: SupportChatRoleUser, Body: "ok", ClientMsgID: "bad"})
	require.ErrorIs(t, err, ErrSupportChatClientIDInvalid)
	require.Equal(t, "20", chatLimitError(ErrSupportChatTooFast).(*infraerrors.ApplicationError).Metadata["max"])
}

func TestSupportChatCursorViewerMapping(t *testing.T) {
	chat := &SupportChat{ID: 1, UserReadMessageID: 7, StaffReadMessageID: 9}
	require.Equal(t, int64(7), mapChatViewer(chat, false).ReadMessageID)
	require.Zero(t, chat.PeerReadMessageID, "users never learn how far staff have read")
	require.Equal(t, int64(9), mapChatViewer(chat, true).ReadMessageID)
	require.Equal(t, int64(7), chat.PeerReadMessageID)
}

func TestSupportChatImageValidationTable(t *testing.T) {
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	var jpegBytes bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil))
	for _, data := range [][]byte{pngBytes.Bytes(), jpegBytes.Bytes()} {
		validated, err := ValidateSupportChatImage(data)
		require.NoError(t, err)
		require.NotEmpty(t, validated.ContentType)
	}
	webp := make([]byte, 30)
	copy(webp[0:4], "RIFF")
	copy(webp[8:12], "WEBP")
	copy(webp[12:16], "VP8X")
	webp[24], webp[27] = 1, 1
	validated, err := ValidateSupportChatImage(webp)
	require.NoError(t, err)
	require.Equal(t, "image/webp", validated.ContentType)
	for _, tc := range []struct {
		name string
		data []byte
		code string
	}{
		{"garbage", []byte("not an image"), "SUPPORT_CHAT_IMAGE_INVALID"},
		{"gif", []byte("GIF89a123456"), "SUPPORT_CHAT_IMAGE_INVALID"},
		{"truncated png", []byte("\x89PNG\r\n\x1a\n1234"), "SUPPORT_CHAT_IMAGE_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateSupportChatImage(tc.data)
			require.Equal(t, tc.code, infraerrors.Reason(err))
		})
	}
	tooLarge := bytes.Repeat([]byte{'x'}, SupportChatImageMaxBytes+1)
	_, err = ValidateSupportChatImage(tooLarge)
	require.ErrorIs(t, err, ErrSupportChatImageTooLarge)
	for _, testCase := range []struct {
		name          string
		width, height int
	}{
		{name: "side exceeds cap", width: SupportChatImageMaxSide + 1, height: 1},
		{name: "pixel count exceeds cap", width: 4096, height: 4097},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			data := append([]byte(nil), pngBytes.Bytes()...)
			binary.BigEndian.PutUint32(data[16:20], uint32(testCase.width))
			binary.BigEndian.PutUint32(data[20:24], uint32(testCase.height))
			binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
			_, err := ValidateSupportChatImage(data)
			require.ErrorIs(t, err, ErrSupportChatImageTooLarge)
		})
	}
}

type blockingSupportChatPurgeRepo struct {
	*supportChatTestRepo
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (r *blockingSupportChatPurgeRepo) PurgeImages(ctx context.Context, _ int, _ time.Time) (int64, int64, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	close(r.canceled)
	// lib/pq reports an in-flight cancel as its own error, not context.Canceled,
	// after earlier batches already committed.
	return 500, 50000, errors.New("pq: canceling statement due to user request")
}

func TestSupportChatImagePurgeStopCancelsInFlightRun(t *testing.T) {
	var logs bytes.Buffer
	var logsMu sync.Mutex
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logsMu}, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	repo := &blockingSupportChatPurgeRepo{
		supportChatTestRepo: &supportChatTestRepo{},
		started:             make(chan struct{}),
		canceled:            make(chan struct{}),
	}
	service := NewSupportChatImagePurgeService(repo, nil, nil, nil)
	service.settings = supportChatTestSettings{runtime: SupportChatRuntime{Enabled: true, Config: SupportTicketConfig{ChatImageRetentionDays: 90}}}
	service.initialDelay = time.Millisecond
	service.Start()
	t.Cleanup(service.Stop)
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("purge did not start")
	}
	stopped := make(chan struct{})
	go func() {
		service.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the in-flight purge")
	}
	select {
	case <-repo.canceled:
	default:
		t.Fatal("purge repository did not observe cancellation")
	}
	logsMu.Lock()
	out := logs.String()
	logsMu.Unlock()
	require.Contains(t, out, "support chat image purge interrupted by shutdown")
	require.Contains(t, out, "rows=500")
	require.NotContains(t, out, "level=WARN")
}

var _ = errors.Is

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
