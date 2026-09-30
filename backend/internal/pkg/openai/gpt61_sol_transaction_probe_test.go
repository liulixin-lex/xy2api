//go:build unit

package openai

import "testing"

// Uses only the pre-existing DefaultModelIDs API so the identical probe also
// compiles on e176. A baseline failure is missing model behavior, not a missing
// new symbol or test helper. Keep this input stable for cumulative rollback.
func TestGPT61SolTransactionCatalog(t *testing.T) {
	counts := make(map[string]int)
	for _, id := range DefaultModelIDs() {
		counts[id]++
	}
	for _, old := range []string{"gpt-6-sol", "gpt-6-luna", "gpt-6-astra"} {
		if counts[old] != 1 {
			t.Fatalf("legacy model %q count=%d, want exactly one", old, counts[old])
		}
	}
	t.Logf("GPT61_CATALOG count=%d legacy_catalog_preserved=true", counts["gpt-6.1-sol"])
	if counts["gpt-6.1-sol"] != 1 {
		t.Fatalf("gpt-6.1-sol catalog count=%d, want exactly one", counts["gpt-6.1-sol"])
	}
}
