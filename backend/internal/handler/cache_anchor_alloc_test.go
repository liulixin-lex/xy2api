//go:build unit

package handler

import (
	"context"
	"math"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Closed-state allocation gate on the actual handlers (M4 kickoff §5, §7.1):
// a request that L-04 skips before its anchor costs what the closed build
// costs. Both arms run on one fixture, alternating; each keeps its minimum over
// five testing.AllocsPerRun rounds after one uncounted warm-up round.
func l04HandlerArmAllocs(arms []func(), op func()) []float64 {
	best := make([]float64, len(arms))
	for i, arm := range arms {
		best[i] = math.Inf(1)
		arm()
		testing.AllocsPerRun(20, op)
	}
	for round := 0; round < 5; round++ {
		for i, arm := range arms {
			arm()
			best[i] = math.Min(best[i], testing.AllocsPerRun(20, op))
		}
	}
	return best
}

func TestL04OutOfScopeActualHandlerAllocsEqualClosedBuild(t *testing.T) {
	withHandlerResilience(t, nil)
	const unlisted = `{"rev":7,"spec":{"anchor_group_ids":[55],"rollout_percent":100,"gemini_anchor":true}}`
	const messages = `{"model":"claude-sonnet-4-5","system":"common system","messages":[{"role":"user","content":"first user"}],"max_tokens":1}`
	for _, tc := range []struct {
		name, policy, platform, path, body  string
		promptFallback, claudeCode, applied bool
		call                                func(*GatewayHandler, *gin.Context)
	}{
		{name: "messages_group_not_in_list", policy: unlisted, path: "/v1/messages", body: messages, call: (*GatewayHandler).Messages},
		{name: "claude_code_fallback_not_in_list", policy: unlisted, claudeCode: true, path: "/v1/messages", body: messages, call: (*GatewayHandler).Messages},
		{name: "prompt_too_long_fallback_not_in_list", policy: unlisted, promptFallback: true, path: "/v1/messages", body: `{"model":"claude-opus-4-6","system":"common system","messages":[{"role":"user","content":"first user"}],"max_tokens":1,"stream":false}`, call: (*GatewayHandler).Messages},
		{name: "compat_chat_listed", path: "/v1/chat/completions", body: `{"model":"claude-sonnet-4-5","messages":[{"role":"system","content":"system A"},{"role":"user","content":"hi"}],"stream":false}`, call: (*GatewayHandler).ChatCompletions},
		{name: "gemini_anchor_off", policy: `{"rev":7,"spec":{"anchor_group_ids":[31],"rollout_percent":100,"gemini_anchor":false}}`, platform: service.PlatformGemini, path: "/v1beta/models/gemini-2.5-flash:generateContent", body: `{"systemInstruction":{"parts":[{"text":"same system"}]},"contents":[{"role":"user","parts":[{"text":"first user"}]}]}`, call: (*GatewayHandler).GeminiV1BetaModels},
		// Positive control: the gate must see an applied anchor's extra work.
		{name: "messages_applied", applied: true, path: "/v1/messages", body: messages, call: (*GatewayHandler).Messages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform := tc.platform
			if platform == "" {
				platform = service.PlatformAnthropic
			}
			store := &l04HandlerPolicyStore{spec: tc.policy}
			h, k, cache, up, policies := l04HandlerStoreFixture(t, service.CacheM4BuildCapabilities{L04: true}, store, tc.promptFallback, platform)
			if tc.claudeCode {
				fallback := int64(77)
				k.Group.ClaudeCodeOnly = true
				k.Group.FallbackGroupID = &fallback
			}
			arm := func(enabled bool) func() {
				return func() {
					store.enabled = enabled
					require.NoError(t, policies.Refresh(context.Background()))
				}
			}
			op := func() {
				cache.calls, up.ids, up.bodies = cache.calls[:0], up.ids[:0], up.bodies[:0]
				l04ServeHandler(t, h, k, tc.path, tc.body, tc.call)
			}
			got := l04HandlerArmAllocs([]func(){arm(false), arm(true)}, op)
			t.Logf("allocs/op closed=%v %s=%v", got[0], tc.name, got[1])
			if tc.applied {
				require.Greater(t, got[1], got[0])
				return
			}
			require.Equal(t, got[0], got[1], "skipped before the anchor must cost what the closed build costs")
		})
	}
}
