//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheNorthstarEmailLabelsUseContractPrivacyRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		private bool
	}{
		{"owner@example.test", true},
		{"OAuth <owner+tag@dept.example.test>", true},
		{"中文用户@domain.example", true},
		{"relay-a-01", false},
		{"团队账号", false},
		{"owner@localhost", false},
		{"owner@ example.test", false},
		{"owner@\u00a0example.test", false},
		{"owner@\u001cexample.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.name
			if tc.private {
				want = "openai#102"
			}
			require.Equal(t, want, cacheBreakdownLabel(tc.name, "account", "102", map[string]any{"platform": "openai"}))
			if tc.private {
				want = "group#7"
			}
			require.Equal(t, want, cacheBreakdownLabel(tc.name, "group", "7", map[string]any{"platform": "openai"}))
		})
	}
}

func TestCacheNorthstarMaskedAccountMatchesAggregatedPlatform(t *testing.T) {
	rows := []CacheNorthstarRow{
		{Platform: "openai", AccountID: 102, AccountLabel: "owner@example.test", Model: "gpt-5.4", CacheNorthstarTotals: CacheNorthstarTotals{Requests: 12, PromptTokens: 1200}},
		{Platform: "anthropic", AccountID: 102, AccountLabel: "owner@example.test", Model: "claude", CacheNorthstarTotals: CacheNorthstarTotals{Requests: 12, PromptTokens: 1200}},
	}
	meta := CachePanelMeta{AsOf: time.Now().UTC()}
	q := CachePanelQuery{Dimension: "account", Platform: "all", CostView: "list", Sort: "opportunity", Limit: 10}
	got := buildCacheBreakdown(meta, q, CacheNorthstarPolicy{}, rows)
	require.Len(t, got.Rows, 1)
	require.Equal(t, "102", got.Rows[0].Key)
	require.Equal(t, "all", got.Rows[0].Meta["platform"])
	require.Equal(t, "all#102", got.Rows[0].Label)
	require.EqualValues(t, 24, got.Rows[0].Requests)
	require.Equal(t, "owner@example.test", rows[0].AccountLabel, "stored source labels stay untouched")
}
