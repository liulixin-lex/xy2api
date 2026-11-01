package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

type SupportChatImagePurgeService struct {
	repo         SupportChatRepository
	settings     supportChatSettings
	leader       LeaderLockCache
	db           *sql.DB
	cancel       context.CancelFunc
	stop         chan struct{}
	done         chan struct{}
	once         sync.Once
	initialDelay time.Duration
}

func NewSupportChatImagePurgeService(repo SupportChatRepository, settings *SettingService, leader LeaderLockCache, db *sql.DB) *SupportChatImagePurgeService {
	return &SupportChatImagePurgeService{repo: repo, settings: settings, leader: leader, db: db}
}

func (s *SupportChatImagePurgeService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.once.Do(func() {
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.run(ctx)
	})
}
func (s *SupportChatImagePurgeService) Stop() {
	if s == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	select {
	case <-s.stop:
	default:
		if s.stop != nil {
			close(s.stop)
		}
	}
	if s.done != nil {
		<-s.done
	}
}
func (s *SupportChatImagePurgeService) run(ctx context.Context) {
	defer close(s.done)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	initialDelay := s.initialDelay
	if initialDelay <= 0 {
		initialDelay = time.Minute
	}
	initial := time.NewTimer(initialDelay)
	defer initial.Stop()
	for {
		select {
		case <-initial.C:
			s.runOnce(ctx)
		case <-t.C:
			s.runOnce(ctx)
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}
func (s *SupportChatImagePurgeService) runOnce(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	if s.settings == nil {
		return
	}
	runtime, err := s.settings.GetSupportChatRuntime(ctx)
	if err != nil {
		// Decide from the service context: lib/pq reports an in-flight cancel as
		// its own error (57014), which does not wrap context.Canceled.
		if parent.Err() != nil {
			return
		}
		slog.Warn("support chat image purge skipped", "error", err)
		return
	}
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.leader, s.db, "support-chat-image-purge", "support-chat-purge", 10*time.Minute)
	if !ok {
		return
	}
	defer release()
	rows, bytes, err := s.repo.PurgeImages(ctx, runtime.Config.ChatImageRetentionDays, time.Now())
	if err != nil {
		// Batches before the interruption are committed; report them either way.
		if parent.Err() != nil {
			slog.Info("support chat image purge interrupted by shutdown", "rows", rows, "bytes_freed", bytes)
			return
		}
		slog.Warn("support chat image purge failed", "error", err, "rows", rows, "bytes_freed", bytes)
		return
	}
	slog.Info("support chat image purge", "rows", rows, "bytes_freed", bytes)
}

func ProvideSupportChatImagePurgeService(repo SupportChatRepository, settings *SettingService, leader LeaderLockCache, db *sql.DB) *SupportChatImagePurgeService {
	s := NewSupportChatImagePurgeService(repo, settings, leader, db)
	s.Start()
	return s
}
