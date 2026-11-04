package service

import "github.com/gin-gonic/gin"

const (
	codexCollaborationNamespace      = "collaboration"
	codexCollaborationNamespaceAlias = "collaboration-xy"
	codexCollabAttemptContextKey     = "openai_codex_collaboration_attempt"
)

type codexCollabOutcome string

const (
	codexCollabApplied     codexCollabOutcome = "applied"
	codexCollabAbsent      codexCollabOutcome = "absent"
	codexCollabConflict    codexCollabOutcome = "conflict"
	codexCollabUnsupported codexCollabOutcome = "unsupported"
	codexCollabInvalid     codexCollabOutcome = "invalid"
)

type codexCollabPlan struct {
	// Filled by the outbound transform and consumed by one response restorer.
	messageTools map[string]struct{}
}

type codexCollabRestorer struct {
	plan  *codexCollabPlan
	stats codexCollabStats
}

type codexCollabStats struct {
	Outcome                                      codexCollabOutcome
	Applied                                      bool
	Calls                                        int
	EFAAbsent, EFAEmpty, EFANonEmpty             int
	MarkerAdded, Vetoed, Contradicted, Uncovered int
}

func applyCodexCollaborationPlaintext(body []byte) (out []byte, plan *codexCollabPlan, outcome codexCollabOutcome) {
	return body, nil, codexCollabAbsent
}

func newCodexCollabRestorer(plan *codexCollabPlan) *codexCollabRestorer {
	return &codexCollabRestorer{plan: plan}
}

func (r *codexCollabRestorer) SSEData(eventType string, data []byte) ([]byte, error) {
	return data, nil
}

func (r *codexCollabRestorer) ResponseJSON(body []byte) ([]byte, error) {
	return body, nil
}

func (r *codexCollabRestorer) Stats() codexCollabStats {
	if r == nil {
		return codexCollabStats{}
	}
	return r.stats
}

func setOpenAICodexCollabAttempt(c *gin.Context, stats codexCollabStats) {
	if c != nil {
		c.Set(codexCollabAttemptContextKey, stats)
	}
}

func openAICodexCollabAttempt(c *gin.Context) (codexCollabStats, bool) {
	if c == nil {
		return codexCollabStats{}, false
	}
	value, exists := c.Get(codexCollabAttemptContextKey)
	if !exists {
		return codexCollabStats{}, false
	}
	stats, ok := value.(codexCollabStats)
	return stats, ok
}
