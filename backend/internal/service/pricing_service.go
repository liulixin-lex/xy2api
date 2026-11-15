package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/util/urlvalidator"
)

var (
	openAIModelDatePattern = regexp.MustCompile(`-\d{8}$`)
	openAIModelBasePattern = regexp.MustCompile(`^(gpt-\d+(?:\.\d+)?)(?:-|$)`)
	// aboveTierPricePattern 匹配 LiteLLM 长上下文绝对价字段名
	// （input_cost_per_token_above_272k_tokens / output_cost_per_token_above_200k_tokens 等）。
	// 带 _flex/_priority 服务档后缀的变体与 cache 侧字段不参与阈值/倍率折算。
	aboveTierPricePattern = regexp.MustCompile(`^(input|output)_cost_per_token_above_(\d+)k_tokens$`)
	// cacheTierPricePattern 匹配 cache 侧的长上下文绝对价字段名
	// （cache_creation_input_token_cost_above_200k_tokens、cache_read_input_token_cost_above_272k_tokens_priority、
	// cache_creation_input_token_cost_above_1hr_above_200k_tokens 等）。
	// 组 1 为基础价字段名主干，组 2 为 1h 缓存时长段（可为空），组 3 为服务档后缀（可为空）。
	cacheTierPricePattern = regexp.MustCompile(`^(cache_(?:creation|read)_input_token_cost)(_above_1hr)?_above_\d+k_tokens((?:_[a-z]+)?)$`)
	// Official GPT Image 2.5 token rates (2026-09-08):
	// https://developers.openai.com/api/docs/pricing#image-generation-models
	openAIGPTImage25FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       5e-06,
		CacheReadInputTokenCost: 1.25e-06,
		InputCostPerImageToken:  8e-06, CacheReadInputImageTokenCost: 2e-06,
		OutputCostPerImageToken: 3e-05,
		LiteLLMProvider:         "openai",
		Mode:                    "image_generation",
		SupportsPromptCaching:   true,
	}
	openAIGPT54FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       2.5e-06, // $2.5 per MTok
		OutputCostPerToken:      1.5e-05, // $15 per MTok
		CacheReadInputTokenCost: 2.5e-07, // $0.25 per MTok
		LiteLLMProvider:         "openai",
		Mode:                    "chat",
		SupportsPromptCaching:   true,
	}
	openAIGPT6AstraFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   1e-05,
		InputCostPerTokenPriority:           2e-05,
		OutputCostPerToken:                  5e-05,
		OutputCostPerTokenPriority:          1e-04,
		CacheCreationInputTokenCost:         1.25e-05,
		CacheCreationInputTokenCostPriority: 2.5e-05,
		CacheReadInputTokenCost:             1e-06,
		CacheReadInputTokenCostPriority:     2e-06,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT61SolFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   2e-6,
		InputCostPerTokenPriority:           4e-6,
		OutputCostPerToken:                  10e-6,
		OutputCostPerTokenPriority:          20e-6,
		CacheCreationInputTokenCost:         2.5e-6,
		CacheCreationInputTokenCostPriority: 5e-6,
		CacheReadInputTokenCost:             0.1e-6,
		CacheReadInputTokenCostPriority:     0.2e-6,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT6SolFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   2e-6,
		InputCostPerTokenPriority:           4e-6,
		OutputCostPerToken:                  10e-6,
		OutputCostPerTokenPriority:          20e-6,
		CacheCreationInputTokenCost:         2.5e-6,
		CacheCreationInputTokenCostPriority: 5e-6,
		CacheReadInputTokenCost:             0.2e-6,
		CacheReadInputTokenCostPriority:     0.4e-6,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT6LunaFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   0.1e-6,
		InputCostPerTokenPriority:           0.2e-6,
		OutputCostPerToken:                  0.5e-6,
		OutputCostPerTokenPriority:          1e-6,
		CacheCreationInputTokenCost:         0.125e-6,
		CacheCreationInputTokenCostPriority: 0.25e-6,
		CacheReadInputTokenCost:             0.01e-6,
		CacheReadInputTokenCostPriority:     0.02e-6,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	claudeOpus55FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken: 4e-6, OutputCostPerToken: 20e-6,
		CacheCreationInputTokenCost: 5e-6, CacheCreationInputTokenCostAbove1hr: 8e-6,
		CacheReadInputTokenCost:   0.2e-6,
		InputCostPerTokenPriority: 8e-6, OutputCostPerTokenPriority: 40e-6,
		CacheCreationInputTokenCostPriority: 10e-6, CacheReadInputTokenCostPriority: 0.4e-6,
		SupportsServiceTier: true, LiteLLMProvider: "anthropic", Mode: "chat", SupportsPromptCaching: true,
	}
	claudeSonnet55FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken: 2e-6, OutputCostPerToken: 10e-6,
		CacheCreationInputTokenCost: 2.5e-6, CacheCreationInputTokenCostAbove1hr: 4e-6,
		CacheReadInputTokenCost: 0.1e-6,
		LiteLLMProvider:         "anthropic", Mode: "chat", SupportsPromptCaching: true,
	}
	// Haiku 5.5 has two rate cards chosen by prompt length: $0.10/$0.50 up to
	// 100K prompt tokens, $0.50/$2.50 above. Cache reads and writes follow the
	// input rate (0.1x, 1.25x for 5m, 2x for 1h).
	claudeHaiku55FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken: 0.1e-6, OutputCostPerToken: 0.5e-6,
		CacheCreationInputTokenCost: 0.125e-6, CacheCreationInputTokenCostAbove1hr: 0.2e-6,
		CacheReadInputTokenCost:         0.01e-6,
		LongContextInputTokenThreshold:  100_000,
		LongContextInputCostMultiplier:  5,
		LongContextOutputCostMultiplier: 5,
		LiteLLMProvider:                 "anthropic", Mode: "chat", SupportsPromptCaching: true,
	}
	openAIGPT56SolFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   5e-06,
		InputCostPerTokenPriority:           1e-05,
		OutputCostPerToken:                  3e-05,
		OutputCostPerTokenPriority:          6e-05,
		CacheCreationInputTokenCost:         6.25e-06,
		CacheCreationInputTokenCostPriority: 1.25e-05,
		CacheReadInputTokenCost:             5e-07,
		CacheReadInputTokenCostPriority:     1e-06,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT56TerraFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   2e-06,
		InputCostPerTokenPriority:           4e-06,
		OutputCostPerToken:                  1.2e-05,
		OutputCostPerTokenPriority:          2.4e-05,
		CacheCreationInputTokenCost:         2.5e-06,
		CacheCreationInputTokenCostPriority: 5e-06,
		CacheReadInputTokenCost:             2e-07,
		CacheReadInputTokenCostPriority:     4e-07,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT56LunaFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   2e-07,
		InputCostPerTokenPriority:           4e-07,
		OutputCostPerToken:                  1.2e-06,
		OutputCostPerTokenPriority:          2.4e-06,
		CacheCreationInputTokenCost:         2.5e-07,
		CacheCreationInputTokenCostPriority: 5e-07,
		CacheReadInputTokenCost:             2e-08,
		CacheReadInputTokenCostPriority:     4e-08,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT54MiniFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       7.5e-07,
		OutputCostPerToken:      4.5e-06,
		CacheReadInputTokenCost: 7.5e-08,
		LiteLLMProvider:         "openai",
		Mode:                    "chat",
		SupportsPromptCaching:   true,
	}
	openAIGPT54NanoFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       2e-07,
		OutputCostPerToken:      1.25e-06,
		CacheReadInputTokenCost: 2e-08,
		LiteLLMProvider:         "openai",
		Mode:                    "chat",
		SupportsPromptCaching:   true,
	}
)

// LiteLLMModelPricing LiteLLM价格数据结构
// 只保留我们需要的字段，使用指针来处理可能缺失的值
type LiteLLMModelPricing struct {
	CacheCreationInputTokenCostExplicit bool    `json:"-"`
	InputCostPerToken                   float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority           float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                  float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority          float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost         float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      int     `json:"long_context_input_token_threshold,omitempty"`
	LongContextInputCostMultiplier      float64 `json:"long_context_input_cost_multiplier,omitempty"`
	LongContextOutputCostMultiplier     float64 `json:"long_context_output_cost_multiplier,omitempty"`
	SupportsServiceTier                 bool    `json:"supports_service_tier"`
	LiteLLMProvider                     string  `json:"litellm_provider"`
	Mode                                string  `json:"mode"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	OutputCostPerImage                  float64 `json:"output_cost_per_image"`       // 图片生成模型每张图片价格
	OutputCostPerImageToken             float64 `json:"output_cost_per_image_token"` // 图片输出 token 价格
	InputCostPerImageToken              float64 `json:"input_cost_per_image_token"`  // 图片输入 token 价格（如 gpt-image-2 图片编辑）
	CacheReadInputImageTokenCost        float64 `json:"cache_read_input_image_token_cost"`

	// TokenPricingAbsent 表示源数据中 input/output token 价格均缺失（仅有图片价）。
	// 此类条目只可用于图片计费，token 计费必须回退到 fallback 或 fail-closed，
	// 否则 token 流量会被按 $0 计费。零值（false）表示条目具备 token 价格。
	TokenPricingAbsent bool `json:"-"`
}

// PricingRemoteClient 远程价格数据获取接口
type PricingRemoteClient interface {
	FetchPricingJSON(ctx context.Context, url string) ([]byte, error)
	FetchHashText(ctx context.Context, url string) (string, error)
}

// LiteLLMRawEntry 用于解析原始JSON数据
type LiteLLMRawEntry struct {
	InputCostPerToken                   *float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority           *float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                  *float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority          *float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost         *float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority *float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr *float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             *float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     *float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      *int     `json:"long_context_input_token_threshold"`
	LongContextInputCostMultiplier      *float64 `json:"long_context_input_cost_multiplier"`
	LongContextOutputCostMultiplier     *float64 `json:"long_context_output_cost_multiplier"`
	SupportsServiceTier                 bool     `json:"supports_service_tier"`
	LiteLLMProvider                     string   `json:"litellm_provider"`
	Mode                                string   `json:"mode"`
	SupportsPromptCaching               bool     `json:"supports_prompt_caching"`
	OutputCostPerImage                  *float64 `json:"output_cost_per_image"`
	OutputCostPerImageToken             *float64 `json:"output_cost_per_image_token"`
	InputCostPerImageToken              *float64 `json:"input_cost_per_image_token"`
	CacheReadInputImageTokenCost        *float64 `json:"cache_read_input_image_token_cost"`
}

// PricingService 动态价格服务
type PricingService struct {
	cfg          *config.Config
	remoteClient PricingRemoteClient
	mu           sync.RWMutex
	pricingData  map[string]*LiteLLMModelPricing
	lastUpdated  time.Time
	localHash    string
	// fallback/override 文件在最近一次成功重建时的内容指纹，定时器据此判断是否
	// 需要从本地目录缓存重建叠加层。
	customFilesHash string
	pricingOrigins  map[string]PricingProvenance

	// 停止信号
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewPricingService 创建价格服务
func NewPricingService(cfg *config.Config, remoteClient PricingRemoteClient) *PricingService {
	s := &PricingService{
		cfg:          cfg,
		remoteClient: remoteClient,
		pricingData:  make(map[string]*LiteLLMModelPricing),
		stopCh:       make(chan struct{}),
	}
	return s
}

// Initialize 初始化价格服务
func (s *PricingService) Initialize() error {
	// 确保数据目录存在
	if err := os.MkdirAll(s.cfg.Pricing.DataDir, 0755); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to create data directory: %v", err)
	}

	// 首次加载价格数据
	if err := s.checkAndUpdatePricing(); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Initial load failed, using fallback: %v", err)
		if err := s.useFallbackPricing(); err != nil {
			return fmt.Errorf("failed to load pricing data: %w", err)
		}
	}

	// 启动定时更新
	s.startUpdateScheduler()

	logger.LegacyPrintf("service.pricing", "[Pricing] Service initialized with %d models", len(s.pricingData))
	return nil
}

// Stop 停止价格服务
func (s *PricingService) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Service stopped")
}

// startUpdateScheduler 启动定时调度器：每个周期先做远程目录哈希同步（配置了 remote_url 时），
// 再比对 fallback/override 文件指纹做本地热重载（配置了任一文件时）。两者都未配置则不启动。
func (s *PricingService) startUpdateScheduler() {
	if s == nil || s.cfg == nil {
		return
	}
	remoteEnabled := strings.TrimSpace(s.cfg.Pricing.RemoteURL) != ""
	watchCustom := s.hasCustomPricingFiles()
	if !remoteEnabled {
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Remote sync disabled: pricing remote URL is empty")
	}
	if !remoteEnabled && !watchCustom {
		return
	}

	hashInterval := time.Duration(s.cfg.Pricing.HashCheckIntervalMinutes) * time.Minute
	if hashInterval < time.Minute {
		hashInterval = 10 * time.Minute
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(hashInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if remoteEnabled {
					if err := s.syncWithRemote(); err != nil {
						logger.LegacyPrintf("service.pricing", "[Pricing] Sync failed: %v", err)
					}
				}
				if watchCustom {
					s.reloadIfCustomFilesChanged()
				}
			case <-s.stopCh:
				return
			}
		}
	}()

	logger.LegacyPrintf("service.pricing", "[Pricing] Update scheduler started (check every %v, remote sync=%t, custom file watch=%t)", hashInterval, remoteEnabled, watchCustom)
}

// checkAndUpdatePricing 检查并更新价格数据
func (s *PricingService) checkAndUpdatePricing() error {
	pricingFile := s.getPricingFilePath()

	// 检查本地文件是否存在
	if _, err := os.Stat(pricingFile); os.IsNotExist(err) {
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Local pricing file not found, downloading...")
		return s.downloadPricingData()
	}

	// 先加载本地文件（确保服务可用），再检查是否需要更新
	if err := s.loadPricingData(pricingFile); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load local file, downloading: %v", err)
		return s.downloadPricingData()
	}

	// 如果配置了哈希URL，通过远程哈希检查是否有更新
	if s.cfg.Pricing.HashURL != "" {
		remoteHash, err := s.fetchRemoteHash()
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash on startup: %v", err)
			return nil // 已加载本地文件，哈希获取失败不影响启动
		}

		s.mu.RLock()
		localHash := s.localHash
		s.mu.RUnlock()

		if localHash == "" || !strings.EqualFold(remoteHash, localHash) {
			logger.LegacyPrintf("service.pricing", "[Pricing] Remote hash differs on startup (local=%s remote=%s), downloading...",
				localHash[:min(8, len(localHash))], remoteHash[:min(8, len(remoteHash))])
			if err := s.downloadPricingData(); err != nil {
				logger.LegacyPrintf("service.pricing", "[Pricing] Download failed, using existing file: %v", err)
			}
		}
		return nil
	}

	// 没有哈希URL时，基于文件年龄检查
	info, err := os.Stat(pricingFile)
	if err != nil {
		return nil // 已加载本地文件
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		logger.LegacyPrintf("service.pricing", "[Pricing] Local file is %v old, updating...", fileAge.Round(time.Hour))
		if err := s.downloadPricingData(); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Download failed, using existing file: %v", err)
		}
	}

	return nil
}

// syncWithRemote 与远程同步（基于哈希校验）
func (s *PricingService) syncWithRemote() error {
	// 如果配置了哈希URL，从远程获取哈希进行比对
	if s.cfg.Pricing.HashURL != "" {
		remoteHash, err := s.fetchRemoteHash()
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash: %v", err)
			return nil // 哈希获取失败不影响正常使用
		}

		s.mu.RLock()
		localHash := s.localHash
		s.mu.RUnlock()

		if localHash == "" || !strings.EqualFold(remoteHash, localHash) {
			logger.LegacyPrintf("service.pricing", "[Pricing] Remote hash differs (local=%s remote=%s), downloading new version...",
				localHash[:min(8, len(localHash))], remoteHash[:min(8, len(remoteHash))])
			return s.downloadPricingData()
		}
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Hash check passed, no update needed")
		return nil
	}

	// 没有哈希URL时，基于时间检查
	pricingFile := s.getPricingFilePath()
	info, err := os.Stat(pricingFile)
	if err != nil {
		return s.downloadPricingData()
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		logger.LegacyPrintf("service.pricing", "[Pricing] File is %v old, downloading...", fileAge.Round(time.Hour))
		return s.downloadPricingData()
	}

	return nil
}

// hasCustomPricingFiles 报告是否配置了 fallback/override 任一文件路径（不要求文件存在）。
func (s *PricingService) hasCustomPricingFiles() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	return strings.TrimSpace(s.cfg.Pricing.FallbackFile) != "" || strings.TrimSpace(s.cfg.Pricing.OverrideFile) != ""
}

// customPricingFilesFingerprint 返回 fallback、override 两个文件当前内容的联合 sha256。
// 每个文件以"长度前缀 + 正文"参与计算，不可读的文件按空正文处理；未配置任何文件返回空串。
func (s *PricingService) customPricingFilesFingerprint() string {
	if !s.hasCustomPricingFiles() {
		return ""
	}
	h := sha256.New()
	for _, path := range []string{s.cfg.Pricing.FallbackFile, s.cfg.Pricing.OverrideFile} {
		var body []byte
		if p := strings.TrimSpace(path); p != "" {
			body, _ = os.ReadFile(p)
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(body)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validateCustomPricingFiles 要求已配置的 fallback/override 文件可读且为 JSON 对象。
func (s *PricingService) validateCustomPricingFiles() error {
	for _, path := range []string{s.cfg.Pricing.FallbackFile, s.cfg.Pricing.OverrideFile} {
		p := strings.TrimSpace(path)
		if p == "" {
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(body, &entries); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if entries == nil {
			return fmt.Errorf("%s: pricing root must be an object", p)
		}
	}
	return nil
}

// reloadIfCustomFilesChanged 比对 fallback/override 文件指纹，与最近一次重建时不同则从
// 本地目录缓存重建内存数据。文件缺失、不可读或不是
// JSON 对象时保留当前数据且不更新指纹，下一轮会再次尝试并重复告警。目录正文与远程同步
// 锚点(localHash)不受本路径影响。
func (s *PricingService) reloadIfCustomFilesChanged() {
	fingerprint := s.customPricingFilesFingerprint()
	s.mu.RLock()
	unchanged := fingerprint == s.customFilesHash
	s.mu.RUnlock()
	if unchanged {
		return
	}
	if err := s.reloadCustomPricingLayers(); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Custom pricing file changed but reload failed: %v", err)
	}
}

// reloadCustomPricingLayers 读取本地目录缓存并重新叠加 fallback/override，只替换内存数据
// 与叠加层指纹。
func (s *PricingService) reloadCustomPricingLayers() error {
	pricingFile := s.getPricingFilePath()
	// 定价层文件可能在读取期间被替换。只有构建前后指纹一致时才提交，
	// 否则丢弃这次混合快照并重试，避免短暂应用不匹配的 fallback/override。
	var data map[string]*LiteLLMModelPricing
	var fingerprint string
	var origins map[string]PricingProvenance
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if validateErr := s.validateCustomPricingFiles(); validateErr != nil {
			return fmt.Errorf("validate custom pricing files: %w", validateErr)
		}
		before := s.customPricingFilesFingerprint()
		body, readErr := os.ReadFile(pricingFile)
		if readErr != nil {
			return fmt.Errorf("read file failed: %w", readErr)
		}
		data, fingerprint, origins, err = s.buildPricingDataWithSource(body)
		if err != nil {
			return fmt.Errorf("parse pricing data: %w", err)
		}
		after := s.customPricingFilesFingerprint()
		if validateErr := s.validateCustomPricingFiles(); validateErr != nil {
			return fmt.Errorf("validate custom pricing files: %w", validateErr)
		}
		if before == after && after == fingerprint {
			break
		}
		if attempt == 2 {
			return fmt.Errorf("custom pricing files changed during reload")
		}
	}

	s.mu.Lock()
	warnDroppedLongContextLadders(s.pricingData, data)
	s.pricingData = data
	s.pricingOrigins = origins
	s.customFilesHash = fingerprint
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Custom pricing files changed, reloaded %d models from %s", len(data), pricingFile)
	return nil
}

// downloadPricingData 从远程下载价格数据
func (s *PricingService) downloadPricingData() error {
	remoteURL, err := s.validatePricingURL(s.cfg.Pricing.RemoteURL)
	if err != nil {
		return err
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Downloading from %s", remoteURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 获取远程哈希（用于同步锚点，不作为完整性校验）
	var remoteHash string
	if strings.TrimSpace(s.cfg.Pricing.HashURL) != "" {
		remoteHash, err = s.fetchRemoteHash()
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash (continuing): %v", err)
		}
	}

	body, err := s.remoteClient.FetchPricingJSON(ctx, remoteURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// 哈希校验：不匹配时仅告警，不阻止更新
	// 远程哈希文件可能与数据文件不同步（如维护者更新了数据但未更新哈希文件）
	dataHash := sha256.Sum256(body)
	dataHashStr := hex.EncodeToString(dataHash[:])
	if remoteHash != "" && !strings.EqualFold(remoteHash, dataHashStr) {
		logger.LegacyPrintf("service.pricing", "[Pricing] Hash mismatch warning: remote=%s data=%s (hash file may be out of sync)",
			remoteHash[:min(8, len(remoteHash))], dataHashStr[:8])
	}

	data, customFilesHash, origins, err := s.buildPricingDataWithSource(body)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}

	// 同步锚点只确认实际下载正文；远端 hash 提前发布时下一轮仍须下载。
	syncHash := dataHashStr
	if strings.EqualFold(remoteHash, dataHashStr) {
		syncHash = strings.ToLower(remoteHash)
	}
	if err := s.savePricingFiles(body, syncHash); err != nil {
		return err
	}

	// 更新内存数据
	s.mu.Lock()
	warnDroppedLongContextLadders(s.pricingData, data)
	s.pricingData = data
	s.pricingOrigins = origins
	s.lastUpdated = time.Now()
	s.localHash = syncHash
	s.customFilesHash = customFilesHash
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Downloaded %d models successfully", len(data))
	return nil
}

// parsePricingData 解析价格数据（处理各种格式）
func (s *PricingService) parsePricingData(body []byte) (map[string]*LiteLLMModelPricing, error) {
	// 首先解析为 map[string]json.RawMessage
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, fmt.Errorf("parse raw JSON: %w", err)
	}
	if rawData == nil {
		return nil, fmt.Errorf("pricing root must be an object")
	}
	var err error
	rawData, err = s.applyPricingOverrides(rawData)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*LiteLLMModelPricing)
	var orphanCacheTiers, lopsidedLadders []string

	for modelName, rawEntry := range rawData {
		// 跳过 sample_spec 等文档条目
		if modelName == "sample_spec" {
			continue
		}

		// 尝试解析每个条目
		var entry LiteLLMRawEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			return nil, fmt.Errorf("pricing %q: %w", modelName, err)
		}
		if err := validatePricingEntry(rawEntry, entry); err != nil {
			return nil, fmt.Errorf("pricing %q: %w", modelName, err)
		}

		// 只保留有有效价格的条目
		if entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil && entry.OutputCostPerImage == nil && entry.OutputCostPerImageToken == nil && entry.InputCostPerImageToken == nil {
			continue
		}

		pricing := &LiteLLMModelPricing{
			LiteLLMProvider:       entry.LiteLLMProvider,
			Mode:                  entry.Mode,
			SupportsPromptCaching: entry.SupportsPromptCaching,
			SupportsServiceTier:   entry.SupportsServiceTier,
			TokenPricingAbsent:    entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil,
		}

		if entry.InputCostPerToken != nil {
			pricing.InputCostPerToken = *entry.InputCostPerToken
		}
		if entry.InputCostPerTokenPriority != nil {
			pricing.InputCostPerTokenPriority = *entry.InputCostPerTokenPriority
		}
		if entry.OutputCostPerToken != nil {
			pricing.OutputCostPerToken = *entry.OutputCostPerToken
		}
		if entry.OutputCostPerTokenPriority != nil {
			pricing.OutputCostPerTokenPriority = *entry.OutputCostPerTokenPriority
		}
		if entry.CacheCreationInputTokenCost != nil {
			pricing.CacheCreationInputTokenCost = *entry.CacheCreationInputTokenCost
			pricing.CacheCreationInputTokenCostExplicit = true
		}
		if entry.CacheCreationInputTokenCostPriority != nil {
			pricing.CacheCreationInputTokenCostPriority = *entry.CacheCreationInputTokenCostPriority
		}
		if entry.CacheCreationInputTokenCostAbove1hr != nil {
			pricing.CacheCreationInputTokenCostAbove1hr = *entry.CacheCreationInputTokenCostAbove1hr
		}
		if entry.CacheReadInputTokenCost != nil {
			pricing.CacheReadInputTokenCost = *entry.CacheReadInputTokenCost
		}
		if entry.CacheReadInputTokenCostPriority != nil {
			pricing.CacheReadInputTokenCostPriority = *entry.CacheReadInputTokenCostPriority
		}
		if entry.LongContextInputTokenThreshold != nil {
			pricing.LongContextInputTokenThreshold = *entry.LongContextInputTokenThreshold
		}
		if entry.LongContextInputCostMultiplier != nil {
			pricing.LongContextInputCostMultiplier = *entry.LongContextInputCostMultiplier
		}
		if entry.LongContextOutputCostMultiplier != nil {
			pricing.LongContextOutputCostMultiplier = *entry.LongContextOutputCostMultiplier
		}
		if entry.OutputCostPerImage != nil {
			pricing.OutputCostPerImage = *entry.OutputCostPerImage
		}
		if entry.OutputCostPerImageToken != nil {
			pricing.OutputCostPerImageToken = *entry.OutputCostPerImageToken
		}
		if entry.InputCostPerImageToken != nil {
			pricing.InputCostPerImageToken = *entry.InputCostPerImageToken
		}
		if entry.CacheReadInputImageTokenCost != nil {
			pricing.CacheReadInputImageTokenCost = *entry.CacheReadInputImageTokenCost
		}

		hasExplicitLongContext := entry.LongContextInputTokenThreshold != nil ||
			entry.LongContextInputCostMultiplier != nil ||
			entry.LongContextOutputCostMultiplier != nil
		if !hasExplicitLongContext {
			deriveLongContextFromAboveTierFields(rawEntry, pricing)
			if isLopsidedLongContextLadder(pricing) {
				lopsidedLadders = append(lopsidedLadders, fmt.Sprintf("%s(input x%.2f, output x%.2f)", modelName,
					pricing.LongContextInputCostMultiplier, pricing.LongContextOutputCostMultiplier))
			}
		}
		if orphans := orphanCacheTierFields(rawEntry); len(orphans) > 0 {
			orphanCacheTiers = append(orphanCacheTiers, modelName+"("+strings.Join(orphans, ",")+")")
		}

		result[modelName] = pricing
	}

	warnOrphanCacheTierFields(orphanCacheTiers)
	warnLopsidedLongContextLadders(lopsidedLadders)

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid pricing entries found")
	}

	return result, nil
}

// deriveLongContextFromAboveTierFields 把 LiteLLM 目录的 *_above_XXXk_tokens 绝对价字段
// 折算成 long_context_* 阈值+倍率（sub2api 计费机制的内部表达）：阈值取自字段名，
// 倍率 = above 价 ÷ 基础价。条目显式携带任一 long_context_* 字段（含显式 0）时由
// 调用方跳过折算，以显式配置为准——显式写 threshold=0 或 multiplier=1 均可关闭该
// 模型的阶梯。多个阈值并存时取最小阈值。
// cache_read/cache_creation 的 above 档在计费中统一跟随输入倍率，不单独折算：
// 目录条目的 cache above 档须恰为基础价 × 输入倍率；缺基础价的 cache above 字段
// 无法参与计费，由 orphanCacheTierFields 哨兵告警。
func deriveLongContextFromAboveTierFields(rawEntry json.RawMessage, pricing *LiteLLMModelPricing) {
	if pricing == nil ||
		pricing.LongContextInputTokenThreshold > 0 ||
		pricing.LongContextInputCostMultiplier > 0 ||
		pricing.LongContextOutputCostMultiplier > 0 {
		return
	}
	if !bytes.Contains(rawEntry, []byte("_above_")) {
		return
	}
	var fields map[string]any
	if err := json.Unmarshal(rawEntry, &fields); err != nil {
		return
	}
	type tierPrices struct{ input, output float64 }
	tiers := make(map[int]*tierPrices)
	for key, value := range fields {
		m := aboveTierPricePattern.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		price, ok := value.(float64)
		if !ok || price <= 0 {
			continue
		}
		thousands, err := strconv.Atoi(m[2])
		if err != nil || thousands <= 0 {
			continue
		}
		threshold := thousands * 1000
		tp := tiers[threshold]
		if tp == nil {
			tp = &tierPrices{}
			tiers[threshold] = tp
		}
		if m[1] == "input" {
			tp.input = price
		} else {
			tp.output = price
		}
	}
	if len(tiers) == 0 {
		return
	}
	threshold := 0
	for t := range tiers {
		if threshold == 0 || t < threshold {
			threshold = t
		}
	}
	tp := tiers[threshold]
	inputMultiplier, outputMultiplier := 1.0, 1.0
	if tp.input > 0 && pricing.InputCostPerToken > 0 {
		inputMultiplier = tp.input / pricing.InputCostPerToken
	}
	if tp.output > 0 && pricing.OutputCostPerToken > 0 {
		outputMultiplier = tp.output / pricing.OutputCostPerToken
	}
	// above 价不高于基础价时视为无附加费，不生成阶梯。
	if inputMultiplier <= 1 && outputMultiplier <= 1 {
		return
	}
	pricing.LongContextInputTokenThreshold = threshold
	pricing.LongContextInputCostMultiplier = inputMultiplier
	pricing.LongContextOutputCostMultiplier = outputMultiplier
}

// isLopsidedLongContextLadder 判断折算出的阶梯是否只有一侧带附加费。官方阶梯（OpenAI、
// Google、Anthropic、xAI）都同时抬高 input 与 output；单侧附加费意味着条目的基础价与
// above 档来自不同价格版本（如基础价被手工 pin、above 档随上游更新），折算出的倍率失真。
func isLopsidedLongContextLadder(pricing *LiteLLMModelPricing) bool {
	if pricing == nil || pricing.LongContextInputTokenThreshold <= 0 {
		return false
	}
	return (pricing.LongContextInputCostMultiplier > 1) != (pricing.LongContextOutputCostMultiplier > 1)
}

// warnLopsidedLongContextLadders 对单侧附加费的折算阶梯打 WARN：应成组修正该条目的
// 基础价与 above 档（目录或 pricing.override_file）。
func warnLopsidedLongContextLadders(entries []string) {
	if len(entries) == 0 {
		return
	}
	sort.Strings(entries)
	total := len(entries)
	if total > 20 {
		entries = append(entries[:20], "...")
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Warning: %d model(s) derive a one-sided long-context ladder (surcharge on only input or only output); base prices and above-tier prices likely come from different price versions: %s", total, strings.Join(entries, ", "))
}

// orphanCacheTierFields 返回条目中没有对应基础价的 cache 侧 above 档字段名。
// cache 侧 above 档不参与计费取值，计费按"基础价 × 输入倍率"；基础价缺失或为 0 时，
// 该缓存分项在整个阶梯上都按 0 计。计费对变体有回落：服务档变体（_priority/_flex）
// 缺自身基础价时用标准基础价，1h 缓存写入缺 above_1hr 价时全部按 5m 价——因此沿
// 回落链任一基础价存在即不算孤儿。
func orphanCacheTierFields(rawEntry json.RawMessage) []string {
	if !bytes.Contains(rawEntry, []byte("_above_")) {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(rawEntry, &fields); err != nil {
		return nil
	}
	positive := func(key string) bool {
		price, ok := fields[key].(float64)
		return ok && price > 0
	}
	var orphans []string
	for key := range fields {
		m := cacheTierPricePattern.FindStringSubmatch(key)
		if m == nil || !positive(key) {
			continue
		}
		stem, hourly, tier := m[1], m[2], m[3]
		if positive(stem+hourly+tier) || positive(stem+hourly) || positive(stem+tier) || positive(stem) {
			continue
		}
		orphans = append(orphans, key)
	}
	sort.Strings(orphans)
	return orphans
}

// warnOrphanCacheTierFields 对带 cache 侧 above 档却没有基础价的条目打 WARN：
// 该缓存分项按 0 计费，目录或 pricing.override_file 补上基础价即可消除。
func warnOrphanCacheTierFields(entries []string) {
	if len(entries) == 0 {
		return
	}
	sort.Strings(entries)
	total := len(entries)
	if total > 20 {
		entries = append(entries[:20], "...")
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Warning: %d model(s) carry cache above-tier prices without a base cache price; that cache item bills at $0 until the catalog/override supplies the base: %s", total, strings.Join(entries, ", "))
}

// applyPricingOverrides 把 override 文件的条目逐字段修补进原始目录数据。目录与回退
// 文件的解析都经过 parsePricingData，因此 override 是最高优先级的数据源。这里只修补
// 已存在的条目：目录/回退里都没有的模型由 mergeOverrideOnlyModels 在两层数据合并后
// 统一并入——若在此处抢先建条目，纯 override 条目会挡住回退文件中同名完整条目的合并。
func (s *PricingService) applyPricingOverrides(rawData map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	overrides, err := s.loadPricingOverrideEntries()
	if err != nil {
		return nil, err
	}
	for name, patch := range overrides {
		base, ok := rawData[name]
		if !ok {
			continue
		}
		merged, valid := mergePricingOverrideEntry(base, patch)
		if !valid {
			return nil, fmt.Errorf("override %q must be a JSON object", name)
		}
		rawData[name] = merged
	}
	return rawData, nil
}

// loadPricingOverrideEntries rejects unreadable or malformed configured overrides.
func (s *PricingService) loadPricingOverrideEntries() (map[string]json.RawMessage, error) {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.OverrideFile) == "" {
		return nil, nil
	}
	path := strings.TrimSpace(s.cfg.Pricing.OverrideFile)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing override: %w", err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parse pricing override: %w", err)
	}
	if entries == nil {
		return nil, fmt.Errorf("pricing override root must be an object")
	}
	for name, raw := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("override %q must be a JSON object", name)
		}
	}
	return entries, nil
}

// mergePricingOverrideEntry 在 JSON 字段层浅合并：patch 字段覆盖 base 同名字段，
// 值为 null 的 patch 字段从结果中删除，base 为空时结果即 patch 本身。
// patch 不是 JSON 对象时返回 ok=false。
func mergePricingOverrideEntry(base, patch json.RawMessage) (json.RawMessage, bool) {
	var patchFields map[string]any
	if err := json.Unmarshal(patch, &patchFields); err != nil || patchFields == nil {
		return nil, false
	}
	merged := make(map[string]any, len(patchFields))
	if len(base) > 0 {
		// 基础条目必须也是对象，不能用补丁掩盖损坏的目录。
		if err := json.Unmarshal(base, &merged); err != nil || merged == nil {
			return nil, false
		}
	}
	for k, v := range patchFields {
		if v == nil {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, false
	}
	return out, true
}

// mergeOverrideOnlyModels validates standalone overrides after catalog/fallback
// merging, so a partial patch can still inherit its required price dimensions.
func (s *PricingService) mergeOverrideOnlyModels(data map[string]*LiteLLMModelPricing) (map[string]*LiteLLMModelPricing, error) {
	overrides, err := s.loadPricingOverrideEntries()
	if err != nil {
		return nil, err
	}
	leftover := make(map[string]json.RawMessage)
	for name, patch := range overrides {
		if _, ok := data[name]; !ok {
			leftover[name] = patch
		}
	}
	if len(leftover) == 0 {
		return data, nil
	}
	body, err := json.Marshal(leftover)
	if err != nil {
		return nil, err
	}
	parsed, err := s.parsePricingData(body)
	if err != nil {
		return nil, fmt.Errorf("standalone pricing override: %w", err)
	}
	for name := range leftover {
		if _, ok := parsed[name]; !ok {
			return nil, fmt.Errorf("standalone override %q has no price fields", name)
		}
	}
	if data == nil {
		data = make(map[string]*LiteLLMModelPricing)
	}
	maps.Copy(data, parsed)
	return data, nil
}

// buildPricingData retains the existing parsing entry point for validation callers.
func (s *PricingService) buildPricingData(body []byte) (map[string]*LiteLLMModelPricing, string, error) {
	data, fingerprint, _, err := s.buildPricingDataWithSource(body)
	return data, fingerprint, err
}

// Publish the prices and their origins from one stable content revision.
func (s *PricingService) buildPricingDataWithSource(body []byte) (map[string]*LiteLLMModelPricing, string, map[string]PricingProvenance, error) {
	fingerprint := s.customPricingFilesFingerprint()
	data, err := s.parsePricingData(body)
	if err != nil {
		return nil, "", nil, err
	}
	origins := make(map[string]PricingProvenance, len(data))
	for key := range data {
		origins[key] = PricingProvenance{Source: "catalog"}
	}
	data, err = s.mergeFallbackPricingData(data)
	if err != nil {
		return nil, "", nil, err
	}
	for key := range data {
		if _, ok := origins[key]; !ok {
			origins[key] = PricingProvenance{Source: "fallback_file"}
		}
	}
	data, err = s.mergeOverrideOnlyModels(data)
	if err != nil {
		return nil, "", nil, err
	}
	overrides, err := s.loadPricingOverrideEntries()
	if err != nil {
		return nil, "", nil, err
	}
	for key := range data {
		source, ok := origins[key]
		if !ok {
			source = PricingProvenance{Source: "override_file"}
		}
		if patch := overrides[key]; len(patch) > 0 {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(patch, &fields); err != nil {
				return nil, "", nil, err
			}
			source.Overrides = map[string]string{}
			for field := range fields {
				if strings.Contains(field, "cost") || strings.Contains(field, "multiplier") || strings.Contains(field, "threshold") {
					source.Overrides[field] = "override_file:" + fingerprint
				}
			}
		}
		origins[key] = source
	}
	if fingerprint != s.customPricingFilesFingerprint() {
		return nil, "", nil, fmt.Errorf("custom pricing files changed during build")
	}
	return data, fingerprint, origins, nil
}

// loadPricingData 从本地文件加载价格数据
func (s *PricingService) loadPricingData(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file failed: %w", err)
	}

	pricingData, customFilesHash, origins, err := s.buildPricingDataWithSource(data)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}

	// 计算哈希
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	s.mu.Lock()
	warnDroppedLongContextLadders(s.pricingData, pricingData)
	s.pricingData = pricingData
	s.pricingOrigins = origins
	s.localHash = hashStr
	s.customFilesHash = customFilesHash

	info, _ := os.Stat(filePath)
	if info != nil {
		s.lastUpdated = info.ModTime()
	} else {
		s.lastUpdated = time.Now()
	}
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Loaded %d models from %s", len(pricingData), filePath)
	return nil
}

func (s *PricingService) mergeFallbackPricingData(data map[string]*LiteLLMModelPricing) (map[string]*LiteLLMModelPricing, error) {
	if data == nil {
		data = make(map[string]*LiteLLMModelPricing)
	}
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.FallbackFile) == "" {
		return data, nil
	}
	fallbackBody, err := os.ReadFile(s.cfg.Pricing.FallbackFile)
	if err != nil {
		return nil, fmt.Errorf("read pricing fallback: %w", err)
	}
	fallbackData, err := s.parsePricingData(fallbackBody)
	if err != nil {
		return nil, fmt.Errorf("parse pricing fallback: %w", err)
	}
	merged := 0
	for modelName, pricing := range fallbackData {
		if _, ok := data[modelName]; ok {
			continue
		}
		data[modelName] = pricing
		merged++
	}
	if merged > 0 {
		logger.LegacyPrintf("service.pricing", "[Pricing] Merged %d fallback-only models", merged)
	}
	return data, nil
}

// warnDroppedLongContextLadders 对比新旧目录数据：原本带长上下文阶梯的条目在新数据里
// 丢失阈值时打 WARN。阶梯已完全数据驱动（无代码兜底），数据源一次误提交就会把阶梯
// 静默变成基础价少收（07-14~08-21 漏收事故的形态），这里是唯一的哨兵。
// 调用方需持有 s.mu 写锁。
func warnDroppedLongContextLadders(old, next map[string]*LiteLLMModelPricing) {
	if len(old) == 0 {
		return
	}
	var dropped []string
	for name, prev := range old {
		if prev == nil || prev.LongContextInputTokenThreshold <= 0 {
			continue
		}
		if cur, ok := next[name]; ok && (cur == nil || cur.LongContextInputTokenThreshold <= 0) {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) == 0 {
		return
	}
	sort.Strings(dropped)
	total := len(dropped)
	if total > 20 {
		dropped = append(dropped[:20], "...")
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Long-context ladder dropped for %d model(s) after reload: %s (verify catalog/override data if unintended)", total, strings.Join(dropped, ", "))
}

// useFallbackPricing 使用回退价格文件
func (s *PricingService) useFallbackPricing() error {
	body, err := os.ReadFile(s.cfg.Pricing.FallbackFile)
	if err != nil {
		return fmt.Errorf("read fallback failed: %w", err)
	}
	data, fingerprint, origins, err := s.buildPricingDataWithSource(body)
	if err != nil {
		return fmt.Errorf("parse pricing fallback: %w", err)
	}
	hash := sha256.Sum256(body)
	syncHash := hex.EncodeToString(hash[:])
	if err := s.savePricingFiles(body, syncHash); err != nil {
		return err
	}
	s.mu.Lock()
	warnDroppedLongContextLadders(s.pricingData, data)
	s.pricingData = data
	s.pricingOrigins = origins
	s.customFilesHash = fingerprint
	s.localHash = syncHash
	s.lastUpdated = time.Now()
	s.mu.Unlock()
	return nil
}

// fetchRemoteHash 从远程获取哈希值
func (s *PricingService) fetchRemoteHash() (string, error) {
	hashURL, err := s.validatePricingURL(s.cfg.Pricing.HashURL)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hash, err := s.remoteClient.FetchHashText(ctx, hashURL)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}

func (s *PricingService) validatePricingURL(raw string) (string, error) {
	if s.cfg != nil && !s.cfg.Security.URLAllowlist.Enabled {
		normalized, err := urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid pricing url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.PricingHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid pricing url: %w", err)
	}
	return normalized, nil
}

// GetModelPricing 获取模型价格（带模糊匹配）
func (s *PricingService) GetModelPricing(modelName string) *LiteLLMModelPricing {
	pricing, _ := s.lookupModelPricingWithSource(modelName)
	return pricing
}

func (s *PricingService) lookupModelPricingWithSource(modelName string) (*LiteLLMModelPricing, PricingProvenance) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if strings.TrimSpace(modelName) == "" {
		return nil, PricingProvenance{}
	}
	candidates := s.buildModelLookupCandidates(strings.ToLower(strings.TrimSpace(modelName)))
	pricing, key, match := s.lookupIdentifiedModelPricingWithSourceLocked(candidates)
	if pricing == nil {
		pricing, key, match = s.matchByModelFamilyWithSource(candidates[0])
	}
	if pricing == nil && strings.HasPrefix(candidates[0], "gpt-") {
		pricing, key, match = s.matchOpenAIModelWithSource(candidates[0])
	}
	if pricing == nil {
		return nil, PricingProvenance{}
	}
	provenance := s.pricingOrigins[key]
	source := provenance.Source
	if source == "" {
		source = "catalog"
	}
	revision := ""
	if s.localHash != "" {
		revision = s.localHash + ":" + s.customFilesHash
	}
	if match == "static" {
		source = "builtin"
		revision = pricingContentRevision(pricing)
	}
	return pricing, PricingProvenance{ModelKey: key, Source: source, Match: match, Revision: revision, Overrides: maps.Clone(provenance.Overrides)}
}

// lookupIdentifiedModelPricingLocked 只做"确定性识别"的三步查找：精确键、已知拼写
// 变体、去掉日期/版本后缀后的同名条目。它刻意不包含 matchByModelFamily /
// matchOpenAIModel 这类按子串猜系列的兜底——那些兜底会给任意名字都返回一个价格。
// 调用方必须持有 s.mu 读锁。
func (s *PricingService) lookupIdentifiedModelPricingLocked(candidates []string) *LiteLLMModelPricing {
	pricing, _, _ := s.lookupIdentifiedModelPricingWithSourceLocked(candidates)
	return pricing
}
func (s *PricingService) lookupIdentifiedModelPricingWithSourceLocked(lookupCandidates []string) (*LiteLLMModelPricing, string, string) {
	if len(lookupCandidates) == 0 {
		return nil, "", ""
	}

	// 1. 精确匹配
	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := s.pricingData[candidate]; ok {
			return pricing, candidate, "exact"
		}
	}

	// 2. 处理常见的模型名称变体
	// claude-opus-4-5-20251101 -> claude-opus-4.5-20251101
	for _, candidate := range lookupCandidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing, ok := s.pricingData[normalized]; ok {
			return pricing, normalized, "variant"
		}
	}

	// 3. 尝试模糊匹配（去掉版本号后缀）
	// claude-opus-4-5-20251101 -> claude-opus-4.5
	baseName := s.extractBaseName(lookupCandidates[0])
	for key, pricing := range s.pricingData {
		keyBase := s.extractBaseName(strings.ToLower(key))
		if keyBase == baseName {
			return pricing, key, "base"
		}
	}

	return nil, "", ""
}

// GetIdentifiedModelPricing 在价格表中确定性地识别模型，识别不到时返回 nil。
// 与 GetModelPricing 的区别：不会退化成按 "opus"/"haiku" 之类子串猜出的系列兜底价。
// 用于必须区分"这是价格表里已知的模型"和"这只是名字里带某个关键词"的场景。
func (s *PricingService) GetIdentifiedModelPricing(modelName string) *LiteLLMModelPricing {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	if modelLower == "" {
		return nil
	}
	return s.lookupIdentifiedModelPricingLocked(s.buildModelLookupCandidates(modelLower))
}

func (s *PricingService) buildModelLookupCandidates(modelLower string) []string {
	rawCandidates := []string{
		modelLower,
		strings.TrimPrefix(modelLower, "models/"),
		lastSegment(modelLower),
		lastSegment(strings.TrimPrefix(modelLower, "models/")),
	}
	normalized := normalizeModelNameForPricing(modelLower)

	// A tier-specific entry should take precedence when the pricing catalog gains
	// one later. Antigravity's Gemini Flash thinking tiers share the base rate,
	// so the normalized base remains the fallback after the exact aliases.
	candidates := rawCandidates
	if normalizeGeminiThinkingTierAlias(lastSegment(modelLower)) != lastSegment(modelLower) {
		candidates = append(candidates, normalized)
	} else {
		// Prefer canonical model names for all other aliases (including models/xxx).
		candidates = append([]string{normalized}, candidates...)
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{modelLower}
	}
	return out
}

func normalizeModelNameForPricing(model string) string {
	// Common Gemini/VertexAI forms:
	// - models/gemini-2.0-flash-exp
	// - publishers/google/models/gemini-2.5-pro
	// - projects/.../locations/.../publishers/google/models/gemini-2.5-pro
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}
	if idx := strings.LastIndex(model, "/models/"); idx != -1 {
		model = model[idx+len("/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	if canonical := canonicalizeOpenAIModelAliasSpelling(model); canonical != "" {
		if canonical == "gpt-6" {
			return "gpt-6-astra"
		}
		if canonical == "gpt-5.6" {
			return "gpt-5.6-sol"
		}
		if suffix, ok := strings.CutPrefix(canonical, "gpt-5.6-"); ok && (suffix == "max" || isKnownCodexModelSuffix(suffix)) {
			return "gpt-5.6-sol"
		}
		return canonical
	}
	return normalizeGeminiThinkingTierAlias(model)
}

// normalizeGeminiThinkingTierAlias maps Antigravity's Gemini Flash
// thinking-tier model IDs to the public base model. The tier controls reasoning
// behavior, not the published token rate, so this keeps -high/-low/-medium and
// -tiered requests on the corresponding base model's price card.
func normalizeGeminiThinkingTierAlias(model string) string {
	for _, baseModel := range []string{"gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash"} {
		for _, tier := range []string{"-high", "-low", "-medium", "-tiered"} {
			if model == baseModel+tier {
				return baseModel
			}
		}
	}
	return model
}

func lastSegment(model string) string {
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

// extractBaseName 提取基础模型名称（去掉日期版本号）
func (s *PricingService) extractBaseName(model string) string {
	// 移除日期后缀 (如 -20251101, -20241022)
	parts := strings.Split(model, "-")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// 跳过看起来像日期的部分（8位数字）
		if len(part) == 8 && isNumeric(part) {
			continue
		}
		// 跳过版本号（如 v1:0）
		if strings.Contains(part, ":") {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "-")
}

// matchByModelFamily 基于模型系列匹配
func (s *PricingService) matchByModelFamilyWithSource(model string) (*LiteLLMModelPricing, string, string) {
	if claude.IsOpus55(model) {
		if pricing, ok := s.pricingData["claude-opus-5-5"]; ok {
			return pricing, "claude-opus-5-5", "family"
		}
		return claudeOpus55FallbackPricing, "claude-opus-5-5", "static"
	}
	if claude.IsSonnet55(model) {
		if pricing, ok := s.pricingData["claude-sonnet-5-5"]; ok {
			return pricing, "claude-sonnet-5-5", "family"
		}
		return claudeSonnet55FallbackPricing, "claude-sonnet-5-5", "static"
	}
	if model == "claude-haiku-5-5" {
		if pricing, ok := s.pricingData["claude-haiku-5-5"]; ok {
			return pricing, "claude-haiku-5-5", "family"
		}
		return claudeHaiku55FallbackPricing, "claude-haiku-5-5", "static"
	}
	// modelFamily 定义一个模型系列的匹配和定价查找规则。
	type modelFamily struct {
		name    string   // 系列名称
		match   []string // 用于将模型归类到此系列的模式（strings.Contains 匹配）
		pricing []string // 用于在定价数据中查找价格的模式（nil 则复用 match；可包含低版本 fallback）
	}

	// 按特异性降序排列：高版本号在前，避免 "claude-opus-4"（opus-4 系列）
	// 因子串关系误匹配 "claude-opus-4-7"（opus-4.7 系列）。
	// 注意：原 map 实现存在 Go map 迭代随机性导致的同类 bug，此处改为有序切片修复。
	families := []modelFamily{
		// Opus 5 与 Opus 4.8 同价（$5/$25 per MTok）。定价数据缺失 claude-opus-5 时
		// 必须回退到 4.8，否则会掉进 "opus-4" 系列按 $15/$75 计费（3 倍超收）。
		{name: "opus-5", match: []string{"claude-opus-5"}, pricing: []string{"claude-opus-5", "claude-opus-4-8"}},
		{name: "opus-4.8", match: []string{"claude-opus-4-8", "claude-opus-4.8"}, pricing: []string{"claude-opus-4-8", "claude-opus-4.8", "claude-opus-4-7"}},
		{name: "opus-4.7", match: []string{"claude-opus-4-7", "claude-opus-4.7"}, pricing: []string{"claude-opus-4-7", "claude-opus-4.7", "claude-opus-4-6"}},
		{name: "opus-4.6", match: []string{"claude-opus-4-6", "claude-opus-4.6"}},
		{name: "opus-4.5", match: []string{"claude-opus-4-5", "claude-opus-4.5"}},
		{name: "opus-4", match: []string{"claude-opus-4", "claude-3-opus"}},
		{name: "sonnet-4.5", match: []string{"claude-sonnet-4-5", "claude-sonnet-4.5"}},
		{name: "sonnet-4", match: []string{"claude-sonnet-4", "claude-3-5-sonnet"}},
		{name: "sonnet-3.5", match: []string{"claude-3-5-sonnet", "claude-3.5-sonnet"}},
		{name: "sonnet-3", match: []string{"claude-3-sonnet"}},
		{name: "haiku-3.5", match: []string{"claude-3-5-haiku", "claude-3.5-haiku"}},
		{name: "haiku-3", match: []string{"claude-3-haiku"}},
	}

	// Phase 1: 按有序切片归类（最具体的系列优先匹配）
	var matched *modelFamily
	for i := range families {
		for _, pattern := range families[i].match {
			if strings.Contains(model, pattern) || strings.Contains(model, strings.ReplaceAll(pattern, "-", "")) {
				matched = &families[i]
				break
			}
		}
		if matched != nil {
			break
		}
	}

	// Phase 2: 二次兜底——当模型 ID 不含已知模式串时，按关键字粗分
	if matched == nil {
		var fallbackName string
		switch {
		case strings.Contains(model, "opus"):
			switch {
			// "opus-5" 必须先判：不能用裸 "5" 匹配，否则 claude-opus-4-5 会被误判。
			case strings.Contains(model, "opus-5") || strings.Contains(model, "opus5"):
				fallbackName = "opus-5"
			case strings.Contains(model, "4.8") || strings.Contains(model, "4-8"):
				fallbackName = "opus-4.8"
			case strings.Contains(model, "4.7") || strings.Contains(model, "4-7"):
				fallbackName = "opus-4.7"
			case strings.Contains(model, "4.6") || strings.Contains(model, "4-6"):
				fallbackName = "opus-4.6"
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "opus-4.5"
			default:
				fallbackName = "opus-4"
			}
		case strings.Contains(model, "sonnet"):
			switch {
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "sonnet-4.5"
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "sonnet-3.5"
			default:
				fallbackName = "sonnet-4"
			}
		case strings.Contains(model, "haiku"):
			switch {
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "haiku-3.5"
			default:
				fallbackName = "haiku-3"
			}
		}
		if fallbackName != "" {
			for i := range families {
				if families[i].name == fallbackName {
					matched = &families[i]
					break
				}
			}
		}
	}

	if matched == nil {
		return nil, "", ""
	}

	// Phase 3: 在定价数据中查找该系列的价格
	lookups := matched.pricing
	if lookups == nil {
		lookups = matched.match
	}
	for _, pattern := range lookups {
		for key, pricing := range s.pricingData {
			keyLower := strings.ToLower(key)
			if matched.name == "opus-5" && claude.IsOpus55(keyLower) {
				continue
			}
			if strings.Contains(keyLower, pattern) {
				return pricing, key, "family"
			}
		}
	}

	return nil, "", ""
}

// matchOpenAIModel OpenAI 模型回退匹配策略
// 回退顺序：
// 1. gpt-5.3-codex-spark* -> gpt-5.1-codex（按业务要求固定计费）
// 2. gpt-5.2-codex -> gpt-5.2（去掉后缀如 -codex, -mini, -max 等）
// 3. gpt-5.2-20251222 -> gpt-5.2（去掉日期版本号）
// 4. gpt-5.3-codex -> gpt-5.2-codex
// 5. gpt-5.4* -> 业务静态兜底价
// 6. 最终回退到 DefaultTestModel (gpt-5.1-codex)
func (s *PricingService) matchOpenAIModel(model string) *LiteLLMModelPricing {
	pricing, _, _ := s.matchOpenAIModelWithSource(model)
	return pricing
}
func (s *PricingService) matchOpenAIModelWithSource(model string) (*LiteLLMModelPricing, string, string) {
	if strings.HasPrefix(model, "gpt-5.3-codex-spark") {
		if pricing, ok := s.pricingData["gpt-5.1-codex"]; ok {
			return pricing, "gpt-5.1-codex", "openai_variant"
		}
	}

	if openai.IsGPT61SolModelSpelling(model) {
		if pricing, ok := s.pricingData["gpt-6.1-sol"]; ok {
			return pricing, "gpt-6.1-sol", "openai_variant"
		}
		return openAIGPT61SolFallbackPricing, "gpt-6.1-sol", "static"
	}
	if openai.IsGPT6SolOrLunaModelSpelling(model) {
		if pricing, ok := s.pricingData[normalizeKnownOpenAICodexModel(model)]; ok {
			return pricing, normalizeKnownOpenAICodexModel(model), "openai_variant"
		}
		if strings.HasPrefix(model, "gpt-6-sol") {
			return openAIGPT6SolFallbackPricing, "gpt-6-sol", "static"
		}
		return openAIGPT6LunaFallbackPricing, "gpt-6-luna", "static"
	}

	// 尝试的回退变体
	variants := s.generateOpenAIModelVariants(model, openAIModelDatePattern)

	for _, variant := range variants {
		if pricing, ok := s.pricingData[variant]; ok {
			return pricing, variant, "openai_variant"
		}
	}

	if strings.HasPrefix(model, "gpt-5.3-codex") {
		if pricing, ok := s.pricingData["gpt-5.2-codex"]; ok {
			return pricing, "gpt-5.2-codex", "openai_variant"
		}
	}

	if isOpenAIGPT6AstraModel(model) {
		return openAIGPT6AstraFallbackPricing, "gpt-6-astra", "static"
	}

	if strings.HasPrefix(model, "gpt-5.6-sol") {
		return openAIGPT56SolFallbackPricing, "gpt-5.6-sol", "static"
	}
	if strings.HasPrefix(model, "gpt-5.6-terra") {
		return openAIGPT56TerraFallbackPricing, "gpt-5.6-terra", "static"
	}
	if strings.HasPrefix(model, "gpt-5.6-luna") {
		return openAIGPT56LunaFallbackPricing, "gpt-5.6-luna", "static"
	}

	// GPT-5.5 回退到 GPT-5.4 定价
	if strings.HasPrefix(model, "gpt-5.5") {
		return openAIGPT54FallbackPricing, "gpt-5.4", "static"
	}

	if strings.HasPrefix(model, "gpt-5.4-mini") {
		return openAIGPT54MiniFallbackPricing, "gpt-5.4-mini", "static"
	}

	if strings.HasPrefix(model, "gpt-5.4-nano") {
		return openAIGPT54NanoFallbackPricing, "gpt-5.4-nano", "static"
	}

	if strings.HasPrefix(model, "gpt-5.4") {
		return openAIGPT54FallbackPricing, "gpt-5.4", "static"
	}

	// Remote price mirrors can lag new releases. Never bill GPT Image 2.5
	// using the older image model's rates when its entry is absent.
	for _, imageModel := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		if model == imageModel || model == imageModel+"-2026-09-08" {
			return openAIGPTImage25FallbackPricing, "gpt-image-2.5", "static"
		}
	}
	if isOpenAIImageGenerationModel(model) {
		for _, candidate := range []string{"gpt-image-2", "gpt-image-1.5", "gpt-image-1"} {
			if pricing, ok := s.pricingData[candidate]; ok {
				return pricing, candidate, "openai_variant"
			}
		}
		return nil, "", ""
	}

	// 最终回退到 DefaultTestModel
	defaultModel := strings.ToLower(openai.DefaultTestModel)
	if pricing, ok := s.pricingData[defaultModel]; ok {
		return pricing, defaultModel, "default"
	}

	return nil, "", ""
}

// generateOpenAIModelVariants 生成 OpenAI 模型的回退变体列表
func (s *PricingService) generateOpenAIModelVariants(model string, datePattern *regexp.Regexp) []string {
	seen := make(map[string]bool)
	var variants []string

	addVariant := func(v string) {
		if v != model && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}

	// 1. 去掉日期版本号: gpt-5.2-20251222 -> gpt-5.2
	withoutDate := datePattern.ReplaceAllString(model, "")
	if withoutDate != model {
		addVariant(withoutDate)
	}

	// 2. 提取基础版本号: gpt-5.2-codex -> gpt-5.2
	// 只匹配纯数字版本号格式 gpt-X 或 gpt-X.Y，不匹配 gpt-4o 这种带字母后缀的
	if matches := openAIModelBasePattern.FindStringSubmatch(model); len(matches) > 1 {
		addVariant(matches[1])
	}

	// 3. 同时去掉日期后再提取基础版本号
	if withoutDate != model {
		if matches := openAIModelBasePattern.FindStringSubmatch(withoutDate); len(matches) > 1 {
			addVariant(matches[1])
		}
	}

	return variants
}

// GetStatus 获取服务状态
func (s *PricingService) GetStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"model_count":  len(s.pricingData),
		"last_updated": s.lastUpdated,
		"local_hash":   s.localHash[:min(8, len(s.localHash))],
	}
}

// ForceUpdate 强制更新
func (s *PricingService) ForceUpdate() error {
	return s.downloadPricingData()
}

// getPricingFilePath 获取价格文件路径
func (s *PricingService) getPricingFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.json")
}

// getHashFilePath 获取哈希文件路径
func (s *PricingService) getHashFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.sha256")
}

// ListModelNamesByProvider returns all model names in the catalog whose
// LiteLLMProvider matches the given provider string (case-insensitive).
// The returned slice is sorted alphabetically.
func (s *PricingService) ListModelNamesByProvider(provider string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	provider = strings.ToLower(strings.TrimSpace(provider))
	names := make([]string, 0)
	for name, p := range s.pricingData {
		if strings.ToLower(p.LiteLLMProvider) == provider {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// isNumeric 检查字符串是否为纯数字
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validatePricingEntry checks the merged price card; explicit zero is valid.
func validatePricingEntry(raw json.RawMessage, entry LiteLLMRawEntry) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("entry must be a JSON object")
	}
	priced := false
	for key, value := range fields {
		if !strings.HasPrefix(key, "input_cost_per_token") && !strings.HasPrefix(key, "output_cost_per_token") &&
			!strings.HasPrefix(key, "cache_creation_input_token_cost") && !strings.HasPrefix(key, "cache_read_input_token_cost") &&
			key != "output_cost_per_image" && key != "output_cost_per_image_token" && key != "input_cost_per_image_token" && key != "cache_read_input_image_token_cost" {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		var price float64
		if err := json.Unmarshal(value, &price); err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
			return fmt.Errorf("%s must be a finite non-negative price", key)
		}
		priced = true
	}
	if !priced || entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil {
		return nil
	}
	hasImagePrice := entry.OutputCostPerImage != nil || entry.OutputCostPerImageToken != nil
	if hasImagePrice {
		return nil
	}
	if entry.Mode == "embedding" && entry.InputCostPerToken != nil {
		return nil
	}
	if entry.InputCostPerToken == nil || entry.OutputCostPerToken == nil {
		return fmt.Errorf("token pricing requires input_cost_per_token and output_cost_per_token")
	}
	return nil
}

// savePricingFiles stages validated bytes before publishing the local cache.
func (s *PricingService) savePricingFiles(body []byte, hash string) error {
	paths := []string{s.getPricingFilePath(), s.getHashFilePath()}
	contents := [][]byte{body, []byte(hash + "\n")}
	temps := make([]string, 0, 2)
	defer func() {
		for _, path := range temps {
			_ = os.Remove(path)
		}
	}()
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("pricing cache path is not a regular file: %s", path)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for i, path := range paths {
		f, err := os.CreateTemp(filepath.Dir(path), ".pricing-*")
		if err != nil {
			return fmt.Errorf("stage pricing file: %w", err)
		}
		temps = append(temps, f.Name())
		_, writeErr := f.Write(contents[i])
		closeErr := f.Close()
		if writeErr != nil {
			return fmt.Errorf("write pricing file: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close pricing file: %w", closeErr)
		}
		if err := os.Chmod(f.Name(), 0644); err != nil {
			return err
		}
	}
	// Keep a restorable data file until the corresponding hash has been published.
	backup := ""
	oldBody, err := os.ReadFile(paths[0])
	if err == nil {
		f, createErr := os.CreateTemp(filepath.Dir(paths[0]), ".pricing-previous-*")
		if createErr != nil {
			return createErr
		}
		backup = f.Name()
		temps = append(temps, backup)
		_, writeErr := f.Write(oldBody)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Chmod(backup, 0644); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temps[0], paths[0]); err != nil {
		return fmt.Errorf("publish pricing file: %w", err)
	}
	if err := os.Rename(temps[1], paths[1]); err != nil {
		var restoreErr error
		if backup != "" {
			restoreErr = os.Rename(backup, paths[0])
		} else {
			restoreErr = os.Remove(paths[0])
		}
		if restoreErr != nil {
			return fmt.Errorf("publish pricing hash: %w; restore previous cache: %w", err, restoreErr)
		}
		return fmt.Errorf("publish pricing hash: %w", err)
	}

	return nil
}
