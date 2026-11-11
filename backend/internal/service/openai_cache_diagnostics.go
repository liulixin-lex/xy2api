package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

type openAICacheTelemetry struct {
	mu       sync.Mutex
	sessions map[string]openAICacheHistory
}

func (s *OpenAIGatewayService) cacheTelemetry() *openAICacheTelemetry {
	s.openaiCacheOnce.Do(func() {
		s.openaiCacheTelemetry = &openAICacheTelemetry{sessions: make(map[string]openAICacheHistory)}
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
	if s.cfg == nil || !s.cfg.Gateway.OpenAICache.DiagnosticsEnabled || c == nil || getAPIKeyIDFromContext(c) <= 0 {
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
	if s.cfg == nil || !s.cfg.Gateway.OpenAICache.DiagnosticsEnabled || req == nil || req.GetBody == nil {
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
	t.mu.Unlock()
	if s.cfg != nil && s.cfg.Gateway.OpenAICache.DiagnosticsEnabled {
		slog.Info("openai_cache_usage", "session_digest", d.Session, "prefix_hmac", d.WirePrefix, "fingerprint_source", d.Source, "account_id", account.ID, "group_id", d.GroupID, "model", d.Model, "cache_mode", d.Mode, "format", d.Format, "length_bucket", d.LengthBucket, "reason", reason, "input_tokens", usage.InputTokens, "cache_read_tokens", usage.CacheReadInputTokens, "cache_write_tokens", usage.CacheCreationInputTokens, "read_source", usage.CacheReadSource, "write_source", usage.CacheWriteSource, "request_id_hmac", s.cacheIdentityDigest("openai-request", result.RequestID))
	}
}
