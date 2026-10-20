package sharesched

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPolicyIsValidAndInert(t *testing.T) {
	p := DefaultPolicy()
	require.NoError(t, p.Validate())
	require.Equal(t, SchedulerSub2API, p.Scheduler, "a new policy must not change routing until enabled")
	require.Equal(t, SchedulerSub2API, p.Effective())
	require.True(t, p.SessionKeep())
}

// TestDecodePolicyAcceptsRetiredFields: every field written by 0.3.0–0.4.3
// still decodes and has no effect (design §3.1).
func TestDecodePolicyAcceptsRetiredFields(t *testing.T) {
	p, err := DecodePolicy([]byte(`{"schema":1,"scheduler":"share","accounts":[
		{"account_id":244,"tier":1,"weight":50,"cache_domain":"anthropic-ws-a"},
		{"account_id":317,"tier":2,"weight":100,"drain":true,"drain_until":"2026-10-11T00:00:00Z"},
		{"account_id":9,"weight":0,"x":1}],
		"defaults":{"tier":"account_priority","weight":"equal"},
		"affinity":{"enabled":true,"key":"auto","ttl_seconds":60,"wait":{"mode":"fixed","max_ms":99999},
			"on_spill":"keep","soft_spill_max_seconds":1800},
		"cache":{"ttl_seconds":30,"realign":"off"},
		"tiers":{"on_saturated":"wait","on_failed":"stop","return":"immediate","stable_seconds":0,"return_deadline_seconds":600},
		"balance":{"unit":"requests","correction":"strong","window_minutes":5,"min_volume":0}}`))
	require.NoError(t, err)
	require.Equal(t, SchedulerShare, p.Effective())
	require.True(t, p.SessionKeep())
}

func TestDecodePolicyRejectsMalformedDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"unknown_top", `{"schema":1,"scheduler":"share","bogus":1}`, "unknown field"},
		{"unknown_affinity", `{"schema":1,"scheduler":"share","affinity":{"bogus":1}}`, "unknown field"},
		{"trailing", `{"schema":1,"scheduler":"share"} {}`, "trailing"},
		{"schema", `{"schema":2,"scheduler":"share"}`, "schema"},
		{"scheduler", `{"schema":1,"scheduler":"controlled"}`, "scheduler"},
		{"key", `{"schema":1,"scheduler":"share","affinity":{"key":"sticky"}}`, "affinity.key"},
		{"type", `{"schema":1,"scheduler":"share","affinity":{"enabled":"yes"}}`, "cannot unmarshal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePolicy([]byte(tc.body))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidationErrorListsEveryProblem(t *testing.T) {
	p := DefaultPolicy()
	p.Scheduler = "x"
	p.Affinity.Key = "y"
	err := p.Validate()
	var verr *ValidationError
	require.True(t, errors.As(err, &verr))
	require.Len(t, verr.Problems, 2)
}

func TestShadowRunsTheOriginalScheduler(t *testing.T) {
	p, err := DecodePolicy([]byte(`{"schema":1,"scheduler":"shadow"}`))
	require.NoError(t, err)
	require.Equal(t, SchedulerShadow, p.Scheduler, "the stored value is kept")
	require.Equal(t, SchedulerSub2API, p.Effective())
}

func TestSessionKeepReadsEveryStoredForm(t *testing.T) {
	for body, want := range map[string]bool{
		`{"schema":1,"scheduler":"share"}`:                                           true,
		`{"schema":1,"scheduler":"share","affinity":{"key":"auto"}}`:                 true,
		`{"schema":1,"scheduler":"share","affinity":{"key":"session"}}`:              true,
		`{"schema":1,"scheduler":"share","affinity":{"key":"prefix"}}`:               true,
		`{"schema":1,"scheduler":"share","affinity":{"key":"none"}}`:                 false,
		`{"schema":1,"scheduler":"share","affinity":{"enabled":false}}`:              false,
		`{"schema":1,"scheduler":"share","affinity":{"enabled":false,"key":"auto"}}`: false,
	} {
		p, err := DecodePolicy([]byte(body))
		require.NoError(t, err, body)
		require.Equal(t, want, p.SessionKeep(), body)
	}
}

func TestNewPolicyDocumentIsMinimal(t *testing.T) {
	raw := NewPolicyDocument(SchedulerShare, false)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, map[string]any{"schema": 1.0, "scheduler": "share", "accounts": []any{},
		"affinity": map[string]any{"enabled": true, "key": "none"}}, doc)
	p, err := DecodePolicy(raw)
	require.NoError(t, err)
	require.Equal(t, SchedulerShare, p.Effective())
	require.False(t, p.SessionKeep())
}

// TestPatchPolicyDocumentKeepsEverythingElse: a save changes the scheduler
// and session keeping only, so a rollback reads the original rules
// (design §3.2).
func TestPatchPolicyDocumentKeepsEverythingElse(t *testing.T) {
	stored := []byte(`{"schema":1,"scheduler":"shadow","accounts":[{"account_id":1,"tier":2,"weight":33}],
		"defaults":{"tier":"account_priority","weight":"equal"},
		"affinity":{"enabled":true,"key":"session","ttl_seconds":3600,"wait":{"mode":"auto","ms_per_1k_tokens":100,"max_ms":15000,"max_waiting":3},"on_spill":"rebind","soft_spill_max_seconds":300},
		"cache":{"ttl_seconds":0,"realign":"cold"},
		"tiers":{"on_saturated":"spill","on_failed":"next_tier","return":"cold","stable_seconds":120,"return_deadline_seconds":0},
		"balance":{"unit":"sessions","correction":"off","window_minutes":60,"min_volume":50}}`)
	field := func(raw []byte, path ...string) any {
		var doc any
		require.NoError(t, json.Unmarshal(raw, &doc))
		for _, p := range path {
			m, ok := doc.(map[string]any)
			require.True(t, ok, p)
			doc = m[p]
		}
		return doc
	}

	patched, err := PatchPolicyDocument(stored, SchedulerShare, true)
	require.NoError(t, err)
	require.Equal(t, "share", field(patched, "scheduler"))
	require.Equal(t, "session", field(patched, "affinity", "key"), "unchanged session keeping leaves affinity alone")
	for _, path := range [][]string{{"accounts"}, {"defaults"}, {"cache"}, {"tiers"}, {"balance"}, {"affinity", "wait"}, {"affinity", "ttl_seconds"}} {
		require.Equal(t, field(stored, path...), field(patched, path...), path)
	}

	off, err := PatchPolicyDocument(patched, SchedulerShare, false)
	require.NoError(t, err)
	require.Equal(t, "none", field(off, "affinity", "key"))
	require.Equal(t, field(stored, "affinity", "wait"), field(off, "affinity", "wait"))

	on, err := PatchPolicyDocument([]byte(`{"schema":1,"scheduler":"share","affinity":{"enabled":false,"key":"none"}}`), SchedulerSub2API, true)
	require.NoError(t, err)
	require.Equal(t, "auto", field(on, "affinity", "key"))
	require.Equal(t, true, field(on, "affinity", "enabled"))
	require.Equal(t, "sub2api", field(on, "scheduler"))

	p, err := DecodePolicy(off)
	require.NoError(t, err)
	require.False(t, p.SessionKeep())
}

// TestPatchPolicyDocumentWithNullAffinity: the decoders of 0.3.0–0.4.3 and
// this version accept "affinity": null (the defaults apply), so session
// keeping can be switched off on such a row.
func TestPatchPolicyDocumentWithNullAffinity(t *testing.T) {
	stored := []byte(`{"schema":1,"scheduler":"share","affinity":null}`)
	p, err := DecodePolicy(stored)
	require.NoError(t, err)
	require.True(t, p.SessionKeep())

	var off []byte
	require.NotPanics(t, func() { off, err = PatchPolicyDocument(stored, SchedulerShare, false) })
	require.NoError(t, err)
	p, err = DecodePolicy(off)
	require.NoError(t, err)
	require.False(t, p.SessionKeep())

	on, err := PatchPolicyDocument(off, SchedulerShare, true)
	require.NoError(t, err)
	p, err = DecodePolicy(on)
	require.NoError(t, err)
	require.True(t, p.SessionKeep())
}

func TestPatchPolicyDocumentReplacesUndecodableDocuments(t *testing.T) {
	for _, stored := range [][]byte{nil, []byte(`not json`), []byte(`{"schema":7}`), []byte(`[]`)} {
		patched, err := PatchPolicyDocument(stored, SchedulerShare, true)
		require.NoError(t, err)
		require.JSONEq(t, string(NewPolicyDocument(SchedulerShare, true)), string(patched))
	}
}

func TestPatchPolicyDocumentRejectsRetiredSchedulers(t *testing.T) {
	_, err := PatchPolicyDocument(nil, SchedulerShadow, true)
	require.Error(t, err)
	_, err = PatchPolicyDocument(nil, "controlled", true)
	require.Error(t, err)
}
