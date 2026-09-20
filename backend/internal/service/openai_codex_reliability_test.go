package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type reliabilitySettings struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
}

func (r *reliabilitySettings) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (r *reliabilitySettings) CompareAndSwapCodexSetting(_ context.Context, key, old, next string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values[key] != old {
		return false, nil
	}
	r.values[key] = next
	return true, nil
}

type reliabilityCipher struct{}

func (reliabilityCipher) Encrypt(raw string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(raw)), nil
}
func (reliabilityCipher) Decrypt(raw string) (string, error) {
	b, e := base64.StdEncoding.DecodeString(raw)
	return string(b), e
}
func reliabilityPoolService(t *testing.T) (*SettingService, *reliabilitySettings) {
	t.Helper()
	repo := &reliabilitySettings{values: map[string]string{SettingKeyOpenAICodexTicketHarvestProxyURL: "http://user:private@example.com:8080"}}
	svc := NewSettingService(repo, &config.Config{})
	svc.codexProxyEncryptor = reliabilityCipher{}
	return svc, repo
}
func TestCodexReliabilityProxyPool(t *testing.T) {
	ctx := context.Background()
	svc, repo := reliabilityPoolService(t)
	pool, err := svc.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	require.Len(t, pool.Entries, 1)
	raw, _ := json.Marshal(pool)
	require.NotContains(t, string(raw), "private")
	require.NotContains(t, string(raw), "user")
	stored, err := repo.GetValue(ctx, SettingKeyCodexTicketProxyPool)
	require.NoError(t, err)
	require.NotContains(t, stored, "private")
	proxyURL := "socks5h://two:secret@second.example:1080"
	updated, err := svc.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: pool.Revision, Entries: []CodexHarvestProxyInput{{ID: pool.Entries[0].ID, Name: "First", Enabled: true}, {ID: uuid.NewString(), Name: "Second", Enabled: true, URL: &proxyURL}}})
	require.NoError(t, err)
	require.Len(t, updated.Entries, 2)
	require.Equal(t, pool.Entries[0].Revision, updated.Entries[0].Revision, "renaming retains connection revision")
	_, err = svc.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: pool.Revision, Entries: []CodexHarvestProxyInput{}})
	require.ErrorIs(t, err, ErrCodexTicketConflict)
	empty, err := svc.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: updated.Revision, Entries: []CodexHarvestProxyInput{}})
	require.NoError(t, err)
	require.Empty(t, empty.Entries)
	fresh := NewSettingService(repo, &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: config.OpenAICodexTicketConfig{HarvestProxyURL: "http://fallback:80"}}})
	fresh.codexProxyEncryptor = reliabilityCipher{}
	gateway := &OpenAIGatewayService{settingService: fresh}
	require.Empty(t, gateway.openAICodexTicketHarvestProxyURLContext(ctx), "empty persisted pool is authoritative after restart")
}
func TestCodexReliabilityProxyTestAndRotation(t *testing.T) {
	ctx := context.Background()
	settings, _ := reliabilityPoolService(t)
	pool, err := settings.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	gateway := ticketTestService(t, config.OpenAICodexTicketConfig{}, &codexTicketFuncUpstream{proxy: func(req *http.Request, proxy string) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Empty(t, req.Header.Get("Authorization"))
		require.True(t, req.Close)
		require.NotEmpty(t, proxy)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ip=2001:db8::1\nloc=CH\n"))}, nil
	}})
	gateway.settingService = settings
	result, err := gateway.TestCodexHarvestProxy(ctx, CodexProxyTestInput{ID: pool.Entries[0].ID, ExpectedRevision: pool.Entries[0].Revision})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "2001:db8::1", result.IPAddress)
	require.Equal(t, "CH", result.CountryCode)
	rt := codexTicketRuntime{}
	entry, _, ok := gateway.selectCodexHarvestProxy(&rt, time.Now())
	require.True(t, ok)
	err = settings.updateCodexProxyHealth(ctx, entry, func(h *codexProxyHealth) { h.RetryAt = time.Now().Add(time.Minute) })
	require.NoError(t, err)
	_, err = settings.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	_, _, ok = gateway.selectCodexHarvestProxy(&rt, time.Now())
	require.False(t, ok)
	_, err = gateway.TestCodexHarvestProxy(ctx, CodexProxyTestInput{ID: entry.ID, ExpectedRevision: "stale"})
	require.ErrorIs(t, err, ErrCodexTicketConflict)
}
func TestCodexReliabilityConfigRevision(t *testing.T) {
	svc, repo := ticketJobService(t, nil)
	ctx := context.Background()
	before, err := svc.GetCodexAccountTicketStatus(ctx, 41)
	require.NoError(t, err)
	_, err = svc.ConfigureCodexAccountTicket(ctx, 41, CodexAccountTicketUpdate{Enabled: false, ExpectedRevision: before.ConfigRevision, RequireRevision: true})
	require.NoError(t, err)
	_, err = svc.ConfigureCodexAccountTicket(ctx, 41, CodexAccountTicketUpdate{Enabled: true, ExpectedRevision: before.ConfigRevision, RequireRevision: true})
	require.ErrorIs(t, err, ErrCodexTicketConflict)
	_, err = svc.ConfigureCodexAccountTicket(ctx, 41, CodexAccountTicketUpdate{Enabled: true, GuardEnable: true})
	require.ErrorIs(t, err, ErrCodexTicketConflict)
	live, err := repo.GetByID(ctx, 41)
	require.NoError(t, err)
	require.False(t, codexAccountTicketConfigOf(live).Enabled)
	current, err := svc.GetCodexAccountTicketStatus(ctx, 41)
	require.NoError(t, err)
	_, err = svc.ConfigureCodexAccountTicket(ctx, 41, CodexAccountTicketUpdate{PreserveEnabled: true, MissingPolicy: "allow_unprotected", ExpectedRevision: current.ConfigRevision, RequireRevision: true})
	require.NoError(t, err)
	live, err = repo.GetByID(ctx, 41)
	require.NoError(t, err)
	require.False(t, codexAccountTicketConfigOf(live).Enabled)
}
func TestCodexReliabilityBudgetPersistsAcrossWorkers(t *testing.T) {
	svc, repo := ticketJobService(t, nil)
	svc.cfg.Gateway.OpenAICodexTicket.AdaptiveSchedulingEnabled = true
	other := ticketTestService(t, svc.cfg.Gateway.OpenAICodexTicket, nil)
	other.accountRepo = repo
	ctx := context.Background()
	model := openAICodexTicketDefaultModel
	for i := 0; i < 16; i++ {
		reservation, err := svc.reserveCodexBudget(ctx, 41, model, 2, "harvest")
		require.NoError(t, err)
		require.NoError(t, other.consumeCodexBudget(ctx, 41, model, "harvest", reservation))
		require.NoError(t, other.consumeCodexBudget(ctx, 41, model, "replay", reservation))
		svc.releaseCodexBudget(41, reservation)
	}
	_, err := other.reserveCodexBudget(ctx, 41, model, 1, "iq")
	require.ErrorIs(t, err, errCodexBackgroundBudget)
	account, err := repo.GetByID(ctx, 41)
	require.NoError(t, err)
	st := codexBudgetStatus(account, model, time.Now())
	require.Equal(t, 32, st.ModelCalls)
}
func TestCodexReliabilityStandbyAndNoResurrection(t *testing.T) {
	a := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{StandbyEnabled: true}, nil)
	model := openAICodexTicketDefaultModel
	now := time.Now()
	active := verifiedTestTicket(a, 292)
	normalizeCodexTicketTimes(active, 3600)
	a.Extra[openAICodexTicketExtraKey(model)] = active
	newer := *active
	newer.State = fakeCodexTicketStateAt(292, now.Add(10*time.Second))
	newer.IssuedAt = now.Add(10 * time.Second)
	newer.ExpiresAt = active.ExpiresAt.Add(10 * time.Second)
	require.Equal(t, "standby", svc.publishCodexTicket(a, model, &newer, now))
	require.Equal(t, active.State, svc.lookupOpenAICodexTicket(a, model).State)
	active.Revoked = true
	active.Verified = false
	require.True(t, svc.promoteCodexStandby(a, model, now))
	require.Equal(t, newer.State, svc.lookupOpenAICodexTicket(a, model).State)
	newer.Revoked = true
	a.Extra[openAICodexTicketExtraKey(model)] = &newer
	require.Equal(t, "unchanged", svc.publishCodexTicket(a, model, &newer, now))
}
func TestCodexReliabilityProxyIPValidation(t *testing.T) {
	ip, _, code := ParseCodexProxyExit([]byte(`{"ip":"203.0.113.7"}`))
	require.Equal(t, "203.0.113.7", ip)
	require.Empty(t, code)
	ip, _, _ = ParseCodexProxyExit([]byte("ip=not-an-ip\nloc=CH"))
	require.Empty(t, ip)
	require.True(t, errors.Is(ErrCodexTicketConflict, ErrCodexTicketConflict))
}

func TestCodexReliabilityManualRotatesHealthyProxyWithinRound(t *testing.T) {
	var exits []string
	s, repo := ticketJobService(t, &codexTicketFuncUpstream{proxy: func(req *http.Request, proxy string) (*http.Response, error) {
		if req.Header.Get(openAICodexTurnStateHeader) == "" {
			exits = append(exits, proxy)
			if len(exits) == 1 {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("fixture unreachable")}
			}
		}
		return codexTicketResponse(), nil
	}})
	settings, store := reliabilityPoolService(t)
	store.values[SettingKeyOpenAICodexTicketEnabled] = "true"
	settings.cfg = s.cfg
	s.settingService = settings
	ctx := context.Background()
	pool, err := settings.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	second := "http://second.example:8080"
	_, err = settings.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: pool.Revision, Entries: []CodexHarvestProxyInput{{ID: pool.Entries[0].ID, Name: "first", Enabled: true}, {ID: uuid.NewString(), Name: "second", Enabled: true, URL: &second}}})
	require.NoError(t, err)
	job := s.startCodexAccountTicketJob(ctx, 41, true, openAICodexTicketDefaultModel)
	waitCodexTicketJob(t, job)
	require.Len(t, exits, 2)
	require.NotEqual(t, exits[0], exits[1])
	require.Equal(t, second, exits[1])
	a, err := repo.GetByID(ctx, 41)
	require.NoError(t, err)
	require.Equal(t, "succeeded", codexTicketRuntimes(a)[openAICodexTicketDefaultModel].Task.State)
}
