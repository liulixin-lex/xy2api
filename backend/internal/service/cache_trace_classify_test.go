package service

import (
	"encoding/csv"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func t3ClassifierFixture() cacheTraceClassificationInput {
	return cacheTraceClassificationInput{
		Transport: "http", UsageObserved: true, UsagePromptPresent: true, UsageFields: 1,
		PromptTokens: 12000, ReadTokens: 6000, ChainSampled: true, BytesOut: 12000,
		ForkRef: "prev1", ForkKind: "diverge", ForkPrecision: "exact", ForkByte: 10000, ForkByteHi: -1,
		PreviousPrompt: 10000, TailKind: "user_msg", ModelGen: "pre56", ReasoningContext: "absent",
		ReasoningContextEffective: "unknown", OldReasoningN: 1, FirstOldReasoningByte: 6000,
	}
}

func TestT3ClassifierLossTable(t *testing.T) {
	for _, tc := range []struct {
		name, want, reason string
		change             func(*cacheTraceClassificationInput)
	}{
		{"old-reasoning-default", "provider_reasoning_drop_suspect", "", func(*cacheTraceClassificationInput) {}},
		{"tool-tail", "under_read", "", func(d *cacheTraceClassificationInput) { d.TailKind = "tool_output" }},
		{"other-tail", "under_read", "", func(d *cacheTraceClassificationInput) { d.TailKind = "other" }},
		{"only-current-reasoning", "under_read", "", func(d *cacheTraceClassificationInput) { d.OldReasoningN = 0 }},
		{"unknown-old-position", "under_read", "", func(d *cacheTraceClassificationInput) { d.FirstOldReasoningByte = -1 }},
		{"prefix-priority-over-reasoning", "prefix_changed", "", func(d *cacheTraceClassificationInput) { d.ForkByte = 8000 }},
		{"plugin-before-all-exclusions", "n/a_plugin", "", func(d *cacheTraceClassificationInput) {
			d.Transport, d.CoverageReason = "plugin", "budget_full"
			d.UsageFields, d.ChainSampled, d.PromptTokens, d.ForkPrecision = 0, false, 0, "range"
		}},
		{"observed-missing-read-before-sampling", "n/a_usage", "", func(d *cacheTraceClassificationInput) {
			d.UsageFields, d.ChainSampled, d.CoverageReason = 240, false, "chain_percent"
		}},
		{"observed-missing-P", "n/a_usage", "", func(d *cacheTraceClassificationInput) { d.UsagePromptPresent = false }},
		{"observed-missing-zero-P", "n/a_usage", "", func(d *cacheTraceClassificationInput) { d.UsagePromptPresent, d.PromptTokens = false, 0 }},
		{"explicit-zero-P", "below_min", "", func(d *cacheTraceClassificationInput) { d.PromptTokens = 0 }},
		{"explicit-zero-read", "under_read", "", func(d *cacheTraceClassificationInput) { d.ReadTokens = 0 }},
		{"chain-not-sampled", "n/a_sampled_out", "chain_percent", func(d *cacheTraceClassificationInput) { d.ChainSampled = false }},
		{"history-not-sampled", "n/a_sampled_out", "hist_unavailable", func(d *cacheTraceClassificationInput) { d.HistoryUnsampled = true }},
		{"previous-P-unavailable", "n/a_sampled_out", "hist_unavailable", func(d *cacheTraceClassificationInput) { d.PreviousPrompt = -1 }},
		{"sampling-before-range-and-minimum", "n/a_sampled_out", "chain_percent", func(d *cacheTraceClassificationInput) {
			d.ChainSampled, d.ForkPrecision, d.PromptTokens, d.ForkByte = false, "range", 0, 8000
		}},
		{"unobserved-rejected-usage-not-missing", "n/a_sampled_out", "budget_full", func(d *cacheTraceClassificationInput) {
			d.UsageObserved, d.UsagePromptPresent, d.UsageFields, d.ChainSampled = false, false, 0, false
			d.CoverageReason = "budget_full"
		}},
		{"unobserved-fallback", "n/a_sampled_out", "not_ready", func(d *cacheTraceClassificationInput) { d.UsageObserved = false }},
		{"unobserved-without-chain-fallback", "n/a_sampled_out", "not_ready", func(d *cacheTraceClassificationInput) { d.UsageObserved, d.ChainSampled = false, false }},
		{"below-min-before-cold", "below_min", "", func(d *cacheTraceClassificationInput) { d.PromptTokens, d.ForkRef = 1023, "none" }},
		{"at-min-cold", "cold", "", func(d *cacheTraceClassificationInput) { d.PromptTokens, d.ForkRef = 1024, "none" }},
		{"above-min-cold", "cold", "", func(d *cacheTraceClassificationInput) { d.PromptTokens, d.ForkRef = 1025, "none" }},
		{"cold-without-previous-P", "cold", "", func(d *cacheTraceClassificationInput) { d.ForkRef, d.PreviousPrompt = "none", -1 }},
		{"other-conversation-before-full", "other_conv", "", func(d *cacheTraceClassificationInput) { d.ForkRef, d.ReadTokens = "other_conv", 12000 }},
		{"prevk-matches-same-way", "provider_reasoning_drop_suspect", "", func(d *cacheTraceClassificationInput) { d.ForkRef = "prevk" }},
		{"append-no-fork-with-stale-position", "under_read", "", func(d *cacheTraceClassificationInput) {
			d.ForkPrecision, d.ForkKind, d.ForkByte, d.ReadTokens = "none", "append", 100, 0
		}},
		{"proven-cur-prefix-is-no-fork", "under_read", "", func(d *cacheTraceClassificationInput) {
			d.PromptTokens, d.BytesOut, d.ForkByte, d.ReadTokens, d.ForkKind = 6000, 6000, 6000, 0, "cur_prefix"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := t3ClassifierFixture()
			tc.change(&in)
			before := in
			got := classifyCacheTrace(in, 1024)
			if got.LossClass != tc.want || got.NAReason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", got.LossClass, got.NAReason, tc.want, tc.reason)
			}
			if in != before || got != classifyCacheTrace(in, 1024) {
				t.Fatal("classification mutated input or was nondeterministic")
			}
		})
	}
	for _, reason := range []string{"chain_percent", "hist_unavailable", "budget_full", "expired", "not_ready"} {
		t.Run("coverage/"+reason, func(t *testing.T) {
			in := t3ClassifierFixture()
			in.CoverageReason, in.UsageObserved, in.UsageFields, in.UsagePromptPresent = reason, false, 0, false
			got := classifyCacheTrace(in, 1024)
			if got.LossClass != "n/a_sampled_out" || got.NAReason != reason {
				t.Fatalf("unobserved rejection became missing usage: %+v", got)
			}
		})
	}
	for _, bit := range []uint8{1, 2, 4, 8, 15} {
		in := t3ClassifierFixture()
		in.UsageFields, in.ReadTokens = bit, 0
		if got := classifyCacheTrace(in, 1024); got.LossClass != "under_read" {
			t.Fatalf("explicit zero read bit %d: %+v", bit, got)
		}
	}
}

func TestT3ClassifierReasoningModels(t *testing.T) {
	models := []string{"pre56", "gpt56", "gpt6x", "other_openai", "non_openai", "unknown"}
	for _, model := range models {
		for _, context := range []string{"absent", "auto", "current_turn", "all_turns", "other", "unknown"} {
			for _, response := range []string{"unknown", "current_turn", "all_turns"} {
				t.Run(model+"/"+context+"/"+response, func(t *testing.T) {
					in := t3ClassifierFixture()
					in.ModelGen, in.ReasoningContext, in.ReasoningContextEffective = model, context, response
					suspect := response == "current_turn" || response == "unknown" && (context == "current_turn" ||
						(context == "absent" || context == "auto") && model == "pre56")
					want := "under_read"
					if suspect {
						want = "provider_reasoning_drop_suspect"
					}
					got := classifyCacheTrace(in, 1024)
					if got.LossClass != want || !got.ReasoningBoundaryHit {
						t.Fatalf("got %+v, want %s with reasoning boundary retained", got, want)
					}
				})
			}
		}
	}
}

func TestT3ClassifierRangeInterpretations(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want                   string
		p, previous, lo, hi, read, oldByte int
	}{
		{"diverge-both-changed", "diverge", "prefix_changed", 12000, 10000, 7000, 8000, 0, -1},
		{"diverge-crosses-boundary", "diverge", "n/a_truncated", 12000, 10000, 8000, 10000, 0, -1},
		{"diverge-both-under", "diverge", "under_read", 12000, 10000, 10000, 11000, 0, -1},
		{"diverge-full-independent-of-range", "diverge", "full", 12000, 10000, 7000, 11000, 9744, -1},
		{"tail-hi-is-no-fork-under", "tail_unproven", "n/a_truncated", 6000, 10000, 5000, 6000, 1000, -1},
		{"tail-hi-is-no-fork-reasoning", "tail_unproven", "n/a_truncated", 6000, 10000, 5000, 6000, 1000, 1000},
		{"tail-both-full-use-current-P", "tail_unproven", "full", 6000, 10000, 5000, 6000, 5744, -1},
		{"tail-both-under", "tail_unproven", "under_read", 12000, 10000, 11000, 12000, 0, -1},
		{"tail-both-reasoning", "tail_unproven", "provider_reasoning_drop_suspect", 12000, 10000, 11000, 12000, 6000, 6000},
		{"tail-below-min-both-ends", "tail_unproven", "below_min", 1023, 10000, 500, 1023, 0, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := t3ClassifierFixture()
			in.ForkPrecision, in.ForkKind = "range", tc.kind
			in.PromptTokens, in.BytesOut, in.PreviousPrompt = tc.p, tc.p, tc.previous
			in.ForkByte, in.ForkByteHi, in.ReadTokens, in.FirstOldReasoningByte = tc.lo, tc.hi, tc.read, tc.oldByte
			got := classifyCacheTrace(in, 1024)
			if got.LossClass != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
		})
	}
	for _, precision := range []string{"exact", "none"} {
		in := t3ClassifierFixture()
		in.ForkPrecision, in.ForkByte, in.ForkByteHi = precision, 8000, 10000
		if got := classifyCacheTrace(in, 1024); got.LossClass == "n/a_truncated" {
			t.Fatalf("%s must never be n/a_truncated", precision)
		}
	}
}

func TestT3ClassifierExactToleranceAndMinimum(t *testing.T) {
	for _, pair := range [][2]int{{12000, 10000}, {6000, 10000}, {1000000, 10000}} {
		match := pair[0]
		if pair[1] < match {
			match = pair[1]
		}
		for _, delta := range []int{-1, 0, 1} {
			in := t3ClassifierFixture()
			in.PromptTokens, in.BytesOut, in.PreviousPrompt = pair[0], pair[0], pair[1]
			in.ForkPrecision, in.ForkKind, in.OldReasoningN = "none", "append", 0
			in.ReadTokens = match - 256 + delta
			want := "full"
			if delta < 0 {
				want = "under_read"
			}
			if got := classifyCacheTrace(in, 1024); got.LossClass != want {
				t.Fatalf("P=%d prev=%d delta=%d: got %s want %s", pair[0], pair[1], delta, got.LossClass, want)
			}
		}
	}
	for _, minimum := range []int{-1, 0, 1024, 2048} {
		boundary := minimum
		if boundary <= 0 {
			boundary = 1024
		}
		for _, delta := range []int{-1, 0, 1} {
			in := t3ClassifierFixture()
			in.ForkRef, in.PromptTokens = "none", boundary+delta
			want := "cold"
			if delta < 0 {
				want = "below_min"
			}
			if got := classifyCacheTrace(in, minimum); got.LossClass != want {
				t.Fatalf("minimum=%d delta=%d: got %s want %s", minimum, delta, got.LossClass, want)
			}
		}
	}
}

func TestT3ClassifierEstimatedToleranceBoundaries(t *testing.T) {
	// Integer tokens permit exactly representable boundaries on both the
	// fixed-256 and 3% branches; each checks the boundary and both +/-1 sides.
	for _, tc := range []struct{ estimate, tolerance int }{{6000, 256}, {400000, 12000}} {
		for _, sign := range []int{-1, 1} {
			for _, delta := range []int{-1, 0, 1} {
				in := t3ClassifierFixture()
				in.PromptTokens, in.BytesOut, in.PreviousPrompt = 1000000, 1000000, 900000
				in.ForkPrecision, in.ForkKind = "none", "append"
				in.FirstOldReasoningByte, in.ReadTokens = tc.estimate, tc.estimate+sign*tc.tolerance+delta
				wantHit := sign*delta <= 0
				got := classifyCacheTrace(in, 1024)
				if got.ReasoningBoundaryHit != wantHit || (got.LossClass == "provider_reasoning_drop_suspect") != wantHit {
					t.Fatalf("reasoning estimate=%d sign=%d delta=%d: %+v", tc.estimate, sign, delta, got)
				}
			}
		}
	}
	for _, tc := range []struct{ fork, tolerance int }{{6000, 256}, {10000, 300}} {
		for _, delta := range []int{-1, 0, 1} {
			in := t3ClassifierFixture()
			in.OldReasoningN, in.ForkByte, in.ReadTokens = 0, tc.fork, 0
			in.PreviousPrompt = tc.fork + tc.tolerance + delta
			got := classifyCacheTrace(in, 1024)
			if (got.LossClass == "prefix_changed") != (delta > 0) {
				t.Fatalf("prefix estimate=%d delta=%d: %+v", tc.fork, delta, got)
			}
			in.ReadTokens, in.PreviousPrompt = tc.fork-tc.tolerance+delta, 12000
			got = classifyCacheTrace(in, 1024)
			if got.ReadShortOfFork != (delta < 0) {
				t.Fatalf("read-short estimate=%d delta=%d: %+v", tc.fork, delta, got)
			}
		}
	}
	// A non-integer 3% threshold must not be rounded to whole tokens first.
	for _, fork := range []int{9708, 9709} {
		in := t3ClassifierFixture()
		in.OldReasoningN, in.ReadTokens, in.ForkByte = 0, 0, fork
		if got := classifyCacheTrace(in, 1024); (got.LossClass == "prefix_changed") != (fork == 9708) {
			t.Fatalf("fractional tolerance fork=%d: %+v", fork, got)
		}
	}
}

func TestT3ClassifierUnavailableDensityAndFlags(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*cacheTraceClassificationInput)
		milli  int
	}{
		{"positive-density", func(*cacheTraceClassificationInput) {}, 1000},
		{"zero-P-is-known-density", func(d *cacheTraceClassificationInput) { d.PromptTokens = 0 }, 0},
		{"rounded-milli", func(d *cacheTraceClassificationInput) { d.PromptTokens, d.BytesOut = 4000, 12000 }, 333},
		{"no-logical-bytes", func(d *cacheTraceClassificationInput) { d.BytesOut = 0 }, -1},
		{"missing-logical-bytes", func(d *cacheTraceClassificationInput) { d.BytesOut = -1 }, -1},
		{"missing-P", func(d *cacheTraceClassificationInput) { d.UsagePromptPresent = false }, -1},
		{"unobserved-usage", func(d *cacheTraceClassificationInput) { d.UsageObserved = false }, -1},
		{"chain-not-sampled", func(d *cacheTraceClassificationInput) { d.ChainSampled = false }, -1},
		{"plugin", func(d *cacheTraceClassificationInput) { d.Transport = "plugin" }, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := t3ClassifierFixture()
			tc.change(&in)
			got := classifyCacheTrace(in, 1024)
			if got.TokensPerByteMilli != tc.milli {
				t.Fatalf("got %+v want milli=%d", got, tc.milli)
			}
			if tc.milli == -1 && (got.ReasoningBoundaryHit || got.ReadShortOfFork) {
				t.Fatalf("unavailable estimates set flags: %+v", got)
			}
		})
	}
	in := t3ClassifierFixture()
	in.UsageFields = 240
	got := classifyCacheTrace(in, 1024)
	if got.TokensPerByteMilli != 1000 || got.ReasoningBoundaryHit || got.ReadShortOfFork {
		t.Fatalf("missing read preserves known density but cannot set flags: %+v", got)
	}
}

func TestT3ClassifierNumericReplay(t *testing.T) {
	var file *os.File
	var err error
	for _, path := range []string{"testdata/cache_trace_numeric_replay.csv", "../../data/cache_trace_numeric_replay.csv"} {
		file, err = os.Open(path)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 121 {
		t.Fatalf("got %d data rows, want 120", len(rows)-1)
	}
	counts := map[string]int{}
	shortened, mapped, aligned := 0, 0, 0
	for index, row := range rows[1:] {
		in := t3ClassifierFixture()
		for _, field := range []struct {
			text   string
			target *int
		}{{row[0], &in.PromptTokens}, {row[1], &in.ReadTokens}} {
			*field.target, err = strconv.Atoi(field.text)
			if err != nil {
				t.Fatal(err)
			}
		}
		in.BytesOut, in.ForkPrecision, in.ForkKind, in.OldReasoningN = in.PromptTokens, "none", "append", 0
		if row[2] == "" {
			in.ForkRef, in.PreviousPrompt = "none", -1
		} else {
			in.PreviousPrompt, err = strconv.Atoi(row[2])
			if err != nil {
				t.Fatal(err)
			}
		}
		got := classifyCacheTrace(in, 1024)
		if got.LossClass != row[4] {
			t.Fatalf("anonymous row %d: got %s want %s", index+1, got.LossClass, row[4])
		}
		counts[got.LossClass]++
		eligible := row[2] == "" || float64(in.PromptTokens) >= .9*float64(in.PreviousPrompt)
		if strconv.FormatBool(eligible) != row[5] {
			t.Fatalf("anonymous row %d: shortened eligibility changed", index+1)
		}
		if !eligible {
			shortened++
		}
		want := ""
		switch row[3] {
		case "S1_healthy":
			want = "full"
		case "S2_plateau", "S3_lagged", "S4_partial_between", "S5", "S0_deep_partial_unmatched", "S0_zero_without_rebound":
			want = "under_read"
		}
		if eligible && want != "" {
			mapped++
			if got.LossClass == want {
				aligned++
			}
		}
	}
	wantCounts := map[string]int{"full": 24, "under_read": 92, "cold": 4}
	if !reflect.DeepEqual(counts, wantCounts) || shortened != 8 || mapped != 86 || aligned != 86 {
		t.Fatalf("counts=%v shortened=%d mapped=%d aligned=%d", counts, shortened, mapped, aligned)
	}
}

func TestT3ClassifierUsesUnroundedDensity(t *testing.T) {
	in := t3ClassifierFixture()
	in.PromptTokens, in.BytesOut, in.PreviousPrompt = 4000, 12000, 3000
	in.ReadTokens, in.ForkByte, in.OldReasoningN = 0, 8232, 0
	got := classifyCacheTrace(in, 1024)
	if got.TokensPerByteMilli != 333 || got.LossClass != "under_read" {
		t.Fatalf("estimated fork 2744 must be exactly at P_prev-256; rounded milli would invent a fork: %+v", got)
	}
}

func TestT3ClassifierRangeIndependentPriority(t *testing.T) {
	for _, reference := range []string{"none", "other_conv"} {
		in := t3ClassifierFixture()
		in.ForkPrecision, in.ForkKind, in.ForkByte, in.ForkByteHi = "range", "diverge", 1000, 11000
		in.ForkRef = reference
		want := "other_conv"
		if reference == "none" {
			want = "cold"
		}
		if got := classifyCacheTrace(in, 1024); got.LossClass != want {
			t.Fatalf("range %s: got %s want %s", reference, got.LossClass, want)
		}
	}
}

var t3ClassificationSink cacheTraceClassification

func TestT3ClassifierAllocations(t *testing.T) {
	for _, scenario := range []string{"admitted", "range", "unobserved-rejected", "plugin"} {
		t.Run(scenario, func(t *testing.T) {
			in := t3ClassifierFixture()
			switch scenario {
			case "range":
				in.ForkPrecision, in.ForkKind, in.ForkByte, in.ForkByteHi = "range", "tail_unproven", 8000, 12000
			case "unobserved-rejected":
				in.UsageObserved, in.ChainSampled, in.CoverageReason = false, false, "budget_full"
			case "plugin":
				in.Transport = "plugin"
			}
			if allocations := testing.AllocsPerRun(1000, func() { t3ClassificationSink = classifyCacheTrace(in, 1024) }); allocations != 0 {
				t.Fatalf("classification allocations=%v, want 0", allocations)
			}
		})
	}
}
