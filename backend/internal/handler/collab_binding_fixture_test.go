//go:build unit

package handler

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

// Match the production cache's conditional response publication contract.
func (m *replayMemory) CreateResponseBinding(_ context.Context, _ int64, key string, id int64, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindings[key] != 0 || time.Now().Before(m.responseGenerations[key]) {
		return nil
	}
	if m.responseGenerations == nil {
		m.responseGenerations = map[string]time.Time{}
	}
	m.bindings[key] = id
	m.responseGenerations[key] = time.Now().Add(max(ttl, service.CollabBindingPublicationLifetime))
	return nil
}
func (m *replayMemory) DeleteResponseBinding(_ context.Context, _ int64, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bindings, key)
	if m.responseGenerations == nil {
		m.responseGenerations = map[string]time.Time{}
	}
	m.responseGenerations[key] = time.Now().Add(service.CollabBindingPublicationLifetime)
	m.effects = append(m.effects, "unbind:"+replayDigest([]byte(key)))
	return nil
}
func (m *replayMemory) SetResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	m.mu.Lock()
	if m.responseGenerations == nil {
		m.responseGenerations = map[string]time.Time{}
	}
	m.responseGenerations[key] = time.Now().Add(max(ttl, service.CollabBindingPublicationLifetime))
	m.mu.Unlock()
	return m.SetSessionAccountID(ctx, group, key, id, ttl)
}
