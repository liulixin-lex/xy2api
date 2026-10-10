package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAICacheDiagnosticKey struct{}

func openAICacheModelLabel(model string) string {
	if len(model) == 0 || len(model) > 100 {
		return "other"
	}
	for _, c := range model {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return "other"
		}
	}
	return model
}

type OpenAICacheDiagnostic struct {
	Session, Prefix, WirePrefix, Mode, Format, Model string
	GroupID                                          int64
	LengthBucket                                     int
	Source                                           string
}

type openAICacheHistory struct {
	account int64
	prefix  string
	at      time.Time
}

type openAICacheSample struct {
	count                    int
	input, read, write, ttft float64
	at                       time.Time
}

type openAICacheTelemetry struct {
	mu       sync.Mutex
	sessions map[string]openAICacheHistory
	scores   map[string]openAICacheSample
}

func (s *OpenAIGatewayService) cacheTelemetry() *openAICacheTelemetry {
	s.openaiCacheOnce.Do(func() {
		s.openaiCacheTelemetry = &openAICacheTelemetry{sessions: make(map[string]openAICacheHistory), scores: make(map[string]openAICacheSample)}
	})
	return s.openaiCacheTelemetry
}

func openAICachePrefix(body []byte) string {
	root := gjson.ParseBytes(body)
	fields := make(map[string]any)
	for _, key := range []string{"instructions", "tools", "functions", "text", "response_format", "reasoning", "reasoning_effort", "tool_choice"} {
		if value := root.Get(key); value.Exists() {
			fields[key] = value.Value()
		}
	}
	var leading []any
	items := root.Get("messages")
	if !items.IsArray() {
		items = root.Get("input")
	}
	items.ForEach(func(_, item gjson.Result) bool {
		role := item.Get("role").String()
		if role != "system" && role != "developer" {
			return false
		}
		leading = append(leading, item.Value())
		return true
	})
	fields["leading"] = leading
	encoded, _ := json.Marshal(fields)
	return string(encoded)
}

func (s *OpenAIGatewayService) newCacheDiagnostic(c *gin.Context, body []byte, source string) *OpenAICacheDiagnostic {
	if s.cfg == nil || (!s.cfg.Gateway.OpenAICache.DiagnosticsEnabled && !s.cfg.Gateway.OpenAICache.AwareRoutingEnabled) || c == nil || getAPIKeyIDFromContext(c) <= 0 {
		return nil
	}
	digest := s.cacheIdentityDigest("openai-prefix-v1", fmt.Sprintf("%d:%d:%s", getAPIKeyIDFromContext(c), getOpenAIGroupIDFromContext(c), openAICachePrefix(body)))
	if digest == "" {
		return nil
	}
	mode := gjson.GetBytes(body, "prompt_cache_options.mode").String()
	switch mode {
	case "automatic", "explicit":
	case "":
		mode = "automatic"
	default:
		mode = "other"
	}
	format := gjson.GetBytes(body, "text.format.type").String()
	if format == "" {
		format = gjson.GetBytes(body, "response_format.type").String()
	}
	switch format {
	case "text", "json_object", "json_schema":
	case "":
		format = "text"
	default:
		format = "other"
	}
	bucket := 1024
	for bucket < len(body) && bucket < 1<<24 {
		bucket *= 2
	}
	session, _ := c.Get(openAICacheSessionGinKey)
	sessionHash, _ := session.(string)
	return &OpenAICacheDiagnostic{Session: sessionHash, Prefix: digest, WirePrefix: digest, Mode: mode, Format: format, Model: openAICacheModelLabel(gjson.GetBytes(body, "model").String()), GroupID: getOpenAIGroupIDFromContext(c), LengthBucket: bucket, Source: source}
}

func (s *OpenAIGatewayService) captureCacheDiagnostic(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	d := s.newCacheDiagnostic(c, body, "wire")
	if d != nil {
		if c.Request != nil {
			if ingress, ok := c.Request.Context().Value(openAICacheDiagnosticKey{}).(*OpenAICacheDiagnostic); ok && ingress != nil {
				d.Prefix = ingress.Prefix
			}
		}
		c.Set("openai_cache_diagnostic", d)
	}
}

func (s *OpenAIGatewayService) captureCacheRequestDiagnostic(c *gin.Context, req *http.Request) {
	if s.cfg == nil || (!s.cfg.Gateway.OpenAICache.DiagnosticsEnabled && !s.cfg.Gateway.OpenAICache.AwareRoutingEnabled) || req == nil || req.GetBody == nil {
		return
	}
	body, err := req.GetBody()
	if err != nil {
		return
	}
	defer func() { _ = body.Close() }()
	encoded, err := io.ReadAll(body)
	if err == nil {
		s.captureCacheDiagnostic(c, encoded)
	}
}

func (s *OpenAIGatewayService) finishCacheDiagnostic(c *gin.Context, body []byte, result *OpenAIForwardResult) {
	if c == nil || result == nil {
		return
	}
	if session, ok := c.Get(openAICacheSessionGinKey); ok {
		result.RoutingSessionID, _ = session.(string)
	}
	d := s.newCacheDiagnostic(c, body, "ingress")
	if value, exists := c.Get("openai_cache_diagnostic"); exists {
		if wire, ok := value.(*OpenAICacheDiagnostic); ok {
			d = wire
		}
	}
	if d != nil {
		copy := *d
		copy.Model = result.UpstreamModel
		if copy.Model == "" {
			copy.Model = result.Model
		}
		copy.Model = openAICacheModelLabel(copy.Model)
		result.CacheDiagnostic = &copy
	}
}

func (s *OpenAIGatewayService) logCacheSettlement(result *OpenAIForwardResult, usage *UsageLog) {
	if s.cfg == nil || !s.cfg.Gateway.OpenAICache.DiagnosticsEnabled || result.CacheDiagnostic == nil {
		return
	}
	slog.Info("openai_cache_settled_usage", "session_digest", result.CacheDiagnostic.Session, "request_id_hmac", s.cacheIdentityDigest("openai-request", result.RequestID), "input_tokens", usage.InputTokens, "cache_read_tokens", usage.CacheReadTokens, "cache_write_tokens", usage.CacheCreationTokens, "total_cost", usage.TotalCost, "actual_cost", usage.ActualCost)
}

func cacheSampleKey(d *OpenAICacheDiagnostic, account *Account, model string) string {
	return fmt.Sprintf("%d:%s:%s:%s:%s:%s:%d:%d", d.GroupID, strings.ToLower(model), account.Type, d.Prefix, d.Mode, d.Format, d.LengthBucket, account.ID)
}

// Only comparable continuations train the preference. Cold requests, expired
// sessions, prefix changes and account switches are reported separately.
func (s *OpenAIGatewayService) observeCacheUsage(result *OpenAIForwardResult, account *Account, now time.Time) {
	d := result.CacheDiagnostic
	if d == nil || d.Session == "" || account == nil || account.Platform != PlatformOpenAI {
		return
	}
	t := s.cacheTelemetry()
	t.mu.Lock()
	previous, exists := t.sessions[d.Session]
	reason := "cold"
	if exists {
		switch {
		case now.Sub(previous.at) > 30*time.Minute:
			reason = "gap_over_30m"
		case previous.prefix != d.WirePrefix:
			reason = "prefix_changed"
		case previous.account != account.ID:
			reason = "account_changed"
		default:
			reason = "comparable_continuation"
		}
	}
	if len(t.sessions) >= 2048 && !exists {
		var oldestKey string
		var oldest time.Time
		for key, value := range t.sessions {
			if oldestKey == "" || value.at.Before(oldest) {
				oldestKey, oldest = key, value.at
			}
		}
		delete(t.sessions, oldestKey)
	}
	t.sessions[d.Session] = openAICacheHistory{account.ID, d.WirePrefix, now}
	usage := result.Usage
	if reason == "comparable_continuation" && usage.InputTokens >= 1024 && usage.CacheReadSource != "" && usage.CacheReadInputTokens <= usage.InputTokens && usage.CacheCreationInputTokens <= usage.InputTokens-usage.CacheReadInputTokens {
		key := cacheSampleKey(d, account, d.Model)
		value := t.scores[key]
		if now.Sub(value.at) > 30*time.Minute {
			value = openAICacheSample{}
		}
		alpha := 0.2
		if value.count == 0 {
			alpha = 1
		}
		value.input = (1-alpha)*value.input + alpha*float64(usage.InputTokens)
		value.read = (1-alpha)*value.read + alpha*float64(usage.CacheReadInputTokens)
		value.write = (1-alpha)*value.write + alpha*float64(usage.CacheCreationInputTokens)
		if result.FirstTokenMs != nil {
			value.ttft = (1-alpha)*value.ttft + alpha*float64(max(*result.FirstTokenMs, 0))
		}
		value.count++
		value.at = now
		if len(t.scores) >= 4096 {
			for oldKey, sample := range t.scores {
				if now.Sub(sample.at) > 30*time.Minute {
					delete(t.scores, oldKey)
				}
			}
		}
		if _, known := t.scores[key]; known || len(t.scores) < 4096 {
			t.scores[key] = value
		}
	}
	t.mu.Unlock()
	if s.cfg != nil && s.cfg.Gateway.OpenAICache.DiagnosticsEnabled {
		slog.Info("openai_cache_usage", "session_digest", d.Session, "prefix_hmac", d.WirePrefix, "fingerprint_source", d.Source, "account_id", account.ID, "group_id", d.GroupID, "model", d.Model, "cache_mode", d.Mode, "format", d.Format, "length_bucket", d.LengthBucket, "reason", reason, "input_tokens", usage.InputTokens, "cache_read_tokens", usage.CacheReadInputTokens, "cache_write_tokens", usage.CacheCreationInputTokens, "read_source", usage.CacheReadSource, "write_source", usage.CacheWriteSource, "attempt_id", result.SchedulingAttemptID, "request_id_hmac", s.cacheIdentityDigest("openai-request", result.RequestID))
	}
}

func (s *OpenAIGatewayService) cachePreference(ctx context.Context, account *Account, model string) (float64, bool) {
	if s.cfg == nil || !s.cfg.Gateway.OpenAICache.AwareRoutingEnabled || account == nil || !account.IsOpenAI() {
		return 0, false
	}
	d, _ := ctx.Value(openAICacheDiagnosticKey{}).(*OpenAICacheDiagnostic)
	if d == nil || d.Session == "" || d.Prefix == "" {
		return 0, false
	}
	// Stable session cohort; restarting the process does not change rollout.
	cohort := sha256SumCacheCohort(d.Session)
	if cohort >= s.cfg.Gateway.OpenAICache.RolloutPercent {
		return 0, false
	}
	t := s.cacheTelemetry()
	t.mu.Lock()
	value := t.scores[cacheSampleKey(d, account, normalizeOpenAIModelForUpstream(account, account.GetMappedModel(model)))]
	t.mu.Unlock()
	if value.count < 20 || value.input <= 0 || time.Since(value.at) > 30*time.Minute {
		return 0, false
	}
	// Normalized input cost plus a bounded TTFT penalty. This is a tie-breaker,
	// not a replacement for account health, priority, ownership or admission.
	readDiscount := 0.1
	if strings.Contains(model, "gpt-6.1-sol") {
		readDiscount = 0.05
	}
	cost := (value.input - (1-readDiscount)*value.read + 0.25*value.write) / value.input
	return cost*account.BillingRateMultiplier() + 0.1*math.Min(value.ttft/15000, 1), true
}

func (s *OpenAIGatewayService) preferCacheCandidates(ctx context.Context, available []accountWithLoad, model string) {
	// Sort only complete comparable cohorts. Missing evidence keeps the existing
	// order and cannot promote an unknown account ahead of a measured account.
	for start := 0; start < len(available); {
		end := start + 1
		for end < len(available) && available[end].account.Priority == available[start].account.Priority && available[end].loadInfo.LoadRate == available[start].loadInfo.LoadRate {
			end++
		}
		scores := make(map[int64]float64)
		complete := true
		for _, candidate := range available[start:end] {
			value, known := s.cachePreference(ctx, candidate.account, model)
			if !known {
				complete = false
				break
			}
			scores[candidate.account.ID] = value
		}
		if complete {
			block := available[start:end]
			sort.SliceStable(block, func(i, j int) bool { return scores[block[i].account.ID] < scores[block[j].account.ID] })
		}
		start = end
	}
}

func (s *OpenAIGatewayService) preferCacheScoredCandidates(ctx context.Context, candidates []openAIAccountCandidateScore, model string) {
	for start := 0; start < len(candidates); {
		end := start + 1
		first := candidates[start]
		for end < len(candidates) && candidates[end].account.Priority == first.account.Priority && candidates[end].loadInfo.LoadRate == first.loadInfo.LoadRate && candidates[end].loadInfo.WaitingCount == first.loadInfo.WaitingCount && candidates[end].score == first.score && candidates[end].errorRate == first.errorRate && candidates[end].ttft == first.ttft && openAICompactSupportTier(candidates[end].account) == openAICompactSupportTier(first.account) {
			end++
		}
		scores := make(map[int64]float64)
		complete := true
		for _, candidate := range candidates[start:end] {
			value, known := s.cachePreference(ctx, candidate.account, model)
			if !known {
				complete = false
				break
			}
			scores[candidate.account.ID] = value
		}
		if complete {
			block := candidates[start:end]
			sort.SliceStable(block, func(i, j int) bool { return scores[block[i].account.ID] < scores[block[j].account.ID] })
		}
		start = end
	}
}
