//go:build unit

package service

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestEngineRuleBReferenceAssembly(t *testing.T) {
	file, err := os.Open("../domain/schedquality/testdata/rule_b_vectors.json.gz")
	require.NoError(t, err)
	defer file.Close()
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer reader.Close()
	var fixture struct {
		Samples struct {
			AccountID, GroupID                         []int64
			Model, SessionKey                          []string
			AvailableDeltaNS, StartedBeforeAvailableNS []int64
			TTFOMs, CensoredMs                         []float64
			Stream, SwitchedAccount                    []bool
			Outcome                                    []q.Outcome
		}
		Cases []struct {
			Name                   string
			Params                 q.RuleBParams
			Scope                  q.RuleBScope
			Now, Cut, LimitSeconds float64
			Samples, GroupSamples  []int
			Peers                  []struct {
				Scope    q.RuleBScope
				Identity int
				Epoch    float64
				Samples  []int
			}
			Expected q.RuleBResult
		}
	}
	require.NoError(t, json.NewDecoder(reader).Decode(&fixture))
	columns := fixture.Samples
	all := make([]q.AttemptSample, len(columns.AccountID))
	var ns int64
	for i := range all {
		ns += columns.AvailableDeltaNS[i]
		ms, censor := int(columns.TTFOMs[i]), int(columns.CensoredMs[i])
		all[i] = q.AttemptSample{AttemptID: fmt.Sprint(i), AccountID: columns.AccountID[i], GroupID: columns.GroupID[i], Model: columns.Model[i], SessionKey: columns.SessionKey[i], StartedAt: time.Unix(0, ns-columns.StartedBeforeAvailableNS[i]), FinishedAt: time.Unix(0, ns).Add(-time.Millisecond), InsertedAt: time.Unix(0, ns), TTFOMs: &ms, CensoredMs: &censor, Stream: columns.Stream[i], Switched: columns.SwitchedAccount[i], Outcome: columns.Outcome[i]}
	}
	stamp := func(seconds float64) time.Time {
		if seconds < -1e15 {
			return time.Time{}
		}
		return time.Unix(0, int64(math.Round(seconds*1e9)))
	}
	for _, spec := range []struct {
		name, scenario string
		review         bool
	}{{"default/32", "rear-peers", false}, {"default/39", "epoch-cut", false}, {"default/32", "expiry-review", true}} {
		for _, tc := range fixture.Cases {
			if tc.Name != spec.name {
				continue
			}
			t.Run(spec.scenario, func(t *testing.T) {
				f := newQualityFixture(t)
				f.now = stamp(tc.Now)
				f.store.Rule = SchedTTFTRuleRecord{Params: tc.Params}
				refs := make(map[int]bool)
				for _, ids := range [][]int{tc.Samples, tc.GroupSamples} {
					for _, i := range ids {
						refs[i] = true
					}
				}
				key := q.LineKey{AccountID: tc.Scope.AccountID, GroupID: tc.Scope.GroupID, Model: tc.Scope.Model, Line: q.DimensionTTFO}
				state := q.LineState{LineKey: key, Epoch: stamp(tc.Cut)}
				kind := q.EventRegular
				if spec.review {
					kind = q.EventReview
					state.Entry = stamp(tc.Cut)
					state.Epoch = f.now
					state.Identity = q.IdentityRear
				}
				for _, peer := range tc.Peers {
					for _, i := range peer.Samples {
						refs[i] = true
					}
				}
				f.e.window.samples = make(map[SchedAccountModel][]q.AttemptSample)
				for i := range refs {
					a := all[i]
					pair := SchedAccountModel{a.AccountID, a.Model}
					f.e.window.samples[pair] = append(f.e.window.samples[pair], a)
				}
				f.e.window.indexGroups()
				d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
				require.NoError(t, err)
				in := ttftInput(d, key, state.Cut(kind), f.now, tc.LimitSeconds)
				got, err := q.EvaluateRuleB(in, tc.Params)
				require.NoError(t, err)
				require.InDelta(t, tc.Expected.Counts.N, got.Counts.N, 1e-8)
				require.InDelta(t, tc.Expected.Counts.K, got.Counts.K, 1e-8)
				require.InDelta(t, tc.Expected.Counts.X, got.Counts.X, 1e-8)
				require.Equal(t, tc.Expected.Counts.S, got.Counts.S)
				require.Equal(t, tc.Expected.Counts.D, got.Counts.D)
				require.InDelta(t, tc.Expected.Prior, got.Prior, 1e-8)
				want := q.RuleBUnknown
				if tc.Expected.Absolute && tc.Expected.Counts.D >= 2 {
					want = q.RuleBBad
				} else if tc.Expected.GoodBase {
					want = q.RuleBGood
				}
				require.Equal(t, want, got.State)
			})
		}
	}
}

func TestEngineAbsoluteVerdictsIgnoreTransitionOrder(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.e.window.merge(f.ledger.Rows, f.now)
	at := f.now.Add(-10 * time.Second)
	run := func(reverse bool) map[q.LineKey]q.LineState {
		d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
		require.NoError(t, err)
		rows, _, _, decisions, err := f.e.replay(context.Background(), d, nil, nil, at.Add(-time.Second), at, func(keys []q.LineKey) {
			if reverse {
				for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		})
		require.NoError(t, err)
		result := make(map[q.LineKey]q.LineState)
		for _, r := range rows {
			result[r.LineKey] = r
		}
		slow := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}
		fast := slow
		fast.AccountID = 2
		require.Equal(t, q.IdentityRear, result[slow].Identity)
		require.Equal(t, q.IdentityPass, result[fast].Identity)
		require.Len(t, decisions, 1)
		require.Equal(t, q.ActionEnter, decisions[0].Action)
		// A peer moving to rear no longer vetoes an otherwise good account.
		again, basis := f.e.assessLine(d, q.LineState{LineKey: fast}, q.EventRegular, at, nil)
		require.Equal(t, q.VerdictPass, again)
		require.Nil(t, basis.TTFT.U)
		require.Nil(t, basis.TTFT.PRel)
		return result
	}
	require.Equal(t, run(false), run(true))
}

func TestEngineSyntheticCycleTiming(t *testing.T) {
	for _, sampleCount := range []int{74, 2000} {
		t.Run(fmt.Sprint(sampleCount), func(t *testing.T) {
			f := newQualityFixture(t)
			at := f.now.Add(-10 * time.Second)
			f.store.Members = make(map[int64][]int64)
			f.store.Policies = make(map[int64]SchedPolicyRecord)
			f.e.window.samples = make(map[SchedAccountModel][]q.AttemptSample)
			for account := int64(1); account <= 300; account++ {
				group := (account-1)/50 + 1
				f.store.Members[group] = append(f.store.Members[group], account)
				p := q.DefaultPolicy()
				p.Enabled = true
				f.store.Policies[group] = SchedPolicyRecord{GroupID: group, Policy: p}
				pair := SchedAccountModel{account, "synthetic"}
				samples := make([]q.AttemptSample, sampleCount)
				for i := range samples {
					finished := at.Add(-time.Duration(float64(i) * float64(4*time.Hour) / float64(sampleCount)))
					ms := 1000
					samples[i] = q.AttemptSample{AttemptID: fmt.Sprintf("%d-%d", account, i), AccountID: account, GroupID: group, Model: pair.Model, SessionKey: fmt.Sprintf("s-%d", i%5), StartedAt: finished.Add(-time.Second), FinishedAt: finished, InsertedAt: finished, Stream: true, Outcome: q.OutcomeOK, TTFOMs: &ms}
				}
				f.e.window.samples[pair] = samples
			}
			started := time.Now()
			f.e.window.indexGroups()
			d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
			require.NoError(t, err)
			prepared := time.Since(started)
			rows, _, _, _, err := f.e.replay(context.Background(), d, nil, nil, at.Add(-q.RegularInterval), at)
			require.NoError(t, err)
			require.Len(t, rows, 900)
			replayed := time.Since(started) - prepared
			f.e.publish(d, rows, nil, nil, f.now)
			t.Logf("300 TTFO keys; 6 groups x 50 members; %d samples/account (%d total); prepare=%s replay=%s snapshot=%s total=%s", sampleCount, sampleCount*300, prepared, replayed, time.Since(started)-prepared-replayed, time.Since(started))
		})
	}
}

func TestEngineDisplayPreservesReplayHorizon(t *testing.T) {
	f := newQualityFixture(t)
	bucket := f.now.UTC().Truncate(q.RegularInterval)
	f.now = bucket.Add(3 * time.Second)
	f.badTTFT()
	// Arrival after the old horizon rounds to a grid still before the bucket.
	event := bucket.Add(-6 * time.Second)
	for i := range f.ledger.Rows {
		f.ledger.Rows[i].InsertedAt = event
	}
	key := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}
	f.store.Rows = []q.LineState{{LineKey: key, Trial: true, Epoch: bucket.Add(-time.Minute), EvaluatedAt: bucket.Add(-20 * time.Second), TrialUntil: bucket.Add(time.Hour), Admissions: 1}}
	f.cycle()
	require.Equal(t, bucket.Add(-7*time.Second), f.e.processedAt)
	require.True(t, f.view().TTFO.Trial)
	require.NoError(t, f.e.SetOverride(context.Background(), SchedOverride{AccountID: 1, GroupID: 10, Mode: q.OverrideExempt}))
	require.NoError(t, f.e.ClearOverride(context.Background(), 1, 10))
	f.now = bucket.Add(13 * time.Second)
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().TTFO.Identity)
	require.Equal(t, bucket.Add(-5*time.Second), f.line(q.DimensionTTFO).Entry)
}

func TestEngineRejudgePreservesReplayHorizon(t *testing.T) {
	for _, line := range []q.Dimension{q.DimensionTTFO, q.DimensionError, q.DimensionCache} {
		t.Run(string(line), func(t *testing.T) {
			f := newQualityFixture(t)
			bucket := f.now.UTC().Truncate(q.RegularInterval)
			f.now = bucket.Add(3 * time.Second)
			f.badTTFT()
			f.cycle()
			policy := f.store.Policies[10].Policy
			switch line {
			case q.DimensionTTFO:
				policy.TTFTSeconds = 2
			case q.DimensionError:
				policy.MaxErrorRate = .1
			case q.DimensionCache:
				policy.MinCacheRatio = .7
			}
			_, err := f.e.UpdatePolicy(context.Background(), 10, 0, policy)
			require.NoError(t, err)
			require.Equal(t, bucket.Add(-7*time.Second), f.e.processedAt)
		})
	}
}

func TestTTFTBasisIncludesOnlyProvenSwitchedTimeout(t *testing.T) {
	f := newQualityFixture(t)
	ctx := context.Background()
	rows := engineSamples(4, f.now, true)
	for i := range rows {
		a := &rows[i]
		a.SessionKey = fmt.Sprintf("session-%d", i)
		a.CaptureVersion = 3
		a.Warm = i == 1 || i == 2
		a.FullTierFirstOutputTimeout = i == 1 || i == 3
		if i == 0 {
			a.CaptureVersion = 2
		}
		waited := 1500
		a.Outcome, a.TTFOMs, a.CensoredMs = q.OutcomeTimeout, nil, &waited
		pred := *a
		pred.AttemptID = "previous-" + a.AttemptID
		pred.AccountID++
		pred.StartedAt, pred.FinishedAt = a.StartedAt.Add(-time.Minute), a.StartedAt.Add(-time.Second)
		f.ledger.Rows = append(f.ledger.Rows, pred)
	}
	f.ledger.Rows = append(f.ledger.Rows, rows...)
	require.NoError(t, f.e.loadEvidence(ctx, f.now))
	d, err := f.e.inputs(ctx, f.now, f.e.window.samples)
	require.NoError(t, err)
	state := q.LineState{LineKey: q.LineKey{GroupID: 10, AccountID: 1, Model: rows[0].Model, Line: q.DimensionTTFO}}
	for _, kind := range []q.EventKind{q.EventRegular, q.EventTrial, q.EventReview} {
		_, basis := f.e.assessLine(d, state, kind, f.now, nil)
		require.Equal(t, []string{rows[1].AttemptID}, basis.AttemptIDs)
		require.Equal(t, []int64{10}, basis.SourceGroups)
		require.Equal(t, 1.0, basis.TTFT.N)
		require.Equal(t, 1.0, basis.TTFT.K)
	}
}

func TestTTFTEvidenceWindowNanosecondBoundaries(t *testing.T) {
	f := newQualityFixture(t)
	ctx := context.Background()
	owner := engineSamples(1, f.now, true)[0]
	owner.AttemptID, owner.SessionKey = "owner", "returning-session"
	owner.CaptureVersion, owner.Warm, owner.FullTierFirstOutputTimeout = 3, true, true
	owner.Outcome, owner.CensoredMs, owner.TTFOMs = q.OutcomeTimeout, owner.TTFOMs, nil
	owner.InsertedAt = f.now
	owner.FinishedAt = f.now.Add(-time.Second)

	peer := engineSamples(1, f.now, true)[0]
	peer.AttemptID, peer.SessionKey, peer.AccountID, peer.CaptureVersion = "peer", "peer-session", 2, 2
	peer.FinishedAt, peer.InsertedAt = owner.FinishedAt, owner.InsertedAt
	pred := engineSamples(1, f.now.Add(-time.Minute), false)[0]
	pred.AttemptID, pred.SessionKey, pred.AccountID = "pred", owner.SessionKey, peer.AccountID
	f.ledger.Rows = []q.AttemptSample{pred, owner, peer}
	require.NoError(t, f.e.loadEvidence(ctx, f.now))
	d, err := f.e.inputs(ctx, f.now, f.e.window.samples)
	require.NoError(t, err)
	for _, tc := range []struct {
		name            string
		at, cut         time.Time
		mass, priorMass float64
	}{
		{"before-insert", f.now.Add(-time.Nanosecond), time.Time{}, 0, 0},
		{"at-insert-and-cut", f.now, owner.StartedAt, 0, 1},
		{"start-before-cut", f.now, owner.StartedAt.Add(time.Nanosecond), 0, 1},
		{"at-window-start", f.now.Add(4 * time.Hour), time.Time{}, 1.0 / 256, 1.0 / 256},
		{"before-window-start", f.now.Add(4*time.Hour + time.Nanosecond), time.Time{}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			states := make(map[q.LineKey]q.LineState)
			for _, a := range []q.AttemptSample{owner, peer} {
				key := q.LineKey{GroupID: a.GroupID, AccountID: a.AccountID, Model: a.Model, Line: q.DimensionTTFO}
				states[key] = q.LineState{LineKey: key, Epoch: tc.cut}
			}
			for key, state := range states {
				in := ttftInput(d, key, tc.cut, tc.at, 1)
				result, err := q.EvaluateRuleB(in, q.DefaultRuleBParams())
				require.NoError(t, err)
				for _, counts := range []q.RuleBCounts{result.Counts} {
					require.Equal(t, tc.mass, counts.N)
					require.Equal(t, tc.mass, counts.K)
				}
				require.Equal(t, 2*tc.priorMass, result.PriorGroupCounts.N)
				require.Equal(t, 2*tc.priorMass, result.PriorGroupCounts.K)
				require.Equal(t, tc.priorMass, result.PriorLocalCounts.N)
				require.Equal(t, tc.priorMass, result.PriorLocalCounts.K)
				_, basis := f.e.assessLine(d, state, q.EventRegular, tc.at, nil)
				require.Equal(t, tc.mass, basis.TTFT.N)
				require.Equal(t, tc.mass, basis.TTFT.K)
				if tc.mass == 0 {
					require.Empty(t, basis.AttemptIDs)
					require.Empty(t, basis.SourceGroups)
				} else {
					wantID := owner.AttemptID
					if key.AccountID == peer.AccountID {
						wantID = peer.AttemptID
					}
					require.Equal(t, []string{wantID}, basis.AttemptIDs)
					require.Equal(t, []int64{owner.GroupID}, basis.SourceGroups)
				}
			}
		})
	}
}

func TestEngineRuleBGroupPriorUsesEventTimeAndKeepsSettings(t *testing.T) {
	f := newQualityFixture(t)
	ctx := context.Background()
	key := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}
	var outcomes []q.Verdict
	for _, peerSlow := range []bool{false, true} {
		rows := engineSamples(40, f.now, true)
		for i := range rows {
			a := &rows[i]
			a.AccountID = 1
			a.SessionKey = fmt.Sprintf("s%d", i%3)
			a.StartedAt, a.FinishedAt, a.InsertedAt = f.now.Add(-time.Second), f.now, f.now
			ms := 100
			if i >= 20 {
				a.AccountID = 2
			}
			if i < 6 || (i >= 20 && peerSlow) {
				ms = 2000
			}
			a.TTFOMs = &ms
		}
		// Another source group and evidence not yet visible must not alter the prior.
		foreign, future := rows[20], rows[20]
		foreign.AttemptID, foreign.GroupID = "foreign", 20
		future.AttemptID, future.InsertedAt = "future", f.now.Add(time.Second)
		opposite := 2000
		if peerSlow {
			opposite = 100
		}
		foreign.TTFOMs, future.TTFOMs = &opposite, &opposite
		f.e.window.samples = map[SchedAccountModel][]q.AttemptSample{
			{AccountID: 1, Model: key.Model}: rows[:20],
			{AccountID: 2, Model: key.Model}: append(rows[20:], foreign, future),
		}
		f.e.window.indexGroups()
		d, err := f.e.inputs(ctx, f.now, f.e.window.samples)
		require.NoError(t, err)
		// A peer reset does not filter the unchanged group-prior input path.
		verdict, basis := f.e.assessLine(d, q.LineState{LineKey: key}, q.EventRegular, f.now, map[int64]time.Time{2: f.now})
		outcomes = append(outcomes, verdict)
		prior := .001
		if peerSlow {
			prior = .999
		}
		require.Equal(t, prior, basis.TTFT.PriorMu)
		require.Equal(t, new(.2), basis.TTFT.Tolerance)
		require.Equal(t, new(.8), basis.TTFT.Confidence)
		require.Nil(t, basis.TTFT.PRel)
		require.Nil(t, basis.TTFT.U)
		f.e.rule.Params.Tolerance, f.e.rule.Params.Confidence = .3, .9
		require.Equal(t, new(.2), basis.TTFT.Tolerance, "old evidence must not follow later settings")
		require.Equal(t, new(.8), basis.TTFT.Confidence)
		f.e.rule.Params = q.DefaultRuleBParams()
	}
	require.Equal(t, []q.Verdict{q.VerdictUnknown, q.VerdictFail}, outcomes)
}

func TestEnginePeerMarginSaveDoesNotRejudge(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.cycle()
	before := append([]q.LineState(nil), f.store.Rows...)
	decisions := len(f.store.Decisions)
	params := q.DefaultRuleBParams()
	params.PeerMargin = .77
	result, err := f.e.UpdateTTFTRule(context.Background(), 0, params)
	require.NoError(t, err)
	require.Equal(t, q.RuleBUnchanged, result.Change)
	require.Zero(t, result.Cleared)
	require.Equal(t, params, f.store.Rule.Params)
	require.Equal(t, before, f.store.Rows)
	require.Len(t, f.store.Decisions, decisions)
}
