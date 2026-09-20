package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	apperrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const SettingKeyCodexTicketProxyPool = "openai_codex_ticket_proxy_pool_v1"
const codexProxyHealthKey = "openai_codex_ticket_proxy_health_v1"

type codexSettingCAS interface {
	CompareAndSwapCodexSetting(context.Context, string, string, string) (bool, error)
}

type CodexProxyProbeResult struct {
	Success     bool      `json:"success"`
	CheckedAt   time.Time `json:"checked_at"`
	LatencyMS   int64     `json:"latency_ms"`
	IPAddress   string    `json:"ip_address,omitempty"`
	Country     string    `json:"country,omitempty"`
	CountryCode string    `json:"country_code,omitempty"`
	Message     string    `json:"message,omitempty"`
}
type CodexHarvestProxy struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	Enabled         bool                   `json:"enabled"`
	Revision        string                 `json:"revision"`
	Display         string                 `json:"display"`
	URL             string                 `json:"-"`
	Probe           *CodexProxyProbeResult `json:"probe,omitempty"`
	LastSuccess     *CodexProxyProbeResult `json:"last_success,omitempty"`
	LastAcquisition string                 `json:"last_acquisition,omitempty"`
	NetworkFailures int                    `json:"-"`
	RetryAt         time.Time              `json:"-"`
}
type CodexHarvestProxyPool struct {
	Revision string              `json:"revision"`
	Entries  []CodexHarvestProxy `json:"entries"`
}
type CodexHarvestProxyInput struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Enabled bool    `json:"enabled"`
	URL     *string `json:"url,omitempty"`
}
type CodexHarvestProxyPoolUpdate struct {
	ExpectedRevision string                   `json:"expected_revision"`
	Entries          []CodexHarvestProxyInput `json:"entries"`
}
type storedCodexProxy struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Revision string `json:"revision"`
	Secret   string `json:"secret"`
}
type storedCodexProxyPool struct {
	Revision string             `json:"revision"`
	Entries  []storedCodexProxy `json:"entries"`
}
type codexProxyHealth struct {
	Revision        string                 `json:"revision"`
	Probe           *CodexProxyProbeResult `json:"probe,omitempty"`
	LastSuccess     *CodexProxyProbeResult `json:"last_success,omitempty"`
	LastAcquisition string                 `json:"last_acquisition,omitempty"`
	NetworkFailures int                    `json:"network_failures"`
	RetryAt         time.Time              `json:"retry_at"`
}

func (s *SettingService) codexPoolRaw(ctx context.Context) (string, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCodexTicketProxyPool)
	if err == nil {
		return raw, nil
	}
	if !errors.Is(err, ErrSettingNotFound) {
		return "", err
	}
	return "", nil
}
func (s *SettingService) codexPoolCAS(ctx context.Context, key, old, next string) error {
	repo, ok := s.settingRepo.(codexSettingCAS)
	if !ok {
		return apperrors.New(503, "CODEX_PROXY_STORAGE", "Atomic proxy settings storage unavailable")
	}
	changed, err := repo.CompareAndSwapCodexSetting(ctx, key, old, next)
	if err != nil {
		return err
	}
	if !changed {
		return ErrCodexTicketConflict
	}
	return nil
}

// Migration commits only encrypted values and is serialized with legacy writers.
func (s *SettingService) ensureCodexProxyPool(ctx context.Context) (string, error) {
	raw, err := s.codexPoolRaw(ctx)
	if err != nil || raw != "" {
		return raw, err
	}
	if s.codexProxyEncryptor == nil {
		return "", apperrors.New(503, "CODEX_PROXY_ENCRYPTION", "Proxy encryption unavailable")
	}
	legacy, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexTicketHarvestProxyURL)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return "", err
	}
	observedLegacy := legacy
	if strings.TrimSpace(legacy) == "" && s.cfg != nil {
		legacy = s.cfg.Gateway.OpenAICodexTicket.HarvestProxyURL
	}
	pool := storedCodexProxyPool{Revision: uuid.NewString(), Entries: []storedCodexProxy{}}
	if strings.TrimSpace(legacy) != "" {
		if ValidateOpenAICodexTicketHarvestProxyURL(legacy) != nil {
			return "", apperrors.BadRequest("CODEX_PROXY_URL", "Invalid acquisition proxy URL")
		}
		encrypted, e := s.codexProxyEncryptor.Encrypt(strings.TrimSpace(legacy))
		if e != nil {
			return "", e
		}
		pool.Entries = append(pool.Entries, storedCodexProxy{ID: uuid.NewString(), Name: "Default", Enabled: true, Revision: uuid.NewString(), Secret: encrypted})
	}
	data, _ := json.Marshal(pool)
	migrationCtx := ContextWithCodexSettingExpectations(ctx, map[string]string{SettingKeyOpenAICodexTicketHarvestProxyURL: observedLegacy})
	err = s.codexPoolCAS(migrationCtx, SettingKeyCodexTicketProxyPool, "", string(data))
	if errors.Is(err, ErrCodexTicketConflict) {
		current, readErr := s.codexPoolRaw(ctx)
		if readErr != nil {
			return "", readErr
		}
		if current == "" {
			return "", ErrCodexTicketConflict
		}
		return current, nil
	}
	return string(data), err
}
func (s *SettingService) GetCodexHarvestProxyPool(ctx context.Context) (*CodexHarvestProxyPool, error) {
	raw, err := s.ensureCodexProxyPool(ctx)
	if err != nil {
		return nil, err
	}
	var stored storedCodexProxyPool
	if json.Unmarshal([]byte(raw), &stored) != nil || stored.Revision == "" {
		return nil, apperrors.New(503, "CODEX_PROXY_DATA", "Proxy pool unavailable")
	}
	health := map[string]codexProxyHealth{}
	if h, e := s.settingRepo.GetValue(ctx, codexProxyHealthKey); e == nil {
		_ = json.Unmarshal([]byte(h), &health)
	}
	pool := &CodexHarvestProxyPool{Revision: stored.Revision, Entries: []CodexHarvestProxy{}}
	for _, entry := range stored.Entries {
		rawURL, e := s.codexProxyEncryptor.Decrypt(entry.Secret)
		if e != nil {
			return nil, apperrors.New(503, "CODEX_PROXY_DECRYPT", "Proxy credentials unavailable")
		}
		u, e := url.Parse(strings.ReplaceAll(rawURL, "{sid}", "%7Bsid%7D"))
		if e != nil {
			return nil, apperrors.New(503, "CODEX_PROXY_DATA", "Proxy pool unavailable")
		}
		out := CodexHarvestProxy{ID: entry.ID, Name: entry.Name, Enabled: entry.Enabled, Revision: entry.Revision, Display: u.Scheme + "://" + u.Host, URL: rawURL}
		if h, ok := health[entry.ID]; ok && h.Revision == entry.Revision {
			out.Probe = h.Probe
			out.LastSuccess = h.LastSuccess
			out.LastAcquisition = h.LastAcquisition
			out.NetworkFailures = h.NetworkFailures
			out.RetryAt = h.RetryAt
		}
		pool.Entries = append(pool.Entries, out)
	}
	s.codexProxySnapshot.Store(pool)
	return pool, nil
}
func (s *SettingService) SaveCodexHarvestProxyPool(ctx context.Context, input CodexHarvestProxyPoolUpdate) (*CodexHarvestProxyPool, error) {
	if len(input.Entries) > 20 || input.Entries == nil {
		return nil, apperrors.BadRequest("CODEX_PROXY_LIMIT", "Provide a proxy list with at most 20 entries")
	}
	raw, err := s.ensureCodexProxyPool(ctx)
	if err != nil {
		return nil, err
	}
	var old storedCodexProxyPool
	if json.Unmarshal([]byte(raw), &old) != nil {
		return nil, errors.New("invalid proxy pool")
	}
	if input.ExpectedRevision == "" || old.Revision != input.ExpectedRevision {
		return nil, ErrCodexTicketConflict
	}
	byID := map[string]storedCodexProxy{}
	for _, entry := range old.Entries {
		byID[entry.ID] = entry
	}
	next := storedCodexProxyPool{Revision: uuid.NewString(), Entries: []storedCodexProxy{}}
	seen := map[string]bool{}
	for _, entry := range input.Entries {
		id := entry.ID
		if id == "" {
			id = uuid.NewString()
		}
		if _, e := uuid.Parse(id); e != nil || seen[id] {
			return nil, apperrors.BadRequest("CODEX_PROXY_ID", "Invalid or duplicate proxy ID")
		}
		seen[id] = true
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = "Proxy"
		}
		if len([]rune(name)) > 80 {
			return nil, apperrors.BadRequest("CODEX_PROXY_NAME", "Proxy name is too long")
		}
		saved, exists := byID[id]
		changed := !exists || saved.Enabled != entry.Enabled
		if entry.URL != nil && strings.TrimSpace(*entry.URL) != "" {
			rawURL := strings.TrimSpace(*entry.URL)
			if len(rawURL) > 8192 || ValidateOpenAICodexTicketHarvestProxyURL(rawURL) != nil {
				return nil, apperrors.BadRequest("CODEX_PROXY_URL", "Invalid acquisition proxy URL")
			}
			previous := ""
			if exists {
				previous, err = s.codexProxyEncryptor.Decrypt(saved.Secret)
				if err != nil {
					return nil, err
				}
			}
			if previous != rawURL {
				saved.Secret, err = s.codexProxyEncryptor.Encrypt(rawURL)
				if err != nil {
					return nil, err
				}
				changed = true
			}
		}
		if saved.Secret == "" {
			return nil, apperrors.BadRequest("CODEX_PROXY_URL", "New proxy requires a connection URL")
		}
		saved.ID = id
		saved.Name = name
		saved.Enabled = entry.Enabled
		if changed {
			saved.Revision = uuid.NewString()
		}
		next.Entries = append(next.Entries, saved)
	}
	encoded, _ := json.Marshal(next)
	if err = s.codexPoolCAS(ctx, SettingKeyCodexTicketProxyPool, raw, string(encoded)); err != nil {
		return nil, err
	}
	s.InvalidateOpenAICodexTicketHarvestProxyCache()
	return s.GetCodexHarvestProxyPool(ctx)
}
func (s *SettingService) updateCodexProxyHealth(ctx context.Context, entry CodexHarvestProxy, fn func(*codexProxyHealth)) error {
	for attempt := 0; attempt < 5; attempt++ {
		pool, err := s.GetCodexHarvestProxyPool(ctx)
		if err != nil {
			return err
		}
		current := false
		for _, e := range pool.Entries {
			if e.ID == entry.ID && e.Revision == entry.Revision {
				current = true
			}
		}
		if !current {
			return ErrCodexTicketConflict
		}
		raw, err := s.settingRepo.GetValue(ctx, codexProxyHealthKey)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return err
		}
		all := map[string]codexProxyHealth{}
		if raw != "" && json.Unmarshal([]byte(raw), &all) != nil {
			return errors.New("invalid proxy health")
		}
		// Keep at most the current pool's health records.
		live := map[string]bool{}
		for _, e := range pool.Entries {
			live[e.ID] = true
		}
		for id := range all {
			if !live[id] {
				delete(all, id)
			}
		}
		h := all[entry.ID]
		if h.Revision != entry.Revision {
			h = codexProxyHealth{Revision: entry.Revision}
		}
		fn(&h)
		all[entry.ID] = h
		data, _ := json.Marshal(all)
		err = s.codexPoolCAS(ctx, codexProxyHealthKey, raw, string(data))
		if errors.Is(err, ErrCodexTicketConflict) {
			continue
		}
		return err
	}
	return ErrCodexTicketConflict
}

// Exact expectations are checked under the same PostgreSQL advisory lock as
// publication and settings writes; encryption stays in the service layer.
type codexSettingExpectationsKey struct{}

func ContextWithCodexSettingExpectations(ctx context.Context, values map[string]string) context.Context {
	return context.WithValue(ctx, codexSettingExpectationsKey{}, values)
}
func CodexSettingExpectations(ctx context.Context) map[string]string {
	values, _ := ctx.Value(codexSettingExpectationsKey{}).(map[string]string)
	return values
}
func (s *SettingService) prepareLegacyCodexProxyUpdate(ctx context.Context, updates map[string]string) (context.Context, error) {
	legacy, present := updates[SettingKeyOpenAICodexTicketHarvestProxyURL]
	// A zero-value scalar is emitted by the legacy whole-settings DTO even when
	// the caller did not edit the proxy. Do not probe the pool for every ordinary
	// settings save; an explicit non-empty legacy address is the compatibility
	// write path. The new pool API handles an explicit empty list separately.
	if !present {
		return ctx, nil
	}
	if strings.TrimSpace(legacy) == "" {
		delete(updates, SettingKeyOpenAICodexTicketHarvestProxyURL)
		return ctx, nil
	}
	raw, err := s.codexPoolRaw(ctx)
	if err != nil || raw == "" {
		return ctx, err
	}
	var pool storedCodexProxyPool
	if json.Unmarshal([]byte(raw), &pool) != nil {
		return ctx, ErrCodexTicketConflict
	}
	if len(pool.Entries) != 1 {
		return ctx, ErrCodexTicketConflict
	}
	if s.codexProxyEncryptor == nil {
		return ctx, apperrors.New(503, "CODEX_PROXY_ENCRYPTION", "Proxy encryption unavailable")
	}
	previous, decryptErr := s.codexProxyEncryptor.Decrypt(pool.Entries[0].Secret)
	if decryptErr != nil {
		return ctx, decryptErr
	}
	if strings.TrimSpace(legacy) == previous {
		delete(updates, SettingKeyOpenAICodexTicketHarvestProxyURL)
		return ctx, nil
	}
	secret, e := s.codexProxyEncryptor.Encrypt(strings.TrimSpace(legacy))
	if e != nil {
		return ctx, e
	}
	pool.Entries[0].Secret = secret
	pool.Entries[0].Revision = uuid.NewString()
	pool.Revision = uuid.NewString()
	next, _ := json.Marshal(pool)
	updates[SettingKeyCodexTicketProxyPool] = string(next)
	delete(updates, SettingKeyOpenAICodexTicketHarvestProxyURL)
	return ContextWithCodexSettingExpectations(ctx, map[string]string{SettingKeyCodexTicketProxyPool: raw}), nil
}
