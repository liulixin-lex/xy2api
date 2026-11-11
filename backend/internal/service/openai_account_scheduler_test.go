package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type openAISnapshotCacheStub struct {
	SchedulerCache
	snapshotAccounts []*Account
	accountsByID     map[int64]*Account
}

type schedulerTestOpenAIAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r schedulerTestOpenAIAccountRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, errors.New("account not found")
}

func (r schedulerTestOpenAIAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	var result []Account
	for _, acc := range r.accounts {
		if acc.Platform == platform {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r schedulerTestOpenAIAccountRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r schedulerTestOpenAIAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	var result []Account
	for _, acc := range r.accounts {
		if acc.Platform == platform {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r schedulerTestOpenAIAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

type schedulerGroupAwareOpenAIAccountRepo struct {
	schedulerTestOpenAIAccountRepo
}

func (r schedulerGroupAwareOpenAIAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	var result []Account
	for _, acc := range r.accounts {
		if acc.Platform == platform && openAIStickyAccountMatchesGroup(&acc, &groupID) {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r schedulerGroupAwareOpenAIAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	var result []Account
	for _, acc := range r.accounts {
		if acc.Platform == platform && openAIStickyAccountMatchesGroup(&acc, nil) {
			result = append(result, acc)
		}
	}
	return result, nil
}

type schedulerTestConcurrencyCache struct {
	ConcurrencyCache
	loadBatchErr    error
	loadMap         map[int64]*AccountLoadInfo
	acquireResults  map[int64]bool
	waitCounts      map[int64]int
	skipDefaultLoad bool
	acquiredIDs     *[]int64
	releasedIDs     *[]int64
}

func (c schedulerTestConcurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	if c.acquiredIDs != nil {
		*c.acquiredIDs = append(*c.acquiredIDs, accountID)
	}
	if c.acquireResults != nil {
		if result, ok := c.acquireResults[accountID]; ok {
			return result, nil
		}
	}
	return true, nil
}

func (c schedulerTestConcurrencyCache) ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error {
	if c.releasedIDs != nil {
		*c.releasedIDs = append(*c.releasedIDs, accountID)
	}
	return nil
}

func (c schedulerTestConcurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	if c.loadBatchErr != nil {
		return nil, c.loadBatchErr
	}
	out := make(map[int64]*AccountLoadInfo, len(accounts))
	if c.skipDefaultLoad && c.loadMap != nil {
		for _, acc := range accounts {
			if load, ok := c.loadMap[acc.ID]; ok {
				out[acc.ID] = load
			}
		}
		return out, nil
	}
	for _, acc := range accounts {
		if c.loadMap != nil {
			if load, ok := c.loadMap[acc.ID]; ok {
				out[acc.ID] = load
				continue
			}
		}
		out[acc.ID] = &AccountLoadInfo{AccountID: acc.ID, LoadRate: 0}
	}
	return out, nil
}

func (c schedulerTestConcurrencyCache) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	if c.waitCounts != nil {
		if count, ok := c.waitCounts[accountID]; ok {
			return count, nil
		}
	}
	return 0, nil
}

type schedulerTestGatewayCache struct {
	sessionBindings map[string]int64
	deletedSessions map[string]int
}

func (c *schedulerTestGatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := c.sessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, ErrStickySessionNotFound
}

func (c *schedulerTestGatewayCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	if c.sessionBindings == nil {
		c.sessionBindings = make(map[string]int64)
	}
	c.sessionBindings[sessionHash] = accountID
	return nil
}

func (c *schedulerTestGatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

func (c *schedulerTestGatewayCache) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	if c.sessionBindings == nil {
		return nil
	}
	if c.deletedSessions == nil {
		c.deletedSessions = make(map[string]int)
	}
	c.deletedSessions[sessionHash]++
	delete(c.sessionBindings, sessionHash)
	return nil
}

func (c *schedulerTestGatewayCache) DeleteSessionAccountIDAndReport(ctx context.Context, groupID int64, sessionHash string) (bool, error) {
	_, existed := c.sessionBindings[sessionHash]
	return existed, c.DeleteSessionAccountID(ctx, groupID, sessionHash)
}

func (c *schedulerTestGatewayCache) SetGrokVideoPendingBilling(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}
func (c *schedulerTestGatewayCache) GetGrokVideoPendingBilling(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}
func (c *schedulerTestGatewayCache) ClaimGrokVideoBilled(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func (c *schedulerTestGatewayCache) ReleaseGrokVideoBilled(_ context.Context, _ string) error {
	return nil
}

func (c *schedulerTestGatewayCache) SetReasoningContent(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}
func (c *schedulerTestGatewayCache) GetReasoningContent(_ context.Context, _ string) (string, error) {
	return "", ErrReasoningContentNotFound
}

func newSchedulerTestOpenAIWSV2Config() *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	return cfg
}

func newSchedulerTestSubscriptionPriorityConfig() *config.Config {
	cfg := &config.Config{}
	return cfg
}

type selectionSettingRepoStub struct {
	values map[string]string
}

func (s *selectionSettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	value, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

func (s *selectionSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if s == nil || s.values == nil {
		return "", ErrSettingNotFound
	}
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (s *selectionSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected call to Set")
}

func (s *selectionSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, err := s.GetValue(context.Background(), key); err == nil {
			result[key] = value
		}
	}
	return result, nil
}

func (s *selectionSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected call to SetMultiple")
}

func (s *selectionSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected call to GetAll")
}

func (s *selectionSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected call to Delete")
}

func newOpenAISelectionRateLimitService() *RateLimitService {
	return &RateLimitService{settingService: NewSettingService(&selectionSettingRepoStub{values: map[string]string{}}, &config.Config{})}
}

func (s *openAISnapshotCacheStub) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	if len(s.snapshotAccounts) == 0 {
		return nil, false, nil
	}
	out := make([]*Account, 0, len(s.snapshotAccounts))
	for _, account := range s.snapshotAccounts {
		if account == nil {
			continue
		}
		cloned := *account
		out = append(out, &cloned)
	}
	return out, true, nil
}

func (s *openAISnapshotCacheStub) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s.accountsByID == nil {
		return nil, nil
	}
	account := s.accountsByID[accountID]
	if account == nil {
		return nil, nil
	}
	cloned := *account
	return &cloned, nil
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_DefaultDisabledUsesLegacyLoadAwareness(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10106)
	accounts := []Account{
		{
			ID:          36001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
		},
		{
			ID:          36002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cache := &schedulerTestGatewayCache{}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_disabled_001", 36001, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"resp_disabled_001",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(36002), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickyPreviousHit)
}

// Regression: the legacy load-batch path had two bare ErrNoAvailableAccounts
// exits that bypassed the diagnostics added for both the advanced scheduler and
// the non-batched legacy selector. This is the default path when load batching
// is enabled, so quota auto-pause could still surface as an opaque 503.
func TestOpenAIGatewayService_SelectAccountWithScheduler_DefaultDisabled_LoadBatchReportsFilterReasons(t *testing.T) {

	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold7d: 0.9})
	groupID := int64(10107)
	quotaPaused := Account{
		ID:          36003,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"codex_7d_used_percent":  95.0,
			"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}
	mappingMiss := Account{
		ID:          36004,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "gpt-4o"},
		},
	}
	excluded := Account{
		ID:          36005,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{quotaPaused, mappingMiss, excluded}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.4-mini",
		map[int64]struct{}{excluded.ID: {}},
		OpenAIUpstreamTransportAny,
		false,
	)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.EqualError(t, err, "no available OpenAI accounts supporting model: gpt-5.4-mini (pool=3, filtered: excluded=1 model_not_supported=1 quota_auto_pause_7d=1)")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_DefaultDisabled_RequiredWSV2_SkipsHTTPOnlyAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10108)
	accounts := []Account{
		{
			ID:          36011,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          36012,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(36012), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_DefaultDisabled_RequiredWSV2_NoAvailableAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10109)
	accounts := []Account{
		{
			ID:          36021,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
	}
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
		false,
	)
	require.ErrorContains(t, err, "no available OpenAI accounts")
	require.Nil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_DefaultDisabled_EmbeddingsSkipsChatOnlyAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10110)
	accounts := []Account{
		{
			ID:          36031,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions"},
			},
		},
		{
			ID:          36032,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions", "embeddings"},
			},
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"",
		"",
		"text-embedding-3-small",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityEmbeddings,
		false,
		false,
		true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(36032), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountForTokenCount_DoesNotAcquireGenerationSlot(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10115)
	acquiredIDs := make([]int64, 0)
	accounts := []Account{
		{
			ID: 36501, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			Credentials: map[string]any{"openai_capabilities": []any{"chat_completions"}},
		},
		{
			ID: 36502, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
			Credentials: map[string]any{"openai_capabilities": []any{"embeddings"}},
		},
		{
			ID: 36503, Platform: PlatformGrok, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
			Credentials: map[string]any{"openai_capabilities": []any{"chat_completions"}},
		},
		{
			ID: 36504, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 15,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions"},
				"model_mapping":       map[string]any{"gpt-4o": "gpt-4o"},
			},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:       &schedulerTestGatewayCache{},
		cfg:         &config.Config{},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquireResults: map[int64]bool{36501: false},
			acquiredIDs:    &acquiredIDs,
		}),
	}

	account, err := svc.SelectAccountForTokenCount(
		ctx,
		&groupID,
		"",
		"gpt-5.1",
		OpenAIEndpointCapabilityChatCompletions,
		PlatformOpenAI,
	)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(36501), account.ID)
	require.Empty(t, acquiredIDs, "token counting must not acquire a generation slot")
}

// 生图意图的 /v1/responses 请求要求 OpenAIEndpointCapabilityResponses：探测确认
// 不支持 Responses API 的 APIKey 账号必须被排除，避免 forward 阶段降级为无法生图
// 的 Chat Completions 直转（#4417）。
func TestOpenAIGatewayService_SelectAccountWithScheduler_ResponsesCapabilityExcludesUnsupportedAPIKey(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10120)

	newSvc := func(accounts []Account) *OpenAIGatewayService {
		cfg := &config.Config{}
		cfg.Gateway.Scheduling.LoadBatchEnabled = false
		return &OpenAIGatewayService{
			accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
			cache:              &schedulerTestGatewayCache{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		}
	}

	supported := Account{
		ID: 37001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
	}
	// 更高优先级但探测确认不支持 Responses——若门控失效会被优先选中。
	unsupported := Account{
		ID: 37002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
		Extra: map[string]any{"openai_responses_supported": false},
	}

	t.Run("生图意图仅选中支持 responses 的账号", func(t *testing.T) {
		svc := newSvc([]Account{supported, unsupported})
		selection, _, err := svc.SelectAccountWithSchedulerForCapability(
			ctx, &groupID, "", "", "gpt-image-2", nil,
			OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses,
			false, false, false,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, int64(37001), selection.Account.ID)
	})

	t.Run("仅有不支持 responses 的账号时生图意图无可用账号", func(t *testing.T) {
		svc := newSvc([]Account{unsupported})
		selection, _, err := svc.SelectAccountWithSchedulerForCapability(
			ctx, &groupID, "", "", "gpt-image-2", nil,
			OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses,
			false, false, false,
		)
		require.Error(t, err)
		require.Nil(t, selection)
	})

	t.Run("非生图路径仍可选中不支持 responses 的账号", func(t *testing.T) {
		svc := newSvc([]Account{unsupported})
		selection, _, err := svc.SelectAccountWithSchedulerForCapability(
			ctx, &groupID, "", "", "gpt-5.1", nil,
			OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions,
			false, false, true,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, int64(37002), selection.Account.ID)
	})
}

// alpha/search 调度必须同时放行 OAuth 与 APIKey 账号：v0.1.157 曾因 OAuth-only
// 门控把 APIKey 账号从候选池剔除，纯 APIKey 分组的独立搜索请求在选号阶段就
// 报无可用账号，Codex 网页搜索整体失效（转发层其实一直支持 APIKey 路径）。
func TestOpenAIGatewayService_SelectAccountWithScheduler_AlphaSearchAllowsAPIKeyAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10125)
	accounts := []Account{
		{
			ID:          38001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.6-sol",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityAlphaSearch,
		false,
		false,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(38001), selection.Account.ID)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_DefaultDisabled_AllowsGrokChatAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10113)
	accounts := []Account{
		{
			ID:          36041,
			Platform:    PlatformGrok,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"",
		"",
		"grok-4.3",
		nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false,
		false,
		false,
		PlatformGrok,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(36041), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_GrokMediaCapabilityFiltersIneligibleAccounts(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10114)
	ineligible := Account{
		ID: 36051, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
		Extra: map[string]any{GrokMediaEligibleExtraKey: false},
	}
	eligible := Account{
		ID: 36052, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		Extra: map[string]any{GrokMediaEligibleExtraKey: true},
	}
	newService := func(accounts []Account) *OpenAIGatewayService {
		cfg := &config.Config{}
		cfg.Gateway.Scheduling.LoadBatchEnabled = false
		return &OpenAIGatewayService{
			accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
			cache:              &schedulerTestGatewayCache{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		}
	}

	t.Run("media generation skips higher priority ineligible account", func(t *testing.T) {
		selection, _, err := newService([]Account{ineligible, eligible}).SelectAccountWithSchedulerForCapability(
			ctx, &groupID, "", "", "grok-imagine-video", nil,
			OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityGrokMediaGeneration,
			false, false, false, PlatformGrok,
		)

		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, eligible.ID, selection.Account.ID)
	})

	t.Run("media generation fails closed when all accounts are ineligible", func(t *testing.T) {
		selection, _, err := newService([]Account{ineligible}).SelectAccountWithSchedulerForCapability(
			ctx, &groupID, "", "", "grok-imagine-video", nil,
			OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityGrokMediaGeneration,
			false, false, false, PlatformGrok,
		)

		require.Error(t, err)
		require.ErrorIs(t, err, ErrNoAvailableAccounts)
		require.Nil(t, selection)
	})

	t.Run("chat remains routable on media-ineligible account", func(t *testing.T) {
		selection, _, err := newService([]Account{ineligible}).SelectAccountWithSchedulerForCapability(
			ctx, &groupID, "", "", "grok-4.3", nil,
			OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions,
			false, false, false, PlatformGrok,
		)

		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, ineligible.ID, selection.Account.ID)
	})
}

// Regression #4599: when the advanced scheduler's load-balance initial filter
// drops every candidate, the resulting "no available OpenAI accounts" error must
// carry per-reason exclusion counts. Previously all filter branches were silent
// (debug logs at best), so a 503 with excluded_account_count=0 could not be
// diagnosed from the error alone.
func TestOpenAIGatewayService_SelectAccountWithScheduler_NoAvailableErrorReportsQuotaAutoPauseExclusion(t *testing.T) {

	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold7d: 0.9})
	groupID := int64(101201)
	accounts := []Account{
		{
			ID:          38101,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"codex_7d_used_percent":  95.0,
				"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339),
			},
		},
	}
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.4-mini", nil, OpenAIUpstreamTransportAny, false,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.EqualError(t, err, "no available OpenAI accounts supporting model: gpt-5.4-mini (pool=1, filtered: quota_auto_pause_7d=1)")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_NoAvailableErrorReportsModelNotSupported(t *testing.T) {

	ctx := context.Background()
	groupID := int64(101202)
	// OAuth account with empty model_mapping: foreign-family models (grok-*)
	// are not servable by Codex upstream (#3662) and are dropped by the filter.
	accounts := []Account{
		{
			ID:          38111,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "grok-4.5", nil, OpenAIUpstreamTransportAny, false,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.EqualError(t, err, "no available OpenAI accounts supporting model: grok-4.5 (pool=1, filtered: model_not_supported=1)")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_NoAvailableErrorAggregatesReasonsDeterministically(t *testing.T) {

	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold7d: 0.9})
	groupID := int64(101203)
	quotaPaused := Account{
		ID:          38121,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"codex_7d_used_percent":  95.0,
			"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}
	mappingMiss := Account{
		ID:          38122,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "gpt-4o"},
		},
	}
	excluded := Account{
		ID:          38123,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{quotaPaused, mappingMiss, excluded}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.4-mini", map[int64]struct{}{38123: {}}, OpenAIUpstreamTransportAny, false,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	// Reasons are sorted lexicographically, so the message is deterministic.
	require.EqualError(t, err, "no available OpenAI accounts supporting model: gpt-5.4-mini (pool=3, filtered: excluded=1 model_not_supported=1 quota_auto_pause_7d=1)")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_NoAvailableErrorReportsEmptyPool(t *testing.T) {

	ctx := context.Background()
	groupID := int64(101204)
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.EqualError(t, err, "no available OpenAI accounts supporting model: gpt-5.1 (pool=0)")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_EnabledUsesAdvancedPreviousResponseRouting(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10107)
	accounts := []Account{
		{
			ID:          37001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			GroupIDs:    []int64{groupID},
			Priority:    5,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
		{
			ID:          37002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			GroupIDs:    []int64{groupID},
			Priority:    0,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_enabled_001", 37001, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"resp_enabled_001",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37001), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.True(t, decision.StickyPreviousHit)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_StickyWeightedSessionInTopKUsesStickyFirst(t *testing.T) {

	ctx := context.Background()
	groupID := int64(101071)
	accounts := []Account{
		{
			ID:          37101,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    100,
			GroupIDs:    []int64{groupID},
		},
		{
			ID:          37102,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
		},
	}
	cfg := &config.Config{}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{
		"openai:session_hash_weighted_topk": 37101,
	}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_weighted_topk",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37101), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)

	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_AccountModelRouteOnlyUsesClaimingAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(1010711)
	owner := Account{
		ID: 371011, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{"model_mapping": map[string]any{"team-alias": "gpt-5.1"}},
	}
	nonOwner := Account{
		ID: 371012, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		GroupIDs: []int64{groupID},
	}
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{owner, nonOwner}}
	ownership, err := (&GatewayService{accountRepo: repo}).resolveCompositeModelOwnership(ctx, groupID, "team-alias")
	require.NoError(t, err)
	require.Equal(t, CompositeModelOwnership{TargetPlatform: PlatformOpenAI, Matched: true}, ownership)
	ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceAccount, GroupID: groupID,
		PublicModel: "team-alias", TargetPlatform: ownership.TargetPlatform, UpstreamModel: "team-alias",
	})

	for _, tt := range []struct {
		name            string
		sessionHash     string
		sessionBindings map[string]int64
	}{
		{name: "initial filter excludes non owner"},
		{name: "non owner sticky hit is rejected", sessionHash: "non-owner", sessionBindings: map[string]int64{"openai:non-owner": nonOwner.ID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.Scheduling.LoadBatchEnabled = false
			svc := &OpenAIGatewayService{
				accountRepo:        repo,
				cache:              &schedulerTestGatewayCache{sessionBindings: tt.sessionBindings},
				cfg:                cfg,
				rateLimitService:   newOpenAISelectionRateLimitService(),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}

			selection, _, selectErr := svc.SelectAccountWithScheduler(
				ctx, &groupID, "", tt.sessionHash, "team-alias", nil, OpenAIUpstreamTransportAny, false,
			)
			require.NoError(t, selectErr)
			require.NotNil(t, selection)
			require.NotNil(t, selection.Account)
			require.Equal(t, owner.ID, selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_AccountModelRouteDBRecheckFailsOverToOwner(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1010712)
	mapping := map[string]any{"model_mapping": map[string]any{"team-alias": "gpt-5.1"}}
	stalePrimary := &Account{
		ID: 371021, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		GroupIDs: []int64{groupID}, Credentials: mapping,
	}
	staleBackup := &Account{
		ID: 371022, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
		GroupIDs: []int64{groupID}, Credentials: mapping,
	}
	dbPrimary := *stalePrimary
	dbPrimary.Credentials = nil
	dbBackup := *staleBackup
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{dbPrimary, dbBackup}}
	ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceAccount, GroupID: groupID,
		PublicModel: "team-alias", TargetPlatform: PlatformOpenAI, UpstreamModel: "team-alias",
	})
	acquiredIDs, releasedIDs := []int64{}, []int64{}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		cfg:              &config.Config{RunMode: config.RunModeStandard},
		rateLimitService: newOpenAISelectionRateLimitService(),
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
			snapshotAccounts: []*Account{stalePrimary, staleBackup},
			accountsByID:     map[int64]*Account{stalePrimary.ID: stalePrimary, staleBackup.ID: staleBackup},
		}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquiredIDs: &acquiredIDs, releasedIDs: &releasedIDs,
		}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "team-alias", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, dbBackup.ID, selection.Account.ID)
	if containsInt64(acquiredIDs, dbPrimary.ID) {
		require.Contains(t, releasedIDs, dbPrimary.ID)
	}
	require.Contains(t, acquiredIDs, dbBackup.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_StickyWeightedPreviousRequiresMovableContext(t *testing.T) {

	ctx := context.Background()
	groupID := int64(101072)
	accounts := []Account{
		{
			ID:          37111,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    100,
			GroupIDs:    []int64{groupID},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
		{
			ID:          37112,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cfg := newSchedulerTestOpenAIWSV2Config()
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_weighted_unmovable", 37111, time.Hour))

	selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"resp_weighted_unmovable",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false,
		false,
		true,
		PlatformOpenAI,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37111), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.True(t, decision.StickyPreviousHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	selection, decision, err = svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"resp_weighted_unmovable",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false,
		true,
		true,
		PlatformOpenAI,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37111), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.True(t, decision.StickyPreviousHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_PreviousResponseCompactUnsupportedDeletesBinding(t *testing.T) {

	ctx := context.Background()
	groupID := int64(101073)
	accounts := []Account{
		{
			ID:          37121,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
				"openai_compact_mode":                           OpenAICompactModeForceOff,
			},
		},
		{
			ID:          37122,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    10,
			GroupIDs:    []int64{groupID},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
				"openai_compact_mode":                           OpenAICompactModeForceOn,
			},
		},
	}
	cfg := newSchedulerTestOpenAIWSV2Config()
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_compact_unsupported", 37121, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"resp_compact_unsupported",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37122), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickyPreviousHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	accountID, err := store.GetResponseAccount(ctx, groupID, "resp_compact_unsupported")
	require.NoError(t, err)
	require.Zero(t, accountID)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_Enabled_EmbeddingsSkipsChatOnlyAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10111)
	accounts := []Account{
		{
			ID:          37011,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions"},
			},
		},
		{
			ID:          37012,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions", "embeddings"},
			},
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"",
		"",
		"text-embedding-3-small",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityEmbeddings,
		false,
		false,
		true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37012), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)

}

func TestOpenAIGatewayService_SelectAccountWithScheduler_Enabled_EmbeddingsSkipsChatOnlyStickyBindings(t *testing.T) {

	ctx := context.Background()
	groupID := int64(10112)
	accounts := []Account{
		{
			ID:          37021,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions"},
			},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
		{
			ID:          37022,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			Credentials: map[string]any{
				"openai_capabilities": []any{"chat_completions", "embeddings"},
			},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_embeddings": 37021,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_embeddings_chat_only", 37021, time.Hour))

	selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"resp_embeddings_chat_only",
		"session_hash_embeddings",
		"text-embedding-3-small",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityEmbeddings,
		false,
		false,
		true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37022), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickyPreviousHit)
	require.False(t, decision.StickySessionHit)
	require.Equal(t, int64(37022), cache.sessionBindings["openai:session_hash_embeddings"])
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SkipsQuarantinedSharedProxy(t *testing.T) {

	proxyA := int64(4698)
	proxyB := int64(4699)
	accounts := []Account{
		{ID: 469801, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, ProxyID: &proxyA},
		{ID: 469802, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, ProxyID: &proxyA},
		{ID: 469803, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, ProxyID: &proxyB},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1,
			failureWindow:    time.Minute,
			quarantineTTL:    10 * time.Minute,
			maxEntries:       16,
		}),
	}
	svc.openaiProxyStreamCircuit.recordFailure(proxyA, time.Now())

	selection, _, err := svc.SelectAccountWithScheduler(
		context.Background(), nil, "", "", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(469803), selection.Account.ID)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_FailsOpenWhenAllProxiesQuarantined(t *testing.T) {

	// Every schedulable account shares the quarantined proxy: the circuit must
	// degrade to a preference instead of zeroing out capacity (#5056).
	proxyA := int64(5056)
	accounts := []Account{
		{ID: 505601, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, ProxyID: &proxyA},
		{ID: 505602, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, ProxyID: &proxyA},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1,
			failureWindow:    time.Minute,
			quarantineTTL:    10 * time.Minute,
			maxEntries:       16,
		}),
	}
	tripped, _ := svc.openaiProxyStreamCircuit.recordFailure(proxyA, time.Now())
	require.True(t, tripped)

	selection, _, err := svc.SelectAccountWithScheduler(
		context.Background(), nil, "", "", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err, "quarantine must fail open instead of returning no available accounts")
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.NotNil(t, selection.Account.ProxyID)
	require.Equal(t, proxyA, *selection.Account.ProxyID)
	require.True(t, svc.openaiProxyStreamCircuit.isBlocked(proxyA, time.Now()),
		"fail-open must not clear the quarantine; only a completed stream or TTL expiry does")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyRateLimitedAccountFallsBackToFreshCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10101)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	staleSticky := &Account{ID: 31001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}
	staleBackup := &Account{ID: 31002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	freshSticky := &Account{ID: 31001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, RateLimitResetAt: &rateLimitedUntil}
	freshBackup := &Account{ID: 31002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:session_hash_rate_limited": 31001}}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{staleSticky, staleBackup}, accountsByID: map[int64]*Account{31001: freshSticky, 31002: freshBackup}}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{*freshSticky, *freshBackup}},
		cache:              cache,
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		schedulerSnapshot:  snapshotService,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_hash_rate_limited", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(31002), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_AutoPauseBy5hThreshold(t *testing.T) {
	ctx := context.Background()
	primary := Account{
		ID:          35001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":   95.0,
			"auto_pause_5h_threshold": 0.95,
		},
	}
	secondary := Account{ID: 35002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35002), account.ID)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_AllowsBelow5hThreshold(t *testing.T) {
	ctx := context.Background()
	primary := Account{
		ID:          35101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":   80.0,
			"auto_pause_5h_threshold": 0.95,
		},
	}
	secondary := Account{ID: 35102, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35101), account.ID)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_AutoPauseBy7dThreshold(t *testing.T) {
	ctx := context.Background()
	primary := Account{
		ID:          35201,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_7d_used_percent":   95.0,
			"auto_pause_7d_threshold": 0.95,
		},
	}
	secondary := Account{ID: 35202, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35202), account.ID)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_UnconfiguredThresholdKeepsLegacyBehavior(t *testing.T) {
	ctx := context.Background()
	primary := Account{ID: 35301, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, Extra: map[string]any{"codex_5h_used_percent": 99.0, "codex_7d_used_percent": 99.0}}
	secondary := Account{ID: 35302, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35301), account.ID)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_UsesGlobalDefaultThreshold(t *testing.T) {
	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	primary := Account{
		ID:          35401,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent": 95.0,
		},
	}
	secondary := Account{ID: 35402, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35402), account.ID)
}

// Regression: a per-account explicit-disable flag exempts the account from auto-pause
// even when a global default threshold is set. Without this, "leave threshold blank"
// silently falls back to global default and admins have no way to whitelist a single
// account.
func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_PerAccountDisableOverridesGlobalDefault(t *testing.T) {
	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	// Account has high usage AND no per-account threshold (would normally fall back to
	// the global default and get paused), but the explicit disable flag is set.
	primary := Account{
		ID:          35701,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":  99.0,
			"auto_pause_5h_disabled": true,
		},
	}
	secondary := Account{ID: 35702, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35701), account.ID)
}

// Disable is per-window: disabling only 5h must still allow 7d auto-pause to fire.
func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_PerWindowDisableScoped(t *testing.T) {
	ctx := context.Background()
	primary := Account{
		ID:          35801,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":   99.0,
			"codex_7d_used_percent":   99.0,
			"auto_pause_5h_disabled":  true,
			"auto_pause_7d_threshold": 0.95,
		},
	}
	secondary := Account{ID: 35802, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35802), account.ID, "7d auto-pause must still fire even though 5h is disabled")
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_StaleUsageWindowResetSkipsPause(t *testing.T) {
	ctx := context.Background()
	// Usage is over threshold but the window's reset time has already passed, so the
	// cached percentage is stale (the real window rolled over) and the account must NOT
	// stay paused — otherwise it could be skipped forever with no traffic to refresh it.
	primary := Account{
		ID:          35501,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":   99.0,
			"auto_pause_5h_threshold": 0.95,
			"codex_5h_reset_at":       time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}
	secondary := Account{ID: 35502, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35501), account.ID)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_FreshUsageWindowStillPauses(t *testing.T) {
	ctx := context.Background()
	// Same as above but the window has not reset yet, so the account stays paused.
	primary := Account{
		ID:          35601,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":   99.0,
			"auto_pause_5h_threshold": 0.95,
			"codex_5h_reset_at":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	secondary := Account{ID: 35602, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35602), account.ID)
}

// Issue #2994: an account poisoned with an inflated used% (e.g. from the reverted #2918
// inversion) gets excluded from scheduling, and a paused account never receives traffic to
// refresh its snapshot. When the snapshot is stale (codex_usage_updated_at older than the
// staleness bound) the account must be allowed a request so it can self-heal from the real
// response headers when no valid future window reset is known.
func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_StaleUsageSnapshotSkipsPause_Issue2994(t *testing.T) {
	ctx := context.Background()
	primary := Account{
		ID:          35701,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":   99.0,
			"auto_pause_5h_threshold": 0.95,
			// No reset time is known, so stale snapshot self-heal applies.
			// Snapshot is stale: older than openAICodexAutoPauseStaleAfter (2h).
			"codex_usage_updated_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
		},
	}
	secondary := Account{ID: 35702, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35701), account.ID)
}

// Issue #2994 guardrail: a genuinely-exhausted account whose snapshot was refreshed recently
// (codex_usage_updated_at fresh) must STILL be auto-paused. The stale self-heal must not let a
// real 99%-used account escape pause.
func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_FreshExhaustedSnapshotStillPauses_Issue2994(t *testing.T) {
	ctx := context.Background()
	primary := Account{
		ID:          35801,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			"codex_5h_used_percent":   99.0,
			"auto_pause_5h_threshold": 0.95,
			"codex_5h_reset_at":       time.Now().Add(time.Hour).Format(time.RFC3339),
			// Snapshot refreshed 1 minute ago: not stale, so the account stays paused.
			"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}
	secondary := Account{ID: 35802, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(35802), account.ID)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_SkipsFreshlyRateLimitedSnapshotCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10102)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	stalePrimary := &Account{ID: 32001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}
	staleSecondary := &Account{ID: 32002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	freshPrimary := &Account{ID: 32001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, RateLimitResetAt: &rateLimitedUntil}
	freshSecondary := &Account{ID: 32002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{stalePrimary, staleSecondary}, accountsByID: map[int64]*Account{32001: freshPrimary, 32002: freshSecondary}}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache}
	svc := &OpenAIGatewayService{
		accountRepo:       schedulerTestOpenAIAccountRepo{accounts: []Account{*freshPrimary, *freshSecondary}},
		cfg:               &config.Config{},
		rateLimitService:  newOpenAISelectionRateLimitService(),
		schedulerSnapshot: snapshotService,
	}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(32002), account.ID)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_ModelRateLimitOnlySkipsThatModel(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Now().Add(30 * time.Minute).Format(time.RFC3339)
	primary := Account{
		ID:          32101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				"gpt-5.4": map[string]any{
					"rate_limit_reset_at": resetAt,
				},
			},
		},
	}
	secondary := Account{
		ID:          32102,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    5,
	}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}},
		cfg:         &config.Config{},
	}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.4", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(32102), account.ID)

	account, err = svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.3", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(32101), account.ID)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyDBRuntimeRecheckSkipsStaleCachedAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10103)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	staleSticky := &Account{ID: 33001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}
	staleBackup := &Account{ID: 33002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	dbSticky := Account{ID: 33001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, RateLimitResetAt: &rateLimitedUntil}
	dbBackup := Account{ID: 33002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:session_hash_db_runtime_recheck": 33001}}
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{staleSticky, staleBackup},
		accountsByID:     map[int64]*Account{33001: staleSticky, 33002: staleBackup},
	}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{dbSticky, dbBackup}},
		cache:              cache,
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		schedulerSnapshot:  snapshotService,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_hash_db_runtime_recheck", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(33002), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_DBRuntimeRecheckSkipsStaleCachedCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10104)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	stalePrimary := &Account{ID: 34001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}
	staleSecondary := &Account{ID: 34002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	dbPrimary := Account{ID: 34001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, RateLimitResetAt: &rateLimitedUntil}
	dbSecondary := Account{ID: 34002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{stalePrimary, staleSecondary},
		accountsByID:     map[int64]*Account{34001: stalePrimary, 34002: staleSecondary},
	}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache}
	svc := &OpenAIGatewayService{
		accountRepo:       schedulerTestOpenAIAccountRepo{accounts: []Account{dbPrimary, dbSecondary}},
		cfg:               &config.Config{},
		rateLimitService:  newOpenAISelectionRateLimitService(),
		schedulerSnapshot: snapshotService,
	}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(34002), account.ID)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_DBFreshGroupRecheckReleasesMovedAccount(t *testing.T) {
	ctx := context.Background()
	groupID, otherGroupID := int64(10105), int64(10106)
	stalePrimary := &Account{ID: 34101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}
	staleBackup := &Account{ID: 34102, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID}}
	dbPrimary := *stalePrimary
	dbPrimary.GroupIDs = []int64{otherGroupID}
	dbBackup := *staleBackup
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{stalePrimary, staleBackup},
		accountsByID:     map[int64]*Account{stalePrimary.ID: stalePrimary, staleBackup.ID: staleBackup},
	}
	acquiredIDs, releasedIDs := []int64{}, []int64{}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := &OpenAIGatewayService{
		accountRepo:       schedulerTestOpenAIAccountRepo{accounts: []Account{dbPrimary, dbBackup}},
		cfg:               cfg,
		schedulerSnapshot: &SchedulerSnapshotService{cache: snapshotCache},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquiredIDs: &acquiredIDs,
			releasedIDs: &releasedIDs,
		}),
	}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.Equal(t, staleBackup.ID, selection.Account.ID)
	require.Equal(t, []int64{staleBackup.ID}, acquiredIDs)
	require.Empty(t, releasedIDs, "moved account is rejected before acquiring a slot")
	selection.ReleaseFunc()
}

func TestOpenAIGatewayService_SelectAccountWithLoadAwareness_DBFreshGroupRecheckWaitsOnValidAccount(t *testing.T) {
	ctx := context.Background()
	groupID, otherGroupID := int64(10107), int64(10108)
	stalePrimary := &Account{ID: 34201, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}
	staleBackup := &Account{ID: 34202, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID}}
	dbPrimary := *stalePrimary
	dbPrimary.GroupIDs = []int64{otherGroupID}
	dbBackup := *staleBackup
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{stalePrimary, staleBackup},
		accountsByID:     map[int64]*Account{stalePrimary.ID: stalePrimary, staleBackup.ID: staleBackup},
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &OpenAIGatewayService{
		accountRepo:       schedulerTestOpenAIAccountRepo{accounts: []Account{dbPrimary, dbBackup}},
		cfg:               cfg,
		schedulerSnapshot: &SchedulerSnapshotService{cache: snapshotCache},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquireResults: map[int64]bool{staleBackup.ID: false},
		}),
	}

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, staleBackup.ID, selection.Account.ID)
	require.Equal(t, staleBackup.ID, selection.WaitPlan.AccountID)
}

func TestOpenAIGatewayService_RecheckSelectedOpenAIAccountFromDB_SimpleModeUsesFullPool(t *testing.T) {
	grouped := Account{ID: 34301, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{99}}
	svc := &OpenAIGatewayService{
		accountRepo:       schedulerTestOpenAIAccountRepo{accounts: []Account{grouped}},
		cfg:               &config.Config{RunMode: config.RunModeSimple},
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{}},
	}
	requestedGroupID := int64(100)

	for _, groupID := range []*int64{nil, &requestedGroupID} {
		fresh := svc.recheckSelectedOpenAIAccountFromDB(context.Background(), &grouped, groupID, PlatformOpenAI, "gpt-5.1", false, "")
		require.NotNil(t, fresh)
		require.Equal(t, grouped.ID, fresh.ID)
	}

	ungrouped := grouped
	ungrouped.ID++
	ungrouped.GroupIDs = nil
	standardSvc := &OpenAIGatewayService{
		accountRepo:       schedulerTestOpenAIAccountRepo{accounts: []Account{grouped, ungrouped}},
		cfg:               &config.Config{RunMode: config.RunModeStandard},
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{}},
	}
	require.Nil(t, standardSvc.recheckSelectedOpenAIAccountFromDB(context.Background(), &grouped, nil, PlatformOpenAI, "gpt-5.1", false, ""))
	require.NotNil(t, standardSvc.recheckSelectedOpenAIAccountFromDB(context.Background(), &ungrouped, nil, PlatformOpenAI, "gpt-5.1", false, ""))
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_PreviousResponseSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(9)
	account := Account{
		ID:          1001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		GroupIDs:    []int64{groupID},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	cache := &schedulerTestGatewayCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_001", account.ID, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"resp_prev_001",
		"session_hash_001",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.True(t, decision.StickyPreviousHit)
	require.Equal(t, account.ID, cache.sessionBindings["openai:session_hash_001"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10)
	account := Account{
		ID:          2001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_abc": account.ID,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_abc",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyBusyBorrowsKeepsBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10100)
	accounts := []Account{
		{
			ID:          21001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
		},
		{
			ID:          21002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    9,
			GroupIDs:    []int64{groupID},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_sticky_busy": 21001,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true

	concurrencyCache := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{
			21001: false, // sticky 账号已满
			21002: true,  // 若回退负载均衡会命中该账号（本测试要求不能切换）
		},
		waitCounts: map[int64]int{
			21001: 999,
		},
		loadMap: map[int64]*AccountLoadInfo{
			21001: {AccountID: 21001, LoadRate: 90, WaitingCount: 9},
			21002: {AccountID: 21002, LoadRate: 1, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_sticky_busy",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21002), selection.Account.ID)
	require.True(t, selection.Acquired)
	require.True(t, selection.StickyCapacitySpillover)
	require.Nil(t, selection.WaitPlan)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	require.Equal(t, int64(21001), cache.sessionBindings["openai:session_hash_sticky_busy"])
	selection.ReleaseFunc()
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyBusyEscapes(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10103)
	accounts := []Account{
		{ID: 21301, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}},
		{ID: 21302, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID}},
	}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:session_hash_sticky_busy_escape": 21301}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	concurrencyCache := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{21301: false, 21302: true},
		waitCounts:     map[int64]int{21301: 999},
		loadMap: map[int64]*AccountLoadInfo{
			21301: {AccountID: 21301, LoadRate: 95, WaitingCount: 9},
			21302: {AccountID: 21302, LoadRate: 1, WaitingCount: 0},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_hash_sticky_busy_escape", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21302), selection.Account.ID)
	require.Nil(t, selection.WaitPlan)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyCapacitySpilloverPreservesOwner(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10104)
	accounts := []Account{
		{ID: 21401, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}},
		{ID: 21402, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID}},
	}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:session_hash_sticky_disabled": 21401}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	concurrencyCache := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{21401: false, 21402: true},
		waitCounts:     map[int64]int{21401: 999},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_hash_sticky_disabled", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21402), selection.Account.ID)
	require.True(t, selection.StickyCapacitySpillover)
	require.Nil(t, selection.WaitPlan)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	require.Equal(t, int64(21401), cache.sessionBindings["openai:session_hash_sticky_disabled"])
	selection.ReleaseFunc()
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SubscriptionPriorityFallsBackWhenSubscriptionFull(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10121)
	accounts := []Account{
		{
			ID:          21611,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
			Credentials: map[string]any{"plan_type": "team"},
		},
		{
			ID:          21612,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    9,
			GroupIDs:    []int64{groupID},
		},
	}
	concurrencyCache := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{21611: false, 21612: true},
		loadMap: map[int64]*AccountLoadInfo{
			21611: {AccountID: 21611, LoadRate: 0, WaitingCount: 0},
			21612: {AccountID: 21612, LoadRate: 90, WaitingCount: 1},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                newSchedulerTestSubscriptionPriorityConfig(),
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_subscription_fallback", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21612), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.True(t, selection.Acquired)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SubscriptionPriorityDisabledUsesScore(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10122)
	accounts := []Account{
		{
			ID:          21621,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    10,
			GroupIDs:    []int64{groupID},
			Credentials: map[string]any{"plan_type": "pro"},
		},
		{
			ID:          21622,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
		},
	}
	concurrencyCache := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{21621: true, 21622: true},
		loadMap: map[int64]*AccountLoadInfo{
			21621: {AccountID: 21621, LoadRate: 90, WaitingCount: 1},
			21622: {AccountID: 21622, LoadRate: 0, WaitingCount: 0},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                newSchedulerTestSubscriptionPriorityConfig(),
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_subscription_disabled", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21622), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_UsesAccountPriorityWithinGroupPool(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10123)
	accounts := []Account{
		{
			ID:          21631,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
			AccountGroups: []AccountGroup{
				{AccountID: 21631, GroupID: groupID, Priority: 100},
			},
			GroupIDs: []int64{groupID},
		},
		{
			ID:          21632,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    100000,
			AccountGroups: []AccountGroup{
				{AccountID: 21632, GroupID: groupID, Priority: 1},
			},
			GroupIDs: []int64{groupID},
		},
	}
	cfg := newSchedulerTestSubscriptionPriorityConfig()
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_group_priority", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21631), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIAccountScheduler_SkipsAccountBlockedForRequestedModel(t *testing.T) {
	now := time.Now()
	account := &Account{ID: 21633, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{openaiModelTransient: newOpenAIAccountModelTransientState(128)}
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-5.5", now)
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-5.5", now.Add(time.Millisecond))
	scheduler := svc

	require.False(t, scheduler.isAccountRequestCompatible(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.5"}))
	require.True(t, scheduler.isAccountRequestCompatible(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.6-sol"}))
}

func TestReportOpenAIAccountScheduleResult_SuccessClearsModelTransientState(t *testing.T) {
	svc := &OpenAIGatewayService{openaiModelTransient: newOpenAIAccountModelTransientState(128)}
	now := time.Now()
	svc.openaiModelTransient.recordFailure(21636, "gpt-5.5", now)
	svc.openaiModelTransient.recordFailure(21636, "gpt-5.5", now.Add(time.Millisecond))
	require.True(t, svc.openaiModelTransient.isBlocked(21636, "gpt-5.5", now.Add(2*time.Millisecond)))

	svc.ReportOpenAIAccountScheduleResult(&Account{ID: 21636}, "gpt-5.5", true, nil)

	require.False(t, svc.openaiModelTransient.isBlocked(21636, "gpt-5.5", now.Add(2*time.Millisecond)))
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionSticky_ForceHTTP(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1010)
	account := Account{
		ID:          2101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
		Extra: map[string]any{
			"openai_ws_force_http": true,
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_force_http": account.ID,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_force_http",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_RequiredWSV2_SkipsStickyHTTPAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1011)
	accounts := []Account{
		{
			ID:          2201,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
		},
		{
			ID:          2202,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			GroupIDs:    []int64{groupID},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_ws_only": 2201,
		},
	}
	cfg := newSchedulerTestOpenAIWSV2Config()

	// 构造“HTTP-only 账号负载更低”的场景，验证 required transport 会强制过滤。
	concurrencyCache := schedulerTestConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			2201: {AccountID: 2201, LoadRate: 0, WaitingCount: 0},
			2202: {AccountID: 2202, LoadRate: 90, WaitingCount: 5},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_ws_only",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(2202), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)

	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_ClearsStickyAccountOutsideGroup(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1013)
	accounts := []Account{
		{
			ID:          2401,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          2402,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			AccountGroups: []AccountGroup{
				{AccountID: 2402, GroupID: groupID},
			},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_removed_group": 2401,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              cache,
		cfg:                &config.Config{},
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_removed_group",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(2402), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	require.Equal(t, 1, cache.deletedSessions["openai:session_hash_removed_group"])
	require.Equal(t, int64(2402), cache.sessionBindings["openai:session_hash_removed_group"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_RequiredWSV2_NoAvailableAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1012)
	accounts := []Account{
		{
			ID:          2301,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                newSchedulerTestOpenAIWSV2Config(),
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
		false,
	)
	require.Error(t, err)
	require.Nil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)

}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceTopKFallback(t *testing.T) {
	ctx := context.Background()
	groupID := int64(11)
	accounts := []Account{
		{
			ID:          3001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          3002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          3003,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true

	concurrencyCache := schedulerTestConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			3001: {AccountID: 3001, LoadRate: 95, WaitingCount: 8},
			3002: {AccountID: 3002, LoadRate: 20, WaitingCount: 1},
			3003: {AccountID: 3003, LoadRate: 10, WaitingCount: 0},
		},
		acquireResults: map[int64]bool{
			3003: false, // top1 失败，必须回退到 top-K 的下一候选
			3002: true,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(3002), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)

	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

// Regression: TopK initial filter must drop quota-auto-paused accounts. Otherwise
// the candidate pool is filled with paused accounts, healthy accounts fall outside
// TopK, and the scheduler returns "no available accounts" even though healthy ones
// exist.
func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceTopKExcludesQuotaPaused(t *testing.T) {
	ctx := context.Background()
	groupID := int64(110)
	accounts := []Account{
		{
			ID:          37001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   96.0,
				"auto_pause_5h_threshold": 0.95,
			},
		},
		{
			ID:          37002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
		},
	}

	cfg := &config.Config{}

	concurrencyCache := schedulerTestConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			37001: {AccountID: 37001, LoadRate: 5, WaitingCount: 0},
			37002: {AccountID: 37002, LoadRate: 5, WaitingCount: 0},
		},
		acquireResults: map[int64]bool{
			37002: true,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(37002), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	// Only the healthy account should ever enter the candidate pool; the paused one
	// must be filtered out at the initial-filter stage.

	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func intPtrForTest(v int) *int {
	return &v
}

func TestClamp01_AllBranches(t *testing.T) {
	require.Equal(t, 0.0, clamp01(-0.2))
	require.Equal(t, 1.0, clamp01(1.3))
	require.Equal(t, 0.5, clamp01(0.5))
}

func TestOpenAIGatewayService_SchedulerWrappersAndDefaults(t *testing.T) {

	svc := &OpenAIGatewayService{}
	ttft := 120
	svc.ReportOpenAIAccountScheduleResult(&Account{ID: 10}, "", true, &ttft)
	require.Equal(t, openaiStickySessionTTL, svc.openAIWSSessionStickyTTL())

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 180
	svcWithCfg := &OpenAIGatewayService{cfg: cfg}

	require.Equal(t, 180*time.Second, svcWithCfg.openAIWSSessionStickyTTL())

}

func TestOpenAISelection_IsAccountTransportCompatible_Branches(t *testing.T) {
	scheduler := &OpenAIGatewayService{}
	require.True(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportAny))
	require.True(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportHTTPSSE))
	require.False(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportResponsesWebsocketV2))
	require.False(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportResponsesWebsocketV2Ingress))

	cfg := newSchedulerTestOpenAIWSV2Config()
	scheduler.cfg = cfg
	account := &Account{
		ID:          8801,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	require.True(t, scheduler.isAccountTransportCompatible(account, OpenAIUpstreamTransportResponsesWebsocketV2))
	require.True(t, scheduler.isAccountTransportCompatible(account, OpenAIUpstreamTransportResponsesWebsocketV2Ingress))

	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeHTTPBridge
	require.False(t, scheduler.isAccountTransportCompatible(account, OpenAIUpstreamTransportResponsesWebsocketV2))
	require.True(t, scheduler.isAccountTransportCompatible(account, OpenAIUpstreamTransportResponsesWebsocketV2Ingress))

	account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
	require.False(t, scheduler.isAccountTransportCompatible(account, OpenAIUpstreamTransportResponsesWebsocketV2Ingress))
}

func int64PtrForTest(v int64) *int64 {
	return &v
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_StickyWeightedFallbackSkipsOutOfGroupStickyAccount(t *testing.T) {

	ctx := context.Background()
	groupID := int64(101081)
	otherGroupID := int64(101082)
	accounts := []Account{
		{
			ID:          38001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    10,
			GroupIDs:    []int64{groupID},
		},
		{
			// 会话粘连绑定指向的账号已被移出请求分组（绑定 TTL 内账号改组的场景）。
			ID:          38002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{otherGroupID},
		},
	}
	cfg := &config.Config{}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{
		"openai:session_weighted_out_of_group": 38002,
	}}
	concurrencyCache := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{38001: false, 38002: true},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_weighted_out_of_group",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	// 组内唯一候选 38001 满并发：必须返回其等待计划，绝不能把请求泄漏到组外的粘连账号 38002。
	require.Equal(t, int64(38001), selection.Account.ID)
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(38001), selection.WaitPlan.AccountID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	// 失效的粘连绑定应被清理，避免后续请求反复走同一条泄漏路径。
	require.Positive(t, cache.deletedSessions["openai:session_weighted_out_of_group"])
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SubscriptionPriorityWaitsOnBusySubscriptionWhenRegularUnusable(t *testing.T) {

	ctx := context.Background()
	groupID := int64(101091)
	accounts := []Account{
		{
			// 订阅账号：支持 compact，但并发已满（busy-but-waitable）。
			ID:          38011,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			GroupIDs:    []int64{groupID},
			Credentials: map[string]any{"plan_type": "team"},
			Extra:       map[string]any{"openai_compact_supported": true},
		},
		{
			// 常规账号：明确不支持 compact，无法服务本次请求。
			ID:          38012,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    9,
			GroupIDs:    []int64{groupID},
			Extra:       map[string]any{"openai_compact_supported": false},
		},
	}
	concurrencyCache := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{38011: false, 38012: true},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                newSchedulerTestSubscriptionPriorityConfig(),
		rateLimitService:   newOpenAISelectionRateLimitService(),
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_subscription_wait",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
		true,
	)
	// 常规池无可用候选时，忙碌的订阅账号应产生等待计划，而不是直接返回 no available accounts。
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(38011), selection.Account.ID)
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(38011), selection.WaitPlan.AccountID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LegacyAccountModelRouteExcludesNonOwner(t *testing.T) {

	ctx := context.Background()
	groupID := int64(1010713)
	owner := Account{
		ID: 371031, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{"model_mapping": map[string]any{"team-alias": "gpt-5.1"}},
		// WS transport must be resolvable for the websocket sub-case so the
		// ownership filter, not transport admission, is the behavior under test.
		Extra: map[string]any{"responses_websockets_v2_enabled": true},
	}
	nonOwner := Account{
		ID: 371032, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		GroupIDs: []int64{groupID},
		Extra:    map[string]any{"responses_websockets_v2_enabled": true},
	}
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{owner, nonOwner}}
	ownership, err := (&GatewayService{accountRepo: repo}).resolveCompositeModelOwnership(ctx, groupID, "team-alias")
	require.NoError(t, err)
	require.Equal(t, CompositeModelOwnership{TargetPlatform: PlatformOpenAI, Matched: true}, ownership)
	ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceAccount, GroupID: groupID,
		PublicModel: "team-alias", TargetPlatform: ownership.TargetPlatform, UpstreamModel: "team-alias",
	})

	for _, tt := range []struct {
		name              string
		requiredTransport OpenAIUpstreamTransport
		sessionHash       string
		sessionBindings   map[string]int64
	}{
		{name: "initial filter excludes non owner", requiredTransport: OpenAIUpstreamTransportAny},
		{name: "initial filter excludes non owner for websocket transport", requiredTransport: OpenAIUpstreamTransportResponsesWebsocket},
		{name: "non owner sticky hit is rejected", requiredTransport: OpenAIUpstreamTransportAny, sessionHash: "non-owner", sessionBindings: map[string]int64{"openai:non-owner": nonOwner.ID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsockets = true
			svc := &OpenAIGatewayService{
				accountRepo:        repo,
				cache:              &schedulerTestGatewayCache{sessionBindings: tt.sessionBindings},
				cfg:                cfg,
				rateLimitService:   newOpenAISelectionRateLimitService(),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}

			selection, _, selectErr := svc.SelectAccountWithScheduler(
				ctx, &groupID, "", tt.sessionHash, "team-alias", nil, tt.requiredTransport, false,
			)
			require.NoError(t, selectErr)
			require.NotNil(t, selection)
			require.NotNil(t, selection.Account)
			require.Equal(t, owner.ID, selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LegacyAccountModelRouteDBRecheckFailsOverToOwner(t *testing.T) {

	ctx := context.Background()
	groupID := int64(1010714)
	mapping := map[string]any{"model_mapping": map[string]any{"team-alias": "gpt-5.1"}}
	stalePrimary := &Account{
		ID: 371041, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		GroupIDs: []int64{groupID}, Credentials: mapping,
	}
	staleBackup := &Account{
		ID: 371042, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
		GroupIDs: []int64{groupID}, Credentials: mapping,
	}
	dbPrimary := *stalePrimary
	dbPrimary.Credentials = nil
	dbBackup := *staleBackup
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{dbPrimary, dbBackup}}
	ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceAccount, GroupID: groupID,
		PublicModel: "team-alias", TargetPlatform: PlatformOpenAI, UpstreamModel: "team-alias",
	})
	acquiredIDs, releasedIDs := []int64{}, []int64{}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		cfg:              cfg,
		rateLimitService: newOpenAISelectionRateLimitService(),
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
			snapshotAccounts: []*Account{stalePrimary, staleBackup},
			accountsByID:     map[int64]*Account{stalePrimary.ID: stalePrimary, staleBackup.ID: staleBackup},
		}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquiredIDs: &acquiredIDs, releasedIDs: &releasedIDs,
		}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "team-alias", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, dbBackup.ID, selection.Account.ID)
	if containsInt64(acquiredIDs, dbPrimary.ID) {
		require.Contains(t, releasedIDs, dbPrimary.ID)
	}
	require.Contains(t, acquiredIDs, dbBackup.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}
