//go:build unit

package operatoraccess

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecideLevelsAnyOfAndFlags(t *testing.T) {
	for _, held := range []Level{LevelNone, LevelView, LevelManage} {
		for _, need := range []Level{LevelView, LevelManage} {
			p := Permissions{Modules: map[Module]Level{ModuleAccounts: held}}
			e := Entry{Kind: KindModule, Module: ModuleAccounts, Level: need}
			d := Decide(e, true, p)
			require.Equal(t, held < need, d.Reason != "")
		}
	}
	p := Permissions{Modules: map[Module]Level{ModuleUsage: LevelView}, Flags: map[Flag]bool{}}
	e := Entry{Kind: KindModule, Module: ModuleAccounts, Level: LevelView, AnyOf: []Module{ModuleUsage, ModuleGroups}, Flags: []Flag{FlagDataExport}}
	require.Equal(t, string(FlagDataExport), Decide(e, true, p).Required)
	p.Flags[FlagDataExport] = true
	require.Empty(t, Decide(e, true, p).Reason)
	e.Level = LevelManage
	d := Decide(e, true, p)
	require.Equal(t, "accounts:manage", d.Required)
	require.Equal(t, "usage:view,groups:view", d.AnyOf)
	require.Equal(t, "catalog", Decide(e, false, p).Required)
	require.Equal(t, "SUPER_ADMIN_REQUIRED", Decide(Entry{Kind: KindSuperOnly}, true, p).Reason)
	require.Empty(t, Decide(Entry{Kind: KindAnyAdmin}, true, p).Reason)
	require.Equal(t, "catalog", Decide(Entry{}, true, p).Required)
	require.False(t, p.Can(ModuleUsage, LevelNone))
}
func TestDecodeWireRejectsUnknownValues(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"other":{}}`, `{"Modules":{}}`, `{"modules":{"unknown":"view"}}`, `{"modules":{"accounts":"none"}}`, `{"modules":{"accounts":""}}`, `{"modules":{"accounts":null}}`, `{"flags":["unknown"]}`, `{"flags":{}}`, `{"modules":true}`} {
		_, err := DecodeWire([]byte(raw))
		require.ErrorIs(t, err, ErrInvalid, raw)
	}
	for _, raw := range []string{`{}`, `{"modules":null,"flags":null}`} {
		p, err := DecodeWire([]byte(raw))
		require.NoError(t, err)
		require.Equal(t, Wire{Modules: map[string]string{}, Flags: []string{}}, p.Wire())
	}
}
func TestWireSortsFlags(t *testing.T) {
	p, err := DecodeWire([]byte(`{"modules":{"usage":"manage"},"flags":["data.export","accounts.upstream_address","data.export"]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"accounts.upstream_address", "data.export"}, p.Wire().Flags)
	raw, err := json.Marshal(Permissions{}.Wire())
	require.NoError(t, err)
	require.JSONEq(t, `{"modules":{},"flags":[]}`, string(raw))
}
