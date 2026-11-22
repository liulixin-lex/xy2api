package resilience

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFCOSettingsCloneReload(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		d := Defaults()
		require.Equal(t, FCOCompatModeReactive, d.FCOCompat.Mode)
		require.NotNil(t, d.FCOCompat.AdminNames)
		require.Empty(t, d.FCOCompat.AdminNames)
		require.NoError(t, d.Validate())
		require.Equal(t, FCOCompatModeReactive, d.FCOCompatMode())
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"fco_compat":{"mode":"reactive","admin_names":[]}`)
	})

	t.Run("documents without the field keep the defaults", func(t *testing.T) {
		old := legacy043Defaults()
		old.Failure.TransientCooldownSeconds = 57
		legacyRaw, err := json.Marshal(old)
		require.NoError(t, err)
		for _, raw := range []string{
			"",
			`{"enabled":true}`,
			string(legacyRaw),
			`{"fco_compat":null}`,
			`{"fco_compat":{}}`,
			`{"fco_compat":{"mode":"","admin_names":null}}`,
		} {
			s, err := Parse(raw)
			require.NoError(t, err, raw)
			require.Equal(t, FCOCompatModeReactive, s.FCOCompat.Mode, raw)
			require.NotNil(t, s.FCOCompat.AdminNames, raw)
			require.Empty(t, s.FCOCompat.AdminNames, raw)
			out, err := json.Marshal(s)
			require.NoError(t, err)
			require.Contains(t, string(out), `"admin_names":[]`, raw)
		}
		upgraded, err := Parse(string(legacyRaw))
		require.NoError(t, err)
		require.Equal(t, 57, upgraded.Failure.TransientCooldownSeconds)
	})

	t.Run("clone and publish share no admin names", func(t *testing.T) {
		s := Defaults()
		s.FCOCompat.AdminNames = []string{"alpha", "beta"}
		c := s.Clone()
		c.FCOCompat.AdminNames[0] = "mutated"
		c.FCOCompat.AdminNames = append(c.FCOCompat.AdminNames, "gamma")
		require.Equal(t, []string{"alpha", "beta"}, s.FCOCompat.AdminNames)

		before := Current().Clone()
		t.Cleanup(func() { Publish(before) })
		Publish(s)
		s.FCOCompat.AdminNames[0] = "changed_after_publish"
		require.Equal(t, []string{"alpha", "beta"}, Current().FCOCompat.AdminNames)
		snapshot := Current().Clone()
		snapshot.FCOCompat.AdminNames[1] = "changed_on_copy"
		require.Equal(t, []string{"alpha", "beta"}, Current().FCOCompat.AdminNames)
	})

	t.Run("stored modes and names survive reload", func(t *testing.T) {
		before := Current().Clone()
		t.Cleanup(func() { Publish(before) })
		for _, mode := range []string{FCOCompatModeOff, FCOCompatModeReactive, FCOCompatModeProactive} {
			s, err := Parse(fmt.Sprintf(`{"fco_compat":{"mode":%q,"admin_names":["Tool_Two","tool_one"]}}`, mode))
			require.NoError(t, err)
			Publish(s)
			raw, err := json.Marshal(Current().Clone().Normalized())
			require.NoError(t, err)
			roundtrip, err := Parse(string(raw))
			require.NoError(t, err)
			require.Equal(t, s, roundtrip)
			require.Equal(t, mode, roundtrip.FCOCompat.Mode)
			require.Equal(t, []string{"Tool_Two", "tool_one"}, roundtrip.FCOCompat.AdminNames)
		}
	})

	t.Run("names are de-duplicated and sorted", func(t *testing.T) {
		s, err := Parse(`{"fco_compat":{"admin_names":["zeta","alpha","zeta","Beta","alpha"]}}`)
		require.NoError(t, err)
		require.Equal(t, []string{"Beta", "alpha", "zeta"}, s.FCOCompat.AdminNames)

		names := make([]string, 0, FCOCompatMaxAdminNames+1)
		for i := 0; i < FCOCompatMaxAdminNames; i++ {
			names = append(names, fmt.Sprintf("name_%02d", i))
		}
		names = append(names, names[0])
		s = Defaults()
		s.FCOCompat.AdminNames = names
		s = s.Normalized()
		require.Len(t, s.FCOCompat.AdminNames, FCOCompatMaxAdminNames, "the cap counts distinct names")
		require.NoError(t, s.Validate())
	})

	t.Run("invalid modes are rejected", func(t *testing.T) {
		for _, mode := range []string{"OFF", "Reactive", " proactive", "proactive ", "passive", "inherit"} {
			s := Defaults()
			s.FCOCompat.Mode = mode
			err := s.Normalized().Validate()
			require.Error(t, err, mode)
			require.Contains(t, err.Error(), "fco_compat.mode must be off, reactive or proactive")
		}
		stored, err := Parse(`{"fco_compat":{"mode":"Proactive"}}`)
		require.Error(t, err)
		require.Equal(t, Defaults(), stored, "an invalid stored document falls back to the defaults")
	})

	t.Run("invalid and reserved names are rejected", func(t *testing.T) {
		for _, name := range []string{
			"", " leading", "trailing ", "with space", "dash-name", "dot.name", "名字", "tab\tname",
			strings.Repeat("a", 129),
		} {
			s := Defaults()
			s.FCOCompat.AdminNames = []string{name}
			err := s.Normalized().Validate()
			require.Error(t, err, "%q", name)
			require.Contains(t, err.Error(), "fco_compat.admin_names entries must be 1-128 ASCII letters, digits or underscores")
			require.False(t, ValidFCOCompatAdminName(name), "%q", name)
		}
		for _, name := range []string{
			"browser_info", "goal_objective", "untrusted_context", "ide_context", "untrusted_content",
			"proposed_plan", "terminal_info", "browser_tabs", "codex_apps_open_page",
			"codex_apps_selected_page_part_", "codex_apps_selected_page_part_1_of_2", "codex_apps_selected_page_part_x",
		} {
			require.True(t, IsFCOCompatReservedName(name), name)
			require.False(t, ValidFCOCompatAdminName(name), name)
			s := Defaults()
			s.FCOCompat.AdminNames = []string{name}
			err := s.Normalized().Validate()
			require.Error(t, err, name)
			require.Contains(t, err.Error(), "is reserved by a built-in rule")
		}
		for _, name := range []string{
			"a", "Z", "0", "_", "Browser_Info", "browser_info2", "codex_apps_selected_page_part", "codex_apps_open_page_v2",
			strings.Repeat("A", 128),
		} {
			require.False(t, IsFCOCompatReservedName(name), name)
			require.True(t, ValidFCOCompatAdminName(name), name)
			s := Defaults()
			s.FCOCompat.AdminNames = []string{name}
			require.NoError(t, s.Normalized().Validate(), name)
		}
		long := strings.Repeat("b", 4096)
		s := Defaults()
		s.FCOCompat.AdminNames = []string{long}
		err := s.Validate()
		require.Error(t, err)
		require.Less(t, len(err.Error()), 512, "an over-long name is not echoed in full")
	})

	t.Run("more than the cap is rejected", func(t *testing.T) {
		names := make([]string, 0, FCOCompatMaxAdminNames+1)
		for i := 0; i <= FCOCompatMaxAdminNames; i++ {
			names = append(names, fmt.Sprintf("name_%02d", i))
		}
		s := Defaults()
		s.FCOCompat.AdminNames = names
		err := s.Normalized().Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "fco_compat.admin_names allows at most 64 entries")
		s.FCOCompat.AdminNames = names[:FCOCompatMaxAdminNames]
		require.NoError(t, s.Normalized().Validate())
	})

	t.Run("global gate follows the master switch", func(t *testing.T) {
		var absent *Settings
		require.Equal(t, FCOCompatModeOff, absent.FCOCompatMode())
		s := Defaults()
		s.FCOCompat.Mode = FCOCompatModeProactive
		require.Equal(t, FCOCompatModeProactive, s.FCOCompatMode())
		s.Enabled = false
		require.Equal(t, FCOCompatModeOff, s.FCOCompatMode())
		s = Defaults()
		s.FCOCompat.Mode = FCOCompatModeOff
		require.Equal(t, FCOCompatModeOff, s.FCOCompatMode())
		s.FCOCompat.Mode = ""
		require.Equal(t, FCOCompatModeReactive, s.FCOCompatMode(), "an unset mode is the default")
		s.FCOCompat.Mode = "bogus"
		require.Equal(t, FCOCompatModeOff, s.FCOCompatMode(), "an unknown mode fails closed")
	})
}
