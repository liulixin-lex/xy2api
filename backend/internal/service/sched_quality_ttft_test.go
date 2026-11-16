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
				rows := make(map[q.LineKey]q.LineState)
				key := q.LineKey{AccountID: tc.Scope.AccountID, GroupID: tc.Scope.GroupID, Model: tc.Scope.Model, Line: q.DimensionTTFO}
				state := q.LineState{LineKey: key, Epoch: stamp(tc.Cut)}
				kind := q.EventRegular
				if spec.review {
					kind = q.EventReview
					state.Entry = stamp(tc.Cut)
					state.Epoch = f.now
					state.Identity = q.IdentityRear
				}
				rows[key] = state
				for _, peer := range tc.Peers {
					k := q.LineKey{AccountID: peer.Scope.AccountID, GroupID: peer.Scope.GroupID, Model: peer.Scope.Model, Line: q.DimensionTTFO}
					identity := q.IdentityUnknown
					if peer.Identity == 1 {
						identity = q.IdentityRear
					}
					rows[k] = q.LineState{LineKey: k, Identity: identity, Epoch: stamp(peer.Epoch)}
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
				d.peers = freezePeers(f.now, rows)
				in := ttftInput(d, key, state.Cut(kind), f.now, tc.LimitSeconds, nil)
				got, err := q.EvaluateRuleB(in, tc.Params)
				require.NoError(t, err)
				require.InDelta(t, tc.Expected.Counts.N, got.Counts.N, 1e-8)
				require.InDelta(t, tc.Expected.Counts.K, got.Counts.K, 1e-8)
				require.InDelta(t, tc.Expected.Counts.X, got.Counts.X, 1e-8)
				require.Equal(t, tc.Expected.Counts.S, got.Counts.S)
				require.Equal(t, tc.Expected.Counts.D, got.Counts.D)
				require.InDelta(t, tc.Expected.Prior, got.Prior, 1e-8)
				require.InDelta(t, tc.Expected.PeerPrior, got.PeerPrior, 1e-8)
				require.Equal(t, tc.Expected.State, got.State)
			})
		}
	}
}

func TestEnginePeerSnapshotPrecedesAllTransitions(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.e.window.merge(f.ledger.Rows, f.now)
	d, err := f.e.inputs(context.Background(), f.now, f.e.window.samples)
	require.NoError(t, err)
	keys, rows := f.e.rowKeys(d, nil)
	for _, k := range keys {
		rows[k] = q.LineState{LineKey: k}
	}
	run := func(reverse bool) map[q.LineKey]q.LineState {
		current := make(map[q.LineKey]q.LineState)
		for k, v := range rows {
			current[k] = v
		}
		d.peers = freezePeers(f.now, current)
		result := make(map[q.LineKey]q.LineState)
		for i := range keys {
			index := i
			if reverse {
				index = len(keys) - 1 - i
			}
			k := keys[index]
			r := current[k]
			v, b := f.e.assessLine(d, r, q.EventRegular, f.now, nil)
			current[k], _ = q.Step(r, q.EventRegular, v, b, f.now)
			result[k] = current[k]
		}
		return result
	}
	require.Equal(t, run(false), run(true))
	for _, peer := range d.peers.rows {
		require.Equal(t, q.IdentityUnknown, peer.Identity)
	}
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
			rows, _, _, _ := f.e.replay(d, nil, nil, at.Add(-q.RegularInterval), at)
			require.Len(t, rows, 900)
			replayed := time.Since(started) - prepared
			f.e.publish(d, rows, nil, nil, f.now)
			t.Logf("300 TTFO keys; 6 groups x 50 members; %d samples/account (%d total); prepare=%s replay=%s snapshot=%s total=%s", sampleCount, sampleCount*300, prepared, replayed, time.Since(started)-prepared-replayed, time.Since(started))
		})
	}
}
