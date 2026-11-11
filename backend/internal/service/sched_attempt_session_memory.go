package service

import (
	"container/list"
	"sync"
	"time"
)

type schedAttemptSessionKey struct {
	groupID int64
	session string
}

type schedAttemptSession struct {
	key          schedAttemptSessionKey
	accountID    int64
	model        string
	promptTokens int
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
		if now.Sub(e.Value.(schedAttemptSession).touchedAt) < time.Hour {
			return
		}
		delete(m.entries, e.Value.(schedAttemptSession).key)
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
	v := e.Value.(schedAttemptSession)
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
		if e.Value.(schedAttemptSession).finishedAt.After(v.finishedAt) {
			return
		}
		e.Value = v
		m.lru.MoveToFront(e)
		return
	}
	m.entries[v.key] = m.lru.PushFront(v)
	if len(m.entries) > 65536 {
		e := m.lru.Back()
		delete(m.entries, e.Value.(schedAttemptSession).key)
		m.lru.Remove(e)
	}
}
