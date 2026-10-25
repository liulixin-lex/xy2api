//go:build unit

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The l07_* fields that the Anthropic event emitter takes from
// L07TraceFieldsFromContext, replayed against cache_trace_contract/m4l07
// v1.0. Each example is produced by a real selection scenario.

const l07TraceDir = "testdata/cache_trace_m4l07"

type l07TraceExample struct {
	Payload map[string]any `json:"payload"`
}

func readL07TraceExample(t *testing.T, name string) l07TraceExample {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(l07TraceDir, "examples", name))
	require.NoError(t, err)
	var ex l07TraceExample
	require.NoError(t, json.Unmarshal(raw, &ex))
	return ex
}

func TestL07TraceContractCopiesAreByteIdentical(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(l07TraceDir, "SHA256SUMS"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 8)
	for _, line := range lines {
		sum, name, ok := strings.Cut(line, "  ")
		require.True(t, ok, line)
		body, err := os.ReadFile(filepath.Join(l07TraceDir, name))
		require.NoError(t, err)
		digest := sha256.Sum256(body)
		require.Equal(t, sum, hex.EncodeToString(digest[:]), name)
	}
}

func l07FieldsFromExample(t *testing.T, p map[string]any) (L07TraceFields, string) {
	t.Helper()
	n := func(k string) int64 { v, _ := p[k].(float64); return int64(v) }
	kept, _ := p["l07_kept"].(bool)
	arm, _ := p["l07_arm"].(string)
	abandon, _ := p["l07_abandon"].(string)
	skip, _ := p["owner_skip_reason"].(string)
	return L07TraceFields{Arm: arm, Kept: kept, Abandon: abandon, BusyN: n("l07_busy_n"), HoldMS: n("l07_hold_ms")}, skip
}

func TestL07TraceFieldsMatchContractExamples(t *testing.T) {
	scenarios := map[string]func(t *testing.T, skip string) L07TraceFields{
		"anthropic_l07_kept.json": func(t *testing.T, skip string) L07TraceFields {
			f := newL07Fixture(t, nil)
			f.withPolicies(t, nil, true, false, nil)
			f.bind("sess", 1)
			ctx, _ := newL07Ctx(t)
			f.selectWith(t, ctx, "sess", nil)
			return L07TraceFieldsFromContext(ctx, skip)
		},
		"anthropic_l07_abandon_consecutive.json": func(t *testing.T, skip string) L07TraceFields {
			f := newL07Fixture(t, nil)
			f.withPolicies(t, nil, true, false, nil)
			f.bind("sess", 1)
			f.cache.segments["sess"] = L07BusyState{Owner: 1, FirstBusyMs: f.now.Add(-18 * time.Second).UnixMilli(), BusyN: 1}
			ctx, _ := newL07Ctx(t)
			f.selectWith(t, ctx, "sess", nil)
			return L07TraceFieldsFromContext(ctx, skip)
		},
		"anthropic_l07_abandon_not_busy.json": func(t *testing.T, skip string) L07TraceFields {
			f := newL07Fixture(t, func(a []Account) { a[0].Type, a[0].Extra = AccountTypeOAuth, map[string]any{"max_sessions": 1} })
			f.limits.full[1] = true
			f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
			f.withPolicies(t, nil, true, false, nil)
			f.bind("sess", 1)
			ctx, _ := newL07Ctx(t)
			f.selectWith(t, ctx, "sess", nil)
			return L07TraceFieldsFromContext(ctx, skip)
		},
		"anthropic_l07_abandon_state_error.json": func(t *testing.T, skip string) L07TraceFields {
			f := newL07Fixture(t, nil)
			f.withPolicies(t, nil, true, false, nil)
			f.cache.readErr = os.ErrDeadlineExceeded
			f.bind("sess", 1)
			ctx, _ := newL07Ctx(t)
			f.selectWith(t, ctx, "sess", nil)
			return L07TraceFieldsFromContext(ctx, skip)
		},
		"anthropic_l07_control.json": func(t *testing.T, skip string) L07TraceFields {
			f := newL07Fixture(t, nil)
			f.withPolicies(t, map[string]string{"cache_routing_policy": `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":50}}}`}, true, false, nil)
			s := l07SessionInCohort(t, false)
			f.bind(s, 1)
			ctx, _ := newL07Ctx(t)
			f.selectWith(t, ctx, s, nil)
			require.Equal(t, int64(2), f.bound(s), "control rebinds as the baseline")
			return L07TraceFieldsFromContext(ctx, skip)
		},
		"anthropic_l07_policy_off_not_busy.json": func(t *testing.T, skip string) L07TraceFields {
			f := newL07Fixture(t, nil)
			f.withPolicies(t, map[string]string{"cache_routing_policy": `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":false}}}`}, true, false, nil)
			f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
			f.bind("sess", 1)
			ctx, _ := newL07Ctx(t)
			require.Equal(t, int64(1), f.selectWith(t, ctx, "sess", nil).Account.ID)
			return L07TraceFieldsFromContext(ctx, skip)
		},
		"anthropic_l07_with_l04.json": func(t *testing.T, skip string) L07TraceFields {
			f := newL07Fixture(t, nil)
			f.withPolicies(t, nil, true, false, nil)
			f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
			f.bind("sess", 1)
			ctx, _ := newL07Ctx(t)
			require.Equal(t, int64(1), f.selectWith(t, ctx, "sess", nil).Account.ID)
			return L07TraceFieldsFromContext(ctx, skip)
		},
	}
	entries, err := os.ReadDir(filepath.Join(l07TraceDir, "examples"))
	require.NoError(t, err)
	require.Len(t, entries, 8)
	for _, e := range entries {
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			ex := readL07TraceExample(t, name)
			if ex.Payload["platform"] != PlatformAnthropic {
				for k := range ex.Payload {
					require.False(t, strings.HasPrefix(k, "l07_"), "%s carries %s", name, k)
				}
				f := newL07Fixture(t, nil)
				f.withPolicies(t, nil, true, false, nil)
				ctx, st := newL07Ctx(t)
				require.Nil(t, f.svc.l07Begin(ctx, &Group{ID: l07TestGroup, Platform: PlatformAnthropic}, "sess", PlatformGemini, 0))
				require.Nil(t, st.L07(), "no L-07 record for a Gemini request")
				return
			}
			want, skip := l07FieldsFromExample(t, ex.Payload)
			produce, ok := scenarios[name]
			require.True(t, ok, "no scenario for %s", name)
			got := produce(t, skip)
			require.Equal(t, want, got)
			requireL07TraceRules(t, got, skip)
		})
	}
}

// requireL07TraceRules are the conditional rules of trace m4l07 §2 (and its
// invalid/ examples) that every emitted field set must satisfy.
func requireL07TraceRules(t *testing.T, f L07TraceFields, skip string) {
	t.Helper()
	require.Contains(t, l07AbandonReasons[:], f.Abandon)
	require.Contains(t, []string{"none", "off", "on"}, f.Arm)
	if f.Kept {
		require.Equal(t, "on", f.Arm)
		require.Equal(t, "busy_wait_full", skip)
		require.GreaterOrEqual(t, f.BusyN, int64(1))
		require.Equal(t, "none", f.Abandon)
	}
	switch f.Arm {
	case "off":
		require.False(t, f.Kept)
		require.Equal(t, "none", f.Abandon)
	case "none":
		require.Contains(t, []string{"none", "policy_off"}, f.Abandon)
	}
	if f.Abandon == "not_busy_reason" {
		require.NotEqual(t, "busy_wait_full", skip)
	}
	switch f.Abandon {
	case "retry", "unhealthy", "consecutive", "hold_time", "owner_cap", "group_budget", "guard", "state_error":
		require.Equal(t, "on", f.Arm)
		require.Equal(t, "busy_wait_full", skip)
	}
}

func TestL07TraceFieldsWithoutRecord(t *testing.T) {
	ctx, _ := newL07Ctx(t)
	require.Equal(t, L07TraceFields{Arm: "none", Abandon: "policy_off"}, L07TraceFieldsFromContext(ctx, "busy_wait_full"))
	require.Equal(t, L07TraceFields{Arm: "none", Abandon: "none"}, L07TraceFieldsFromContext(ctx, "busy_quota_full"))
	require.Equal(t, L07TraceFields{Arm: "none", Abandon: "none"}, L07TraceFieldsFromContext(ctx, "none"))
	for _, skip := range []string{"busy_wait_full", "busy_quota_full", "gate_check", "none"} {
		requireL07TraceRules(t, L07TraceFieldsFromContext(ctx, skip), skip)
	}
}
