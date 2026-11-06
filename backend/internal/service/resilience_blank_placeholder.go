package service

import (
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const openAIBlankPlaceholderStatsKey = "resilience_openai_blank_placeholder_stats"

type openAIBlankPlaceholderStats struct {
	mu              sync.Mutex
	stagedAttempts  int
	timeoutAttempts int
}

// markOpenAIBlankPlaceholderStaged records one attempt after it has actually
// written a recognized blank placeholder into its bounded pre-output stage.
func markOpenAIBlankPlaceholderStaged(c *gin.Context, accountID int64) {
	if c == nil {
		return
	}
	stats, exists := c.Get(openAIBlankPlaceholderStatsKey)
	current, ok := stats.(*openAIBlankPlaceholderStats)
	if !exists || !ok || current == nil {
		current = &openAIBlankPlaceholderStats{}
		c.Set(openAIBlankPlaceholderStatsKey, current)
	}
	current.mu.Lock()
	current.stagedAttempts++
	current.mu.Unlock()
	logger.FromContext(c.Request.Context()).Info("resilience.blank_placeholder_staged", zap.Int64("account_id", accountID))
}

func markOpenAIBlankPlaceholderTimeout(c *gin.Context, staged bool) {
	if c == nil || !staged {
		return
	}
	stats, exists := c.Get(openAIBlankPlaceholderStatsKey)
	current, ok := stats.(*openAIBlankPlaceholderStats)
	if !exists || !ok || current == nil {
		return
	}
	current.mu.Lock()
	current.timeoutAttempts++
	current.mu.Unlock()
}

// OpenAIBlankPlaceholderOutcomeCounts exposes only the request totals needed
// by the Responses handler's completion log.
func OpenAIBlankPlaceholderOutcomeCounts(c *gin.Context) (stagedAttempts, timeoutAttempts int, marked bool) {
	if c == nil {
		return 0, 0, false
	}
	stats, exists := c.Get(openAIBlankPlaceholderStatsKey)
	current, ok := stats.(*openAIBlankPlaceholderStats)
	if !exists || !ok || current == nil {
		return 0, 0, false
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.stagedAttempts, current.timeoutAttempts, current.stagedAttempts > 0
}
