package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

type SupportChatImagePurgeService struct {
	repo     SupportChatRepository
	settings supportChatSettings
	leader   LeaderLockCache
	db       *sql.DB
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func NewSupportChatImagePurgeService(repo SupportChatRepository, settings *SettingService, leader LeaderLockCache, db *sql.DB) *SupportChatImagePurgeService {
	return &SupportChatImagePurgeService{repo: repo, settings: settings, leader: leader, db: db}
}

func (s *SupportChatImagePurgeService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.once.Do(func() { s.stop = make(chan struct{}); s.done = make(chan struct{}); go s.run() })
}
func (s *SupportChatImagePurgeService) Stop() {
	if s == nil {
		return
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
func (s *SupportChatImagePurgeService) run() {
	defer close(s.done)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	initial := time.NewTimer(time.Minute)
	defer initial.Stop()
	for {
		select {
		case <-initial.C:
			s.runOnce()
		case <-t.C:
			s.runOnce()
		case <-s.stop:
			return
		}
	}
}
func (s *SupportChatImagePurgeService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if s.settings == nil {
		return
	}
	runtime, err := s.settings.GetSupportChatRuntime(ctx)
	if err != nil {
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
		slog.Warn("support chat image purge failed", "error", err)
		return
	}
	slog.Info("support chat image purge", "rows", rows, "bytes_freed", bytes)
}

func ProvideSupportChatImagePurgeService(repo SupportChatRepository, settings *SettingService, leader LeaderLockCache, db *sql.DB) *SupportChatImagePurgeService {
	s := NewSupportChatImagePurgeService(repo, settings, leader, db)
	s.Start()
	return s
}
