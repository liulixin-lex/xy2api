//go:build unit

package handler

import (
	"context"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

// Match the production cache's conditional response publication contract.
func (m *replayMemory) CreateResponseBinding(ctx context.Context, _ int64, key string, id int64, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	publication := service.CollabBindingPublicationFrom(ctx)
	if !time.Now().Before(publication.Expires) || publication.Token == "" || publication.Token != m.responseGenerations[key] {
		return nil
	}
	if m.responseGenerations == nil {
		m.responseGenerations = map[string]string{}
	}
	m.bindings[key] = id
	m.responseGenerations[key] = "published:" + publication.Token
	return nil
}
func (m *replayMemory) DeleteResponseBinding(_ context.Context, _ int64, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bindings, key)
	if m.responseGenerations == nil {
		m.responseGenerations = map[string]string{}
	}
	m.responseGenerations[key] = service.NewCollabBindingPublication().Token
	m.effects = append(m.effects, "unbind:"+replayDigest([]byte(key)))
	return nil
}
func (m *replayMemory) SetResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	m.mu.Lock()
	if m.responseGenerations == nil {
		m.responseGenerations = map[string]string{}
	}
	m.responseGenerations[key] = service.NewCollabBindingPublication().Token
	m.mu.Unlock()
	return m.SetSessionAccountID(ctx, group, key, id, ttl)
}

func (m *replayMemory) ReserveResponseBindings(ctx context.Context, _ int64, keys []string, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.responseGenerations == nil {
		m.responseGenerations = map[string]string{}
	}
	publisher, _, _ := strings.Cut(token, ":")
	for _, key := range keys {
		metadata := m.responseGenerations[key]
		if metadata != "" {
			owner, _, _ := strings.Cut(strings.TrimPrefix(metadata, "published:"), ":")
			if owner != publisher {
				return ctx.Err()
			}
		} else if m.bindings[key] != 0 {
			return ctx.Err()
		}
	}
	for _, key := range keys {
		m.responseGenerations[key] = token
	}
	return ctx.Err()
}
