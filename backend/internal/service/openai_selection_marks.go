package service

import (
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type fullMarks struct {
	mu sync.Mutex
	at map[int64]time.Time
}

func (m *fullMarks) Mark(accountID int64, at time.Time) {
	if m == nil || accountID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at == nil {
		m.at = make(map[int64]time.Time)
	}
	if at.After(m.at[accountID]) {
		m.at[accountID] = at
	}
}

func (m *fullMarks) last(accountID int64) time.Time {
	if m == nil {
		return time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at[accountID]
}

func (m *fullMarks) Recently(accountID int64, now time.Time) bool {
	at := m.last(accountID)
	return !at.IsZero() && now.Sub(at) < schedquality.TakeoverFullCooloff
}

func (m *fullMarks) observe(accounts []*Account, loads map[int64]*AccountLoadInfo, now time.Time) {
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if load := loads[account.ID]; load != nil && account.Concurrency > 0 && load.CurrentConcurrency >= account.Concurrency {
			m.Mark(account.ID, now)
		}
	}
}
