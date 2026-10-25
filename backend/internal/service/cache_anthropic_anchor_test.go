//go:build unit

package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type anchorReferenceVectors struct {
	Secret string `json:"secret"`
	Cases  []struct {
		Name  string `json:"name"`
		Input struct {
			APIKeyID       int64    `json:"api_key_id"`
			ServingGroupID int64    `json:"serving_group_id"`
			Model          string   `json:"model"`
			System         string   `json:"system"`
			Users          []string `json:"users"`
		} `json:"input"`
		Expected struct {
			NormalizedFields []string `json:"normalized_fields"`
			FieldLengths     []int    `json:"field_lengths"`
			MessageHex       string   `json:"message_hex"`
			Anchor           string   `json:"anchor"`
			Cohort           *int     `json:"cohort"`
		} `json:"expected"`
	} `json:"cases"`
	Models map[string]string `json:"model_table"`
}

func readAnchorReferenceVectors(t *testing.T) anchorReferenceVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/cache_anchor_v2/reference-vectors.json")
	require.NoError(t, err)
	var v anchorReferenceVectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.Len(t, v.Cases, 10)
	return v
}

func TestAnchorModelFamilyGolden(t *testing.T) {
	for model, want := range readAnchorReferenceVectors(t).Models {
		t.Run(model, func(t *testing.T) { require.Equal(t, want, anchorModelFamily(model)) })
	}
	for model, want := range map[string]string{
		" PUBLISHERS/GOOGLE/MODELS/GEMINI-2.5-FLASH-LITE@001 ": "gemini-flash-lite",
		"eu.anthropic.claude-3-7-sonnet-20250219-v1:0":         "claude-sonnet",
		"gemini-2.5-pro-experimental-thinking":                 "gemini-pro",
		"gemini-2.5-flash-latest-pro":                          "gemini-flash",
		"claude-123":                                           "claude",
		"private_model-5@revision":                             "other:private_model-5",
	} {
		require.Equal(t, want, anchorModelFamily(model))
	}
}

func TestAnchorV2ByteGolden(t *testing.T) {
	vectors := readAnchorReferenceVectors(t)
	for _, vector := range vectors.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			system, err := json.Marshal(vector.Input.System)
			require.NoError(t, err)
			messages := make([]map[string]any, 0, len(vector.Input.Users))
			for _, user := range vector.Input.Users {
				messages = append(messages, map[string]any{"role": "user", "content": user})
			}
			raw, err := json.Marshal(messages)
			require.NoError(t, err)
			got := deriveCacheAnchorV2(vectors.Secret, vector.Input.APIKeyID, vector.Input.ServingGroupID, vector.Input.Model, PlatformAnthropic, system, raw)
			require.Equal(t, vector.Expected.Anchor, got)
			if vector.Expected.Cohort == nil {
				_, ok := (CacheAnchorV2{}).cohortForGroup(vector.Input.ServingGroupID)
				require.False(t, ok)
				return
			}
			// The independently recorded transcript locks field order, byte lengths,
			// the NUL domain separator and the 128-bit output truncation.
			message, err := hex.DecodeString(vector.Expected.MessageHex)
			require.NoError(t, err)
			h := hmac.New(sha256.New, []byte(vectors.Secret))
			_, _ = h.Write(message)
			require.Equal(t, got, "a2:"+hex.EncodeToString(h.Sum(nil)[:16]))
			anchor := CacheAnchorV2{value: got, servingGroupID: vector.Input.ServingGroupID}
			bucket, ok := anchor.cohortForGroup(vector.Input.ServingGroupID)
			require.True(t, ok)
			require.Equal(t, *vector.Expected.Cohort, bucket)
		})
	}
}

func TestAnchorV2NormalizesOnlyEligibleText(t *testing.T) {
	cases := []struct{ name, platform, system, messages, wantSystem, wantUser string }{
		{"anthropic", PlatformAnthropic,
			`[{"type":"text","text":"  alpha\u00a0beta "},{"type":"thinking","text":"hidden"},{"type":"text","text":"<system-reminder>ignore</system-reminder>"},{"type":"text","text":"gamma"}]`,
			`[{"role":"assistant","content":"ignore"},{"role":"user","content":[{"type":"tool_result","content":"ignore"},{"type":"text","text":" <system-reminder>hint</system-reminder> "}]},{"role":"user","content":[{"type":"text","text":"first\nrequest"},{"type":"image","text":"ignore"},{"type":"text","text":"尾部"}]},{"role":"user","content":"later turn"}]`,
			"alpha beta gamma", "first request 尾部"},
		{"gemini", PlatformGemini,
			`[{"text":"alpha\u3000beta"},{"inlineData":{"data":"ignore"}},{"text":"gamma"}]`,
			`[{"role":"model","parts":[{"text":"ignore"}]},{"parts":[{"text":"<system-reminder>hint</system-reminder>"}]},{"parts":[{"functionCall":{"name":"ignore"}},{"text":"first\trequest"},{"text":"尾部"}]}]`,
			"alpha beta gamma", "first request 尾部"},
		{"mixed reminder stays", PlatformAnthropic, `"<system-reminder>hint</system-reminder> suffix"`,
			`[{"role":"user","content":"prefix <system-reminder>hint</system-reminder>"}]`,
			"<system-reminder>hint</system-reminder> suffix", "prefix <system-reminder>hint</system-reminder>"},
		{"no assistant fallback", PlatformAnthropic, `null`, `[{"role":"assistant","content":"ignore"}]`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			system, user := anchorRequestText(tc.platform, []byte(tc.system), []byte(tc.messages))
			for _, item := range []struct {
				blocks []string
				want   string
			}{{system, tc.wantSystem}, {user, tc.wantUser}} {
				var dst bytes.Buffer
				require.Equal(t, uint64(len(item.want)), writeNormalizedAnchorText(nil, item.blocks))
				require.Equal(t, uint64(len(item.want)), writeNormalizedAnchorText(&dst, item.blocks))
				require.Equal(t, item.want, dst.String())
			}
		})
	}
}

func TestAnchorV2TenantAndServingScope(t *testing.T) {
	derive := func(key, group int64, secret string) string {
		return deriveCacheAnchorV2(secret, key, group, "claude-sonnet-4-5", PlatformAnthropic, []byte(`"system"`), []byte(`[{"role":"user","content":"same first user"}]`))
	}
	a := derive(101, 31, "synthetic-secret")
	b := derive(101, 77, "synthetic-secret")
	require.NotEmpty(t, a)
	require.NotEqual(t, a, b)
	require.NotEqual(t, a, derive(102, 31, "synthetic-secret"))
	require.NotEqual(t, a, derive(101, 31, "rotated-synthetic-secret"))
	require.Equal(t, a, derive(101, 31, "synthetic-secret"), "A→B→A restores A's identity")
	for _, tc := range []struct {
		key, group int64
		secret     string
	}{{0, 31, "x"}, {-1, 31, "x"}, {101, 0, "x"}, {101, -1, "x"}, {101, 31, ""}} {
		require.Empty(t, derive(tc.key, tc.group, tc.secret))
	}
	anchor := CacheAnchorV2{value: a, servingGroupID: 31, policyRev: 7, armed: true}
	require.Equal(t, a, anchor.sessionForGroup(31))
	for _, group := range []int64{0, 77} {
		require.Empty(t, anchor.candidateForGroup(group))
		require.Empty(t, anchor.sessionForGroup(group))
		_, ok := anchor.cohortForGroup(group)
		require.False(t, ok, "mismatched serving scope must not even enter cohort")
	}
	anchor.armed = false
	require.Empty(t, anchor.sessionForGroup(31))
	require.Equal(t, a, anchor.candidateForGroup(31), "control arm retains candidate only")
}

func TestAnchorV2DoesNotTruncateOrUseLaterTurns(t *testing.T) {
	system := strings.Repeat("abc 界\u2003", 20000)
	first := strings.Repeat("text\t", 20000)
	derive := func(system, first, later string) string {
		s, err := json.Marshal(system)
		require.NoError(t, err)
		m, err := json.Marshal([]map[string]string{{"role": "user", "content": first}, {"role": "assistant", "content": "answer"}, {"role": "user", "content": later}})
		require.NoError(t, err)
		return deriveCacheAnchorV2("synthetic-secret", 101, 31, "claude-sonnet-4-5", PlatformAnthropic, s, m)
	}
	a := derive(system, first, "turn two")
	require.NotEmpty(t, a)
	require.Equal(t, a, derive(system, first, "different turn two"))
	require.NotEqual(t, a, derive(system+"tail", first, "turn two"))
	require.NotEqual(t, a, derive(system, first+"tail", "turn two"))
}

func TestAnchorV2NestedRolloutAndCapability(t *testing.T) {
	// A fixed independently recorded cohort (37) locks exact threshold edges.
	a := CacheAnchorV2{value: "a2:98092e98963b2b031eceda698ce10a10", servingGroupID: 31}
	require.Equal(t, "off", a.armForGroup(31, 37, 0, false))
	require.Equal(t, "l04_only", a.armForGroup(31, 38, 0, false))
	require.Equal(t, "l04_only", a.armForGroup(31, 100, 100, false), "T2.2 must never emit both while L03 is absent")
	require.Equal(t, "l04_only", a.armForGroup(31, 100, 37, true))
	require.Equal(t, "both", a.armForGroup(31, 100, 38, true))
	require.Equal(t, "l04_only", a.armForGroup(31, 100, 0, true), "L03 rollback preserves L04 assignment")
	require.Equal(t, "none", a.armForGroup(77, 100, 100, true))
	require.Equal(t, "none", (CacheAnchorV2{}).armForGroup(31, 100, 100, true))
}
