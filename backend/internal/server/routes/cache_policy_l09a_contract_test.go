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

// Byte-identical copy of 00-shared/cache_policies_contract/l09a (v1.0),
// except the README, which only documents the contract (as for m4l07).
// SHA256SUMS digest as published in 07-design/kickoff/T3_KICKOFF_READY.md.
const (
	cacheL09ARoot    = "testdata/cache_policies_contract/l09a"
	cacheL09ASumsSHA = "f01a0730876f5662c035e8a5514deec39546ff56d935d8a1b0fd5bd03bbb3edd"
)

var cacheL09AOmitted = map[string]bool{"README.md": true}

// The T3.1a samples (_fixture.build_capabilities with l09a), replayed
// through TestCachePolicyM4ContractReplay with the derived ones.
var cacheL09ANewSamples = []string{
	"l09a_dormant_list_db_enabled.json",
	"l09a_dormant_validate_unsupported.json",
	"l09a_list_prev_host_on.json",
	"l09a_list_prev_host_static_off.json",
	"l09a_put_200.json",
	"l09a_validate_disable.json",
	"l09a_validate_enable.json",
	"l09a_validate_quality_unsupported.json",
}

// Each negative shares its canonical scenario's request.
var cacheL09AInvalidSources = map[string]string{
	"inv_l09a_blocker_order.json":        "l09a_dormant_list_db_enabled.json",
	"inv_l09a_enable_without_typed.json": "l09a_validate_enable.json",
	"inv_l09a_marked_when_cap.json":      "l09a_list_prev_host_on.json",
	"inv_l09a_missing_rollout.json":      "l09a_list_prev_host_on.json",
	"inv_l09a_quality_accepted.json":     "l09a_validate_quality_unsupported.json",
	"inv_l09a_static_off_on.json":        "l09a_list_prev_host_static_off.json",
}

func cacheL09AListSample(name string) bool {
	return strings.HasPrefix(name, "l09a_list_") || strings.HasPrefix(name, "l09a_dormant_list_")
}

// cacheL09AManifest checks every copied file against SHA256SUMS and returns
// the digests plus the m4l07 sample names that F derived for T3.1a (SCOPE).
func cacheL09AManifest(t *testing.T) (map[string]string, map[string]bool) {
	t.Helper()
	sums, err := os.ReadFile(filepath.Join(cacheL09ARoot, "SHA256SUMS"))
	require.NoError(t, err)
	require.Equal(t, cacheL09ASumsSHA, fmt.Sprintf("%x", sha256.Sum256(sums)))
	hashes := map[string]string{}
	omitted := 0
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		parts := strings.Fields(line)
		require.Len(t, parts, 2)
		if cacheL09AOmitted[parts[1]] {
			_, err := os.Stat(filepath.Join(cacheL09ARoot, parts[1]))
			require.True(t, os.IsNotExist(err), parts[1])
			omitted++
			continue
		}
		raw, err := os.ReadFile(filepath.Join(cacheL09ARoot, parts[1]))
		require.NoError(t, err)
		require.Equal(t, parts[0], fmt.Sprintf("%x", sha256.Sum256(raw)), parts[1])
		hashes[parts[1]] = parts[0]
	}
	require.Equal(t, len(cacheL09AOmitted), omitted)
	require.NoError(t, filepath.WalkDir(cacheL09ARoot, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(cacheL09ARoot, path)
		require.NoError(t, err)
		if rel != "SHA256SUMS" {
			require.Contains(t, hashes, filepath.ToSlash(rel))
		}
		return nil
	}))
	var scope struct {
		Derived   []string `json:"derived"`
		Unchanged []string `json:"unchanged_in_m4l07"`
	}
	raw, err := os.ReadFile(filepath.Join(cacheL09ARoot, "SCOPE.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &scope))
	require.Len(t, scope.Derived, 12)
	require.Len(t, scope.Unchanged, 5)
	derived := map[string]bool{}
	for _, name := range scope.Derived {
		require.Contains(t, hashes, "l09a_"+name)
		derived[name] = true
	}
	samples := 0
	for name := range hashes {
		if strings.HasPrefix(name, "l09a_") {
			samples++
		}
	}
	require.Equal(t, len(scope.Derived)+len(cacheL09ANewSamples), samples)
	for _, name := range cacheL09ANewSamples {
		require.Contains(t, hashes, name)
	}
	return hashes, derived
}

// cacheL09AOverlay is F's l09a derivation rule (validate_policies_l09a.py
// transform): affinity_rollout_percent follows affinity_mode in the routing
// schema and is filled into defaults and the stored spec; without the l09a
// capability affinity_mode carries not_implemented_in_build.
func cacheL09AOverlay(t *testing.T, response map[string]any, l09a bool) map[string]any {
	t.Helper()
	out := contractMap(t, response)
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if schema, ok := x["schema"].([]any); ok && x["key"] == "routing" && x["available"] == true {
				at := -1
				for i, field := range schema {
					if cacheM4As[map[string]any](t, field)["field"] == "affinity_mode" {
						at = i
					}
				}
				require.GreaterOrEqual(t, at, 0)
				if !l09a {
					cacheM4As[map[string]any](t, schema[at])["not_implemented_in_build"] = true
				}
				next := append([]any{}, schema[:at+1]...)
				next = append(next, map[string]any{"field": "affinity_rollout_percent", "type": "int", "min": float64(0), "max": float64(100), "unit": "percent"})
				x["schema"] = append(next, schema[at+1:]...)
				cacheM4As[map[string]any](t, x["defaults"])["affinity_rollout_percent"] = float64(0)
				spec := cacheM4As[map[string]any](t, cacheM4As[map[string]any](t, x["stored"])["spec"])
				if _, exists := spec["affinity_rollout_percent"]; !exists {
					spec["affinity_rollout_percent"] = float64(0)
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

// The overlay used for the legacy negatives and the other replays must
// reproduce F's twelve authoritative derivations exactly.
func TestCachePolicyL09AOverlayMatchesAuthority(t *testing.T) {
	_, derived := cacheL09AManifest(t)
	for name := range derived {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(cacheM4L07Root, name))
			require.NoError(t, err)
			var source cacheContract
			require.NoError(t, json.Unmarshal(raw, &source))
			raw, err = os.ReadFile(filepath.Join(cacheL09ARoot, "l09a_"+name))
			require.NoError(t, err)
			var want cacheContract
			require.NoError(t, json.Unmarshal(raw, &want))
			require.Equal(t, source.Request.Method, want.Request.Method)
			require.Equal(t, source.Request.Path, want.Request.Path)
			require.JSONEq(t, string(source.Request.Body), string(want.Request.Body))
			require.Equal(t, source.Status, want.Status)
			// Every derived sample declares a build without l09a.
			require.Equal(t, want.Response, cacheL09AOverlay(t, source.Response, false))
		})
	}
}

// cacheL09ADerivedFixture maps a replayed scenario of the M4 replay to F's
// T3.1a derivation, if any: m3m4 names via their m4l07 derivation, and the
// T2.5 samples directly.
func cacheL09ADerivedFixture(name string, m4l07Derived, l09aDerived map[string]bool) (string, bool) {
	switch {
	case m4l07Derived[name] && l09aDerived["m4l07_"+name]:
		return "l09a_m4l07_" + name, true
	case strings.HasPrefix(name, "m4l07_") && l09aDerived[name]:
		return "l09a_" + name, true
	}
	return "", false
}
