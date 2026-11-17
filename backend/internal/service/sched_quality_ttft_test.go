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
				require.InDelta(t, tc.Expected.PeerCounts.N, got.PeerCounts.N, 1e-8)
				require.InDelta(t, tc.Expected.PeerCounts.K, got.PeerCounts.K, 1e-8)
				require.InDelta(t, tc.Expected.PeerCounts.X, got.PeerCounts.X, 1e-8)
				if tc.Expected.U == nil {
					require.Nil(t, got.U)
				} else {
					require.NotNil(t, got.U)
					require.InDelta(t, *tc.Expected.U, *got.U, 1e-8)
				}
				require.Equal(t, tc.Expected.State, got.State)
			})
		}
	}
}

func TestEnginePeerSnapshotPrecedesAllTransitions(t *testing.T) {
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
		require.Equal(t, q.IdentityUnknown, d.peers.rows[slow].Identity, "snapshot predates slow peer's entry")
		require.Len(t, decisions, 1)
		require.Equal(t, q.ActionEnter, decisions[0].Action)
		// This is a discriminating fixture: using the updated membership would
		// remove the fast account's only peer and change its verdict to unknown.
		d.peers = freezePeers(at, result)
		wrong, _ := f.e.assessLine(d, q.LineState{LineKey: fast}, q.EventRegular, at, nil)
		require.Equal(t, q.VerdictUnknown, wrong)
		return result
	}
	require.Equal(t, run(false), run(true))
}

func TestEnginePeerBucketsSurviveRewindFollowerAndOtherLineChanges(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.cycle()
	bucket := f.now.UTC().Truncate(q.RegularInterval)
	frozen := f.e.peerBuckets[bucket]
	slow := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}
	require.Equal(t, q.IdentityUnknown, frozen.rows[slow].Identity)
	late := engineSamples(1, bucket.Add(-10*time.Second), true)[0]
	late.AttemptID = "late-for-previous-bucket"
	late.InsertedAt = late.FinishedAt
	f.ledger.Rows = append(f.ledger.Rows, late)
	f.now = f.now.Add(20 * time.Second)
	f.cycle()
	require.Same(t, frozen, f.e.peerBuckets[bucket], "replaying the preceding bucket must not refreeze this one")
	f.store.lock.Lock()
	f.cycle()
	f.store.lock.Unlock()
	require.Same(t, frozen, f.e.peerBuckets[bucket], "losing the cycle lock must keep the frozen membership")
	for _, line := range []q.Dimension{q.DimensionError, q.DimensionCache} {
		p := f.store.Policies[10].Policy
		if line == q.DimensionError {
			p.MaxErrorRate += .1
		} else {
			p.MinCacheRatio -= .1
		}
		_, err := f.e.UpdatePolicy(context.Background(), 10, f.store.Version, p)
		require.NoError(t, err)
		require.Same(t, frozen, f.e.peerBuckets[bucket], "relaxing %s must not change TTFO membership", line)
	}
}

func TestEngineTTFTRelaxOnlyOverridesAffectedBucketKeys(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	f.store.Members[20] = []int64{1, 2}
	f.store.Policies[20] = SchedPolicyRecord{GroupID: 20, Policy: f.store.Policies[10].Policy}
	f.cycle()
	bucket := f.now.UTC().Truncate(q.RegularInterval)
	original := f.e.peerBuckets[bucket]
	// Recreate a preexisting bucket with both groups in rear to make a
	// full refreeze distinguishable from replacing only this group's keys.
	frozen := freezePeers(f.now, original.rows)
	for k, r := range frozen.rows {
		r.Identity = q.IdentityRear
		r.Epoch = f.now
		frozen.rows[k] = r
	}
	f.e.peerBuckets[bucket] = frozen
	p := f.store.Policies[10].Policy
	p.TTFTSeconds = 2
	_, err := f.e.UpdatePolicy(context.Background(), 10, 0, p)
	require.NoError(t, err)
	for k, old := range frozen.rows {
		next := f.e.peerBuckets[bucket].rows[k]
		if k.GroupID == 10 {
			require.Equal(t, q.IdentityUnknown, next.Identity)
			require.True(t, next.Epoch.IsZero())
		} else {
			require.Equal(t, old, next)
		}
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
			rows, _, _, _, err := f.e.replay(context.Background(), d, nil, nil, at.Add(-q.RegularInterval), at)
			require.NoError(t, err)
			require.Len(t, rows, 900)
			replayed := time.Since(started) - prepared
			f.e.publish(d, rows, nil, nil, f.now)
			t.Logf("300 TTFO keys; 6 groups x 50 members; %d samples/account (%d total); prepare=%s replay=%s snapshot=%s total=%s", sampleCount, sampleCount*300, prepared, replayed, time.Since(started)-prepared-replayed, time.Since(started))
		})
	}
}

func TestEngineDisplayDoesNotFreezeFutureBucket(t *testing.T) {
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
	require.NotContains(t, f.e.peerBuckets, bucket, "display must not save membership before replay reaches the bucket")
	require.NoError(t, f.e.SetOverride(context.Background(), SchedOverride{AccountID: 1, GroupID: 10, Mode: q.OverrideExempt}))
	require.NotContains(t, f.e.peerBuckets, bucket, "rebuild must not freeze a future bucket")
	require.NoError(t, f.e.ClearOverride(context.Background(), 1, 10))
	require.NotContains(t, f.e.peerBuckets, bucket)
	f.now = bucket.Add(13 * time.Second)
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().TTFO.Identity)
	require.Equal(t, bucket.Add(-5*time.Second), f.line(q.DimensionTTFO).Entry)
	require.Contains(t, f.e.peerBuckets, bucket)
	require.Equal(t, q.IdentityRear, f.e.peerBuckets[bucket].rows[key].Identity, "official membership includes the pre-boundary trial failure")
}

func TestEngineRejudgeDoesNotSaveFutureBucket(t *testing.T) {
	for _, line := range []q.Dimension{q.DimensionTTFO, q.DimensionError, q.DimensionCache} {
		t.Run(string(line), func(t *testing.T) {
			f := newQualityFixture(t)
			bucket := f.now.UTC().Truncate(q.RegularInterval)
			f.now = bucket.Add(3 * time.Second)
			f.badTTFT()
			f.cycle()
			require.NotContains(t, f.e.peerBuckets, bucket)
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
			require.NotContains(t, f.e.peerBuckets, bucket, "immediate reevaluation uses disposable membership before replay reaches the bucket")
		})
	}
}
