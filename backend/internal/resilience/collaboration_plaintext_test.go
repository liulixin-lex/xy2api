package resilience

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollaborationPlaintextSettings(t *testing.T) {
	s, err := Parse(`{}`)
	require.NoError(t, err)
	require.False(t, s.CollaborationPlaintext.Enabled)
	require.Equal(t, []string{}, s.CollaborationPlaintext.Models)
	s, err = Parse(`{"collaboration_plaintext":{"enabled":true,"models":[" GPT-X ","gpt-x","gpt-y"]}}`)
	require.NoError(t, err)
	require.Equal(t, []string{"GPT-X", "gpt-y"}, s.CollaborationPlaintext.Models)
	require.True(t, s.CollaborationPlaintextModel(" gPt-X "))
	require.False(t, s.CollaborationPlaintextModel("gpt-x-extra"))
	s.Enabled = false
	require.False(t, s.CollaborationPlaintextModel("gpt-x"))
	s.Enabled, s.CollaborationPlaintext.Enabled = true, false
	require.False(t, s.CollaborationPlaintextModel("gpt-x"))
	var absent *Settings
	require.False(t, absent.CollaborationPlaintextModel("gpt-x"))
	s.CollaborationPlaintext.Models = nil
	raw, err := json.Marshal(s.Clone())
	require.NoError(t, err)
	require.Contains(t, string(raw), `"models":[]`)
	for _, n := range []int{1, 128, 129} {
		s.CollaborationPlaintext.Models = []string{strings.Repeat("模", n)}
		if n <= 128 {
			require.NoError(t, s.Validate())
		} else {
			require.Error(t, s.Validate())
		}
	}
	s.CollaborationPlaintext.Models = []string{"  "}
	require.Error(t, s.Validate())
	for _, n := range []int{16, 17} {
		s.CollaborationPlaintext.Models = nil
		for i := 0; i < n; i++ {
			s.CollaborationPlaintext.Models = append(s.CollaborationPlaintext.Models, fmt.Sprintf("m%d", i))
		}
		if n == 16 {
			require.NoError(t, s.Validate())
		} else {
			require.Error(t, s.Validate())
		}
	}
}

func TestCollaborationPlaintextPublishIsolationRoundTrip(t *testing.T) {
	before := Current().Clone()
	t.Cleanup(func() { Publish(before) })
	s := Defaults()
	s.CollaborationPlaintext = CollaborationPlaintextSettings{Enabled: true, Models: []string{"gpt-x"}}
	Publish(s)
	s.CollaborationPlaintext.Models[0] = "changed"
	require.True(t, Current().CollaborationPlaintextModel("gpt-x"))
	clone := Current().Clone()
	clone.CollaborationPlaintext.Models[0] = "other"
	require.True(t, Current().CollaborationPlaintextModel("gpt-x"))
	raw, err := json.Marshal(Current())
	require.NoError(t, err)
	parsed, err := Parse(string(raw))
	require.NoError(t, err)
	require.Equal(t, *Current(), parsed)
}
