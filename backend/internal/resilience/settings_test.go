package resilience

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloneSharesNothing(t *testing.T) {
	s := Defaults()
	s.Budget.ModelOverrides = map[string]int{"m": 200}
	c := s.Clone()
	c.Budget.ModelOverrides["m"] = 1
	c.Keepalive.Edges[0] = "x"
	c.Failure.BalanceKeywordsExtra = append(c.Failure.BalanceKeywordsExtra, "y")
	require.Equal(t, 200, s.Budget.ModelOverrides["m"])
	require.Equal(t, "cf", s.Keepalive.Edges[0])
	require.Empty(t, s.Failure.BalanceKeywordsExtra)
}

func TestC0DefaultsAndGates(t *testing.T) {
	s := Defaults()
	require.True(t, s.Stream.PreludeStaging)
	require.True(t, s.Stream.NativePreludeWhitelist)
	require.True(t, s.Stream.StrictTerminal)
	require.True(t, s.Failure.DecisionV2)
	require.True(t, s.Errors.FailedFrameRetryAfter)
	require.Equal(t, 10, s.Errors.CodexRetryAfterCapSeconds)
	require.True(t, s.Capability.StreamOnlyLearning)
	require.Equal(t, 21600, s.Capability.LearnTTLSeconds)
	require.True(t, s.PreludeStagingOn())
	require.True(t, s.NativePreludeWhitelistOn())
	require.True(t, s.StrictTerminalOn())
	require.True(t, s.DecisionV2On())
	require.True(t, s.FailedFrameRetryAfterOn())
	require.True(t, s.CodexRetryAfterCapOn())
	require.True(t, s.StreamOnlyLearningOn())
	require.True(t, s.StreamOnlyFailoverOn())

	s.Enabled = false
	require.False(t, s.PreludeStagingOn())
	require.False(t, s.NativePreludeWhitelistOn())
	require.False(t, s.StrictTerminalOn())
	require.False(t, s.DecisionV2On())
	require.False(t, s.FailedFrameRetryAfterOn())
	require.False(t, s.CodexRetryAfterCapOn())
	require.False(t, s.StreamOnlyLearningOn())
	require.False(t, s.StreamOnlyFailoverOn())

	s = Defaults()
	s.Failure.DecisionV2 = false
	require.False(t, s.StreamOnlyFailoverOn(), "capability failover depends on decision_v2")
	require.True(t, s.StreamOnlyLearningOn(), "learning remains independent of failover")
	s.Errors.CodexRetryAfterCapSeconds = 0
	require.False(t, s.CodexRetryAfterCapOn(), "zero disables the Codex cap")
}

func TestC0SettingsValidateNewRanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Settings)
	}{
		{name: "codex cap below range", mutate: func(s *Settings) { s.Errors.CodexRetryAfterCapSeconds = -1 }},
		{name: "codex cap above range", mutate: func(s *Settings) { s.Errors.CodexRetryAfterCapSeconds = 31 }},
		{name: "learning TTL below range", mutate: func(s *Settings) { s.Capability.LearnTTLSeconds = 599 }},
		{name: "learning TTL above range", mutate: func(s *Settings) { s.Capability.LearnTTLSeconds = 86401 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Defaults()
			tc.mutate(&s)
			require.Error(t, s.Validate())
		})
	}
	for _, ttl := range []int{600, 86400} {
		s := Defaults()
		s.Capability.LearnTTLSeconds = ttl
		require.NoError(t, s.Validate())
	}
	for _, capSeconds := range []int{0, 30} {
		s := Defaults()
		s.Errors.CodexRetryAfterCapSeconds = capSeconds
		require.NoError(t, s.Validate())
	}
}

func TestC0LegacySettingsKeepNewSwitchDefaults(t *testing.T) {
	old := legacy043Defaults()
	old.Failure.TransientCooldownSeconds = 57
	old.Budget.ModelOverrides["example-model"] = 90
	old.Session.Enabled = false
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	s, err := Parse(string(raw))
	require.NoError(t, err)
	require.Equal(t, 57, s.Failure.TransientCooldownSeconds)
	require.Equal(t, 90, s.Budget.ModelOverrides["example-model"])
	require.False(t, s.Session.Enabled)
	require.True(t, s.Stream.PreludeStaging)
	require.True(t, s.Stream.NativePreludeWhitelist)
	require.True(t, s.Stream.StrictTerminal)
	require.True(t, s.Failure.DecisionV2)
	require.True(t, s.Errors.FailedFrameRetryAfter)
	require.Equal(t, 10, s.Errors.CodexRetryAfterCapSeconds)
	require.True(t, s.Capability.StreamOnlyLearning)
	require.Equal(t, 21600, s.Capability.LearnTTLSeconds)
}

func TestC0StoredDocumentDowngradesTo043(t *testing.T) {
	require.Equal(t, legacy043SettingKey, SettingKey)
	old := legacy043Defaults()
	old.Failure.TransientCooldownSeconds = 57
	old.Failure.BalanceKeywordsExtra = []string{"custom balance signal"}
	old.Budget.ModelOverrides["example-model"] = 90
	old.Keepalive.Edges = []string{"esa"}
	old.Session.Enabled = false
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	next, err := Parse(string(raw))
	require.NoError(t, err)
	next.Stream.PreludeStaging = false
	next.Failure.DecisionV2 = false
	next.Errors.CodexRetryAfterCapSeconds = 0
	next.Capability.LearnTTLSeconds = 7200
	newRaw, err := json.Marshal(next)
	require.NoError(t, err)
	downgraded, err := legacy043Parse(string(newRaw))
	require.NoError(t, err)
	require.Equal(t, old, downgraded, "every old setting survives reading a new document")

	// The old administrator PUT is strict, unlike its stored-document reader.
	dec := json.NewDecoder(bytes.NewReader(newRaw))
	dec.DisallowUnknownFields()
	require.Error(t, dec.Decode(&downgraded))

	// Saving through the old administrator drops new fields. A subsequent
	// upgrade retains all old settings and initializes new fields to defaults.
	oldRaw, err := json.Marshal(old)
	require.NoError(t, err)
	upgraded, err := Parse(string(oldRaw))
	require.NoError(t, err)
	require.True(t, upgraded.Stream.PreludeStaging)
	require.True(t, upgraded.Failure.DecisionV2)
	require.Equal(t, 10, upgraded.Errors.CodexRetryAfterCapSeconds)
	require.Equal(t, 21600, upgraded.Capability.LearnTTLSeconds)
}

func TestC0ExplicitOffSurvivesParseCloneAndPublish(t *testing.T) {
	s, err := Parse(`{"stream":{"prelude_staging":false,"native_prelude_whitelist":false,"strict_terminal":false},"failure":{"decision_v2":false},"errors":{"failed_frame_retry_after":false,"codex_retry_after_cap_seconds":0},"capability":{"stream_only_learning":false,"learn_ttl_seconds":600}}`)
	require.NoError(t, err)
	before := Current().Clone()
	t.Cleanup(func() { Publish(before) })
	Publish(s)
	raw, err := json.Marshal(Current().Clone().Normalized())
	require.NoError(t, err)
	roundtrip, err := Parse(string(raw))
	require.NoError(t, err)
	require.Equal(t, s, roundtrip)
	for _, snapshot := range []*Settings{&roundtrip, nil} {
		require.False(t, snapshot.PreludeStagingOn())
		require.False(t, snapshot.NativePreludeWhitelistOn())
		require.False(t, snapshot.StrictTerminalOn())
		require.False(t, snapshot.DecisionV2On())
		require.False(t, snapshot.FailedFrameRetryAfterOn())
		require.False(t, snapshot.CodexRetryAfterCapOn())
		require.False(t, snapshot.StreamOnlyLearningOn())
		require.False(t, snapshot.StreamOnlyFailoverOn())
	}
	s = Defaults()
	s.Errors.Enabled = false
	require.False(t, s.FailedFrameRetryAfterOn())
	require.False(t, s.CodexRetryAfterCapOn())
	require.True(t, s.PreludeStagingOn(), "error semantics does not gate streams")
}
