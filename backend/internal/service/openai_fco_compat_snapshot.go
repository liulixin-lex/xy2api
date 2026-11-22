package service

import (
	"context"

	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// OpenAIFCOCompatSnapshot binds one inbound request, or one WebSocket turn, to
// a single view of the fco_compat settings. Content moderation and every
// outbound attempt read the same snapshot, so a settings reload in the middle
// of a request cannot make the audited text differ from the text that is sent.
type OpenAIFCOCompatSnapshot struct {
	// Settings is the published resilience settings pointer captured at
	// ingress. Published settings are immutable; never write through it.
	Settings *resilience.Settings
	// Rules is the rule set derived from Settings at ingress.
	Rules FCOCompatRules
	// IngressHasDuplicateMembers records that the body as received from the
	// client carried a duplicate JSON member. Later decode and re-encode steps
	// can erase the duplicate, so the flag is taken from the original bytes and
	// forbids any rewrite for the rest of the request or turn.
	IngressHasDuplicateMembers bool
	// IngressMayCarryCandidates records that the body as received carried both
	// the function_call_output text and an "agent" string while the global
	// mode was on. Without it no item can match, so callers skip parsing.
	IngressMayCarryCandidates bool
}

type openAIFCOCompatSnapshotKey struct{}

var (
	fcoIngressCandidateMarker = []byte("function_call_output")
	// A rewritable item needs a JSON string that is exactly agent. Quotes
	// inside string content are escaped, so this literal only matches real
	// string values (or keys) spelled without JSON escapes.
	fcoIngressAgentMarker = []byte(`"agent"`)
)

// NewOpenAIFCOCompatSnapshot captures cfg and inspects the ingress body. The
// duplicate member scan walks the whole body, so it only runs when a rewrite
// is possible at all: the global mode is not off (off cannot be overridden per
// account), and the body carries both the function_call_output text and an
// "agent" string. A body that spells either with JSON escapes is not scanned;
// the rewrite still checks the outbound body for duplicates itself.
func NewOpenAIFCOCompatSnapshot(cfg *resilience.Settings, ingressBody []byte) OpenAIFCOCompatSnapshot {
	var adminNames []string
	if cfg != nil {
		adminNames = cfg.FCOCompat.AdminNames
	}
	rewritePossible := cfg.FCOCompatMode() != resilience.FCOCompatModeOff &&
		hasOpenAIFCOLiterals(ingressBody)
	return OpenAIFCOCompatSnapshot{
		Settings:                   cfg,
		Rules:                      DefaultOpenAIFCORules(adminNames),
		IngressHasDuplicateMembers: rewritePossible && !pkghttputil.HasUniqueJSONMembers(ingressBody),
		IngressMayCarryCandidates:  rewritePossible,
	}
}

// GlobalMode is the global mode after the resilience master switch. Content
// moderation runs before an account is selected and therefore uses this.
func (s OpenAIFCOCompatSnapshot) GlobalMode() string {
	return s.Settings.FCOCompatMode()
}

// ModeFor is the effective mode for one account. It does not decide whether
// the account's platform, type or upstream is eligible.
func (s OpenAIFCOCompatSnapshot) ModeFor(account *Account) string {
	return ResolveOpenAIFCOCompatMode(account, s.Settings)
}

// WithOpenAIFCOCompatSnapshot stores the snapshot for one request or turn.
func WithOpenAIFCOCompatSnapshot(ctx context.Context, snapshot OpenAIFCOCompatSnapshot) context.Context {
	return context.WithValue(ctx, openAIFCOCompatSnapshotKey{}, snapshot)
}

// OpenAIFCOCompatSnapshotFromContext returns the snapshot stored at ingress.
func OpenAIFCOCompatSnapshotFromContext(ctx context.Context) (OpenAIFCOCompatSnapshot, bool) {
	if ctx == nil {
		return OpenAIFCOCompatSnapshot{}, false
	}
	snapshot, ok := ctx.Value(openAIFCOCompatSnapshotKey{}).(OpenAIFCOCompatSnapshot)
	return snapshot, ok
}

// OpenAIFCOCompatSnapshotOrCurrent returns the ingress snapshot, or builds one
// from the current settings and body when an entry point did not store one.
// The fallback sees body after earlier normalization, so its duplicate member
// flag is weaker than the ingress one; entry points should store a snapshot.
func OpenAIFCOCompatSnapshotOrCurrent(ctx context.Context, body []byte) OpenAIFCOCompatSnapshot {
	if snapshot, ok := OpenAIFCOCompatSnapshotFromContext(ctx); ok {
		return snapshot
	}
	return NewOpenAIFCOCompatSnapshot(resilience.Current(), body)
}
