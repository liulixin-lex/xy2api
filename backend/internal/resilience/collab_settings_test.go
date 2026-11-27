package resilience

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollabSettingsDefaultsRoundTrip(t *testing.T) {
	for _, raw := range []string{"", "{}", `{"enabled":true}`, `{"collab":{}}`} {
		settings, err := Parse(raw)
		require.NoError(t, err)
		require.Equal(t, DefaultCollabSettings(), settings.Collab)
		require.True(t, settings.CollabRecordsOn())
		require.False(t, settings.CollabLadderOn())
		require.False(t, settings.CollabLosslessFirstOn())
		require.False(t, settings.CollabIncrementalDeclarationsOn())
		require.False(t, settings.CollabTreeAffinityOn())
		require.False(t, settings.CollabStrictMarkerVetoOn())
		require.False(t, settings.CollabRecordRoutingOn())
		require.False(t, settings.CollabLocalEndingOn(1))
	}
	settings, err := Parse(`{"collab":{"records":false,"ladder":true,"local_ending":{"mode":"groups","groups":[3,1,3]}}}`)
	require.NoError(t, err)
	require.False(t, settings.CollabRecordsOn())
	require.Equal(t, []int64{1, 3}, settings.Collab.LocalEnding.Groups)
	require.True(t, settings.CollabLocalEndingOn(1))
	require.False(t, settings.CollabLocalEndingOn(2))
	encoded, err := json.Marshal(settings)
	require.NoError(t, err)
	parsed, err := Parse(string(encoded))
	require.NoError(t, err)
	require.Equal(t, settings, parsed)
	require.Contains(t, string(encoded), `"records":false`)
}

func TestCollabSettingsGates(t *testing.T) {
	s := Defaults()
	s.Collab = CollabSettings{IncrementalDeclarations: true, Records: true, TreeAffinity: true, Ladder: true, LosslessFirst: true, RecordRouting: true, StrictMarkerVeto: true, LocalEnding: CollabLocalEndingSettings{Mode: CollabLocalEndingAll}}
	check := func(s *Settings, want bool) {
		require.Equal(t, want, s.CollabIncrementalDeclarationsOn())
		require.Equal(t, want, s.CollabRecordsOn())
		require.Equal(t, want, s.CollabRecoveryRecordsWriteOn())
		require.Equal(t, want, s.CollabTreeAffinityOn())
		require.Equal(t, want, s.CollabLadderOn())
		require.Equal(t, want, s.CollabLosslessFirstOn())
		require.Equal(t, want, s.CollabRecordRoutingOn())
		require.Equal(t, want, s.CollabStrictMarkerVetoOn())
		require.Equal(t, want, s.CollabLocalEndingOn(1))
	}
	check(&s, true)
	s.Collab.Ladder = false
	require.False(t, s.CollabRecordRoutingOn())
	require.False(t, s.CollabLosslessFirstOn())
	require.False(t, s.CollabLocalEndingOn(1))
	require.True(t, s.CollabRecordsOn())
	require.False(t, s.CollabRecoveryRecordsWriteOn())
	s.Enabled = false
	check(&s, false)
	check(nil, false)
}

func TestCollabSettingsInvalid(t *testing.T) {
	for _, raw := range []string{
		`{"collab":null}`,
		`{"collab":{"records":null}}`,
		`{"collab":{"local_ending":null}}`,
		`{"collab":{"local_ending":{"mode":null}}}`,
		`{"collab":{"local_ending":{"mode":""}}}`,
		`{"collab":{"local_ending":{"mode":"ALL"}}}`,
		`{"collab":{"local_ending":{"groups":null}}}`,
		`{"collab":{"local_ending":{"groups":[null]}}}`,
		`{"collab":{"local_ending":{"groups":[0]}}}`,
		`{"collab":{"local_ending":{"groups":[-1]}}}`,
		`{"collab":{"unknown":true}}`,
	} {
		// Strict rejection belongs to admin input, not stored-document Parse.
		var fields struct{ Collab json.RawMessage }
		require.NoError(t, json.Unmarshal([]byte(raw), &fields))
		require.Error(t, CheckCollabSettingsJSON(fields.Collab), raw)
	}
}

func TestCollabSettingsFreezeAndClone(t *testing.T) {
	before := Current().Clone()
	t.Cleanup(func() { Publish(before) })
	s := Defaults()
	s.Collab.LocalEnding = CollabLocalEndingSettings{Mode: CollabLocalEndingGroups, Groups: []int64{1, 2}}
	s.Collab.Ladder = true
	Publish(s)
	state := NewRequestState()
	clone := s.Clone()
	clone.Collab.LocalEnding.Groups[0] = 3
	require.Equal(t, []int64{1, 2}, s.Collab.LocalEnding.Groups)
	normalized := s.Normalized()
	normalized.Collab.LocalEnding.Groups[0] = 4
	require.Equal(t, []int64{1, 2}, s.Collab.LocalEnding.Groups)
	s.Collab.Ladder = false
	s.Collab.LocalEnding.Groups[0] = 9
	Publish(s)
	snapshot := state.CollabSettingsSnapshot()
	require.True(t, snapshot.CollabLocalEndingOn(1))
	copyForMutation := snapshot.Clone()
	copyForMutation.Collab.LocalEnding.Groups[0] = 8
	require.Same(t, snapshot, state.CollabSettingsSnapshot())
	require.Equal(t, []int64{1, 2}, state.CollabSettingsSnapshot().Collab.LocalEnding.Groups)
	ctx := WithRequestState(context.Background(), state)
	require.Same(t, state, StateFrom(ctx))
	frozen := state.CollabSettingsSnapshot()
	require.True(t, frozen.CollabLocalEndingOn(1))
	var missing *RequestState
	off := missing.CollabSettingsSnapshot()
	require.False(t, off.CollabRecordsOn())
}

func TestCollabStoredSettingsTolerateFutureValues(t *testing.T) {
	for _, collab := range []string{
		`null`, `[]`, `"future"`,
		`{"future_switch":true,"records":false,"ladder":true}`,
		`{"records":null,"ladder":true}`,
		`{"records":false,"lossless_first":"future","ladder":true}`,
		`{"records":false,"local_ending":null}`,
		`{"records":false,"local_ending":{"mode":"future","groups":[2]}}`,
		`{"records":false,"local_ending":{"mode":"groups","groups":[null]}}`,
		`{"records":false,"local_ending":{"mode":"groups","groups":[-1]}}`,
		`{"records":false,"local_ending":{"future_field":null,"mode":"groups","groups":[2]}}`,
	} {
		before := CollabSafetySnapshot().SettingsCorrections
		raw := `{"enabled":false,"collaboration_plaintext":{"enabled":true,"models":["synthetic-model"]},"budget":{"max_attempts":7},"stall":{"text_stall_seconds":90},"collab":` + collab + `}`
		settings, err := Parse(raw)
		require.NoError(t, err, collab)
		require.False(t, settings.Enabled)
		require.Equal(t, []string{"synthetic-model"}, settings.CollaborationPlaintext.Models)
		require.True(t, settings.CollaborationPlaintext.Enabled)
		require.Equal(t, 7, settings.Budget.MaxAttempts)
		require.Equal(t, 90, settings.Stall.TextStallSeconds)
		require.Greater(t, CollabSafetySnapshot().SettingsCorrections, before)
		if strings.Contains(collab, `"records":false`) {
			require.False(t, settings.Collab.Records)
		}
		if strings.Contains(collab, `"ladder":true`) {
			require.True(t, settings.Collab.Ladder)
		}
	}
	settings, err := Parse(`{"collab":{"records":false,"local_ending":{"mode":"future","groups":[2]}}}`)
	require.NoError(t, err)
	require.Equal(t, CollabLocalEndingOff, settings.Collab.LocalEnding.Mode)
	require.Equal(t, []int64{2}, settings.Collab.LocalEnding.Groups)
}
