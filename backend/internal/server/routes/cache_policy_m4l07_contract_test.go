//go:build unit

package routes

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Byte-identical copy of 00-shared/cache_policies_contract/m4l07 (v1.0),
// except the omitted files below. SHA256SUMS digest as published in
// 08-impl-p0a/M4L07_CONTRACT_READY.md.
const (
	cacheM4L07Root    = "testdata/cache_policies_contract/m4l07"
	cacheM4L07SumsSHA = "483aed2fbee9ca6f0df2526309e441e04468559d73e98730d38cf9eab05eaa5a"
)

// Listed in SHA256SUMS but not copied: the README only documents the
// contract, and its version strings trip the repository docs consistency
// check. Like the m31 and m3m4 copies, the testdata carries no README.
var cacheM4L07Omitted = map[string]bool{"README.md": true}

// The T2.5 samples (_fixture.build_capabilities l07=true), replayed through
// TestCachePolicyM4ContractReplay together with the derived ones.
var cacheM4L07NewSamples = []string{
	"m4l07_list_clipped_static_cap.json",
	"m4l07_list_guard_tripped.json",
	"m4l07_list_t25.json",
	"m4l07_put_400_exceeds_static_cap.json",
	"m4l07_validate_health_lookback_raise.json",
	"m4l07_validate_hold_raise.json",
	"m4l07_validate_owner_cap_auto.json",
	"m4l07_validate_share_group_warning.json",
}

// Each negative shares its canonical scenario's request. Derived canonicals
// are keyed by their m3m4 source name, as in the replay.
var cacheM4L07InvalidSources = map[string]string{
	"inv_m4l07_clipped_missing.json":        "m4l07_list_clipped_static_cap.json",
	"inv_m4l07_default_hold.json":           "m4_list_default.json",
	"inv_m4l07_derived_drift.json":          "m4_list_masked_by_master.json",
	"inv_m4l07_field_order.json":            "m4l07_list_t25.json",
	"inv_m4l07_health_raise_as_enable.json": "m4l07_validate_health_lookback_raise.json",
	"inv_m4l07_marker_on_param.json":        "m4l07_list_t25.json",
	"inv_m4l07_owner_auto_as_disable.json":  "m4l07_validate_owner_cap_auto.json",
}

func cacheM4ListSample(name string) bool {
	return strings.HasPrefix(name, "m4_list_") || strings.HasPrefix(name, "m4_dormant_list_") || strings.HasPrefix(name, "m4l07_list_")
}

// cacheM4L07Manifest checks every copied file against SHA256SUMS and returns
// the digests plus the m3m4 sources that F derived for T2.5.
func cacheM4L07Manifest(t *testing.T) (map[string]string, map[string]bool) {
	t.Helper()
	sums, err := os.ReadFile(filepath.Join(cacheM4L07Root, "SHA256SUMS"))
	require.NoError(t, err)
	require.Equal(t, cacheM4L07SumsSHA, fmt.Sprintf("%x", sha256.Sum256(sums)))
	hashes := map[string]string{}
	omitted := 0
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		parts := strings.Fields(line)
		require.Len(t, parts, 2)
		if cacheM4L07Omitted[parts[1]] {
			_, err := os.Stat(filepath.Join(cacheM4L07Root, parts[1]))
			require.True(t, os.IsNotExist(err), parts[1])
			omitted++
			continue
		}
		raw, err := os.ReadFile(filepath.Join(cacheM4L07Root, parts[1]))
		require.NoError(t, err)
		require.Equal(t, parts[0], fmt.Sprintf("%x", sha256.Sum256(raw)), parts[1])
		hashes[parts[1]] = parts[0]
	}
	require.Equal(t, len(cacheM4L07Omitted), omitted)
	// No file outside the published set.
	require.NoError(t, filepath.WalkDir(cacheM4L07Root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(cacheM4L07Root, path)
		require.NoError(t, err)
		if rel != "SHA256SUMS" {
			require.Contains(t, hashes, filepath.ToSlash(rel))
		}
		return nil
	}))
	var scope struct {
		Derived   []string `json:"derived"`
		Unchanged []string `json:"unchanged"`
	}
	raw, err := os.ReadFile(filepath.Join(cacheM4L07Root, "SCOPE.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &scope))
	require.Len(t, scope.Derived, 9)
	derived := map[string]bool{}
	for _, name := range scope.Derived {
		require.Contains(t, hashes, "m4l07_"+name)
		derived[name] = true
	}
	samples := 0
	for name := range hashes {
		if strings.HasPrefix(name, "m4l07_") {
			samples++
		}
	}
	require.Equal(t, len(scope.Derived)+len(cacheM4L07NewSamples), samples)
	for _, name := range cacheM4L07NewSamples {
		require.Contains(t, hashes, name)
	}
	return hashes, derived
}

// cacheM4L07Overlay is F's m4l07 derivation rule (validate_policies_m4l07.py
// transform): the five keep_sticky_on_busy leaves follow rollout_percent in
// the routing schema and are filled into defaults and the stored spec.
func cacheM4L07Overlay(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	fields := []map[string]any{
		{"field": "keep_sticky_on_busy.max_consecutive_busy", "type": "int", "min": float64(1), "max": float64(5), "cap_source": "gateway.cache_routing.l07_max_consecutive_cap"},
		{"field": "keep_sticky_on_busy.max_hold_seconds", "type": "int", "min": float64(10), "max": float64(300), "cap_source": "gateway.cache_routing.l07_max_hold_seconds_cap"},
		{"field": "keep_sticky_on_busy.owner_busy_skip_per_min_cap", "type": "int", "min": float64(0), "max": float64(1000)},
		{"field": "keep_sticky_on_busy.group_keep_ratio_pct", "type": "int", "min": float64(1), "max": float64(20), "unit": "percent", "cap_source": "gateway.cache_routing.l07_group_keep_ratio_cap_pct"},
		{"field": "keep_sticky_on_busy.health_lookback_seconds", "type": "int", "min": float64(30), "max": float64(600)},
	}
	defaults := []float64{2, 60, 0, 5, 120}
	out := contractMap(t, response)
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if schema, ok := x["schema"].([]any); ok && x["key"] == "routing" && x["available"] == true {
				at := -1
				for i, field := range schema {
					if cacheM4L07As[map[string]any](t, field)["field"] == "keep_sticky_on_busy.rollout_percent" {
						at = i + 1
					}
				}
				require.Positive(t, at)
				next := append([]any{}, schema[:at]...)
				for _, field := range fields {
					next = append(next, contractMap(t, field))
				}
				x["schema"] = append(next, schema[at:]...)
				for _, holder := range []any{x["defaults"], cacheM4L07As[map[string]any](t, x["stored"])["spec"]} {
					busy := cacheM4L07As[map[string]any](t, cacheM4L07As[map[string]any](t, holder)["keep_sticky_on_busy"])
					for i, field := range fields {
						name := strings.TrimPrefix(cacheM4L07As[string](t, field["field"]), "keep_sticky_on_busy.")
						if _, exists := busy[name]; !exists {
							busy[name] = defaults[i]
						}
					}
				}
			}
			for _, item := range x {
				walk(item)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(out)
	return out
}

// The overlay used for the legacy negatives must reproduce F's nine
// authoritative derivations exactly, and commute with the M3.1 overlay.
func TestCachePolicyM4L07OverlayMatchesAuthority(t *testing.T) {
	_, derived := cacheM4L07Manifest(t)
	for name := range derived {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata/cache_policies_contract/m3m4", name))
			require.NoError(t, err)
			var source cacheContract
			require.NoError(t, json.Unmarshal(raw, &source))
			raw, err = os.ReadFile(filepath.Join(cacheM4L07Root, "m4l07_"+name))
			require.NoError(t, err)
			var want cacheContract
			require.NoError(t, json.Unmarshal(raw, &want))
			require.Equal(t, source.Request.Method, want.Request.Method)
			require.Equal(t, source.Request.Path, want.Request.Path)
			require.JSONEq(t, string(source.Request.Body), string(want.Request.Body))
			require.Equal(t, source.Status, want.Status)
			require.Equal(t, want.Response, cacheM4L07Overlay(t, source.Response))
			if !cacheM4ListSample(name) {
				return
			}
			raw, err = os.ReadFile(filepath.Join("testdata/cache_policies_contract/m31", "m31_"+name))
			require.NoError(t, err)
			var m31 cacheContract
			require.NoError(t, json.Unmarshal(raw, &m31))
			require.Equal(t, cacheM31M4ProjectLegacyList(t, want.Response), cacheM4L07Overlay(t, m31.Response), "M3.1 and T2.5 overlays commute")
		})
	}
}

// cacheM4L07As checks a decoded contract value's dynamic type before use.
func cacheM4L07As[T any](t *testing.T, value any) T {
	t.Helper()
	out, ok := value.(T)
	require.True(t, ok, "unexpected contract value type %T", value)
	return out
}
