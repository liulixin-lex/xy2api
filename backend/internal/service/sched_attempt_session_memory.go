package service

import (
	"container/list"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedAttemptSessionKey struct {
	groupID int64
	session string
}

type schedAttemptSession struct {
	key          schedAttemptSessionKey
	accountID    int64
	model        string
	promptTokens *int
	finishedAt   time.Time
	touchedAt    time.Time
}

type schedAttemptSessionMemory struct {
	mu      sync.Mutex
	entries map[schedAttemptSessionKey]*list.Element
	lru     list.List
}

func newSchedAttemptSessionMemory() *schedAttemptSessionMemory {
	return &schedAttemptSessionMemory{entries: make(map[schedAttemptSessionKey]*list.Element)}
}

func (m *schedAttemptSessionMemory) expire(now time.Time) {
	for e := m.lru.Back(); e != nil; e = m.lru.Back() {
		entry, _ := e.Value.(schedAttemptSession)
		if now.Sub(entry.touchedAt) < schedquality.SessionMemoryTTL {
			return
		}
		delete(m.entries, entry.key)
		m.lru.Remove(e)
	}
}

func (m *schedAttemptSessionMemory) get(key schedAttemptSessionKey, now time.Time) (schedAttemptSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expire(now)
	e, ok := m.entries[key]
	if !ok {
		return schedAttemptSession{}, false
	}
	v, _ := e.Value.(schedAttemptSession)
	v.touchedAt = now
	e.Value = v
	m.lru.MoveToFront(e)
	return v, true
}

func (m *schedAttemptSessionMemory) put(v schedAttemptSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expire(v.finishedAt)
	v.touchedAt = v.finishedAt
	if e, ok := m.entries[v.key]; ok {
		entry, _ := e.Value.(schedAttemptSession)
		if entry.finishedAt.After(v.finishedAt) {
			return
		}
		e.Value = v
		m.lru.MoveToFront(e)
		return
	}
	m.entries[v.key] = m.lru.PushFront(v)
	if len(m.entries) > schedquality.SessionMemoryMaxEntries {
		e := m.lru.Back()
		entry, _ := e.Value.(schedAttemptSession)
		delete(m.entries, entry.key)
		m.lru.Remove(e)
	}
}
