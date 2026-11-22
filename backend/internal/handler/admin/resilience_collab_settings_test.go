package admin

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateResilienceSettingsCollab(t *testing.T) {
	before := resilience.Current().Clone()
	t.Cleanup(func() { resilience.Publish(before) })
	initial := resilience.Defaults()
	initial.Collab.Ladder = true
	initial.Collab.LosslessFirst = true
	initial.Collab.LocalEnding = resilience.CollabLocalEndingSettings{Mode: resilience.CollabLocalEndingGroups, Groups: []int64{1, 2}}
	resilience.Publish(initial)
	live := resilience.Current()
	h := NewResilienceHandler(service.NewResilienceService(nil, nil))
	for _, body := range []string{
		`{"collab":null}`,
		`{"collab":{"lossless_first":null}}`,
		`{"collab":{"local_ending":{"mode":"future"}}}`,
		`{"collab":{"records":null}}`,
		`{"collab":{"local_ending":null}}`,
		`{"collab":{"local_ending":{"mode":null}}}`,
		`{"collab":{"local_ending":{"mode":""}}}`,
		`{"collab":{"local_ending":{"groups":null}}}`,
		`{"collab":{"local_ending":{"groups":[null]}}}`,
		`{"collab":{"local_ending":{"groups":[0]}}}`,
		`{"collab":{"local_ending":{"groups":[3]},"typo":true}}`,
		`{"Collab":{"Records":null}}`,
		`{"collab":{"records":false},"collab":null}`,
		`{"collab":{"local_ending":{"mode":""}},"collab":{"records":false}}`,
	} {
		require.Equal(t, http.StatusBadRequest, putResilienceSettings(t, h, body), body)
		require.Same(t, live, resilience.Current())
		require.Equal(t, []int64{1, 2}, live.Collab.LocalEnding.Groups)
	}
	for _, body := range []string{`{}`, `{"collab":{}}`, `{"collab":{"local_ending":{}}}`} {
		require.Equal(t, http.StatusOK, putResilienceSettings(t, h, body))
		require.Equal(t, initial.Collab, resilience.Current().Collab)
	}
	require.Equal(t, http.StatusOK, putResilienceSettings(t, h, `{"collab":{"records":false,"local_ending":{"groups":[]}}}`))
	require.False(t, resilience.Current().Collab.Records)
	require.True(t, resilience.Current().Collab.Ladder)
	require.Equal(t, resilience.CollabLocalEndingGroups, resilience.Current().Collab.LocalEnding.Mode)
	require.Empty(t, resilience.Current().Collab.LocalEnding.Groups)
	require.Equal(t, []int64{1, 2}, live.Collab.LocalEnding.Groups)
	require.Equal(t, http.StatusOK, putResilienceSettings(t, h, `{"collab":{"local_ending":{"groups":[3,1,3]}}}`))
	require.Equal(t, []int64{1, 3}, resilience.Current().Collab.LocalEnding.Groups)
	require.False(t, resilience.Current().Collab.Records)
}
