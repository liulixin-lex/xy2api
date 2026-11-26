package service

import (
	"cmp"
	"math"
	"math/big"
	"slices"
	"strconv"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/shopspring/decimal"
)

const schedPerfWindow = 12 * time.Hour
const schedPerfTick = 5 * time.Minute

// The ledger reader resolves request and session adjacency before cutting the window.
// A zero Input means the attempt did not qualify as cache-line evidence.
type SchedPerfFact struct {
	ID, Session                              string
	Group, Account                           int64
	Model                                    string
	Start, Finish                            int64 // Unix microseconds, the precision of the ledger
	Read, Input                              int64
	TTFO, Censored                           int // milliseconds; Censored is -1 when the ledger has no lower bound
	Outcome                                  q.Outcome
	First, Delivered, Stream, Warm, FullTier bool
}

type SchedPerfSwitch struct {
	ID, Group, Account, Target               int64
	At                                       time.Time
	Request, Model, Kind, Reason, Generation string
}

type SchedPerfRecent struct{ First10, First30 int }
type schedPerfFirst struct {
	N            int      `json:"n"`
	Slow         int      `json:"slow"`
	Sessions     int      `json:"sessions"`
	ExpectedN    int      `json:"expected_n"`
	ExpectedSlow *float64 `json:"expected_slow"`
	Unknown      int      `json:"unknown"`
}
type schedPerfCont struct {
	Turns       int   `json:"turns"`
	Sessions    int   `json:"sessions"`
	ReadTokens  int64 `json:"read_tokens"`
	InputTokens int64 `json:"input_tokens"`
}
type schedPerfAttempts struct {
	N        int `json:"n"`
	Timeouts int `json:"timeouts"`
	Failures int `json:"failures"`
}
type schedPerfSwitches struct {
	Handoff     int `json:"handoff"`
	Takeover    int `json:"takeover"`
	Migrate     int `json:"migrate"`
	ProfitFloor int `json:"profit_floor"`
	Other       int `json:"other"`
}
type schedPerfScore struct {
	Value    *float64 `json:"value"`
	N        int      `json:"n"`
	Sessions int      `json:"sessions"`
	Excess   float64  `json:"excess"`
	micros   int64
}
type schedPerfRow struct {
	First    schedPerfFirst    `json:"first"`
	Cont     schedPerfCont     `json:"cont"`
	Attempts schedPerfAttempts `json:"attempts"`
	Requests int               `json:"requests"`
	Switches schedPerfSwitches `json:"switches"`
	recent   SchedPerfRecent
}
type SchedPerfSnapshot struct {
	at, from time.Time
	complete bool
	rows     map[schedQualityKey]*schedPerfRow
	scores   map[SchedAccountModel]schedPerfScore
	first15  map[int64]int
	policies map[int64]SchedPolicyRecord // immutable version used to classify every row
	reach    map[SchedAccountModel]schedPerfReach
	resets   map[int64]time.Time // reset cutoffs used to build reach
}

func (s *SchedPerfSnapshot) Usable(now time.Time) bool {
	return s != nil && s.complete && !now.Before(s.at) && now.Sub(s.at) <= 2*schedPerfTick
}
func (s *SchedPerfSnapshot) ScoreMicros(accountID int64, model string) (int64, bool) {
	if s == nil || !s.complete {
		return 0, false
	}
	score := s.scores[SchedAccountModel{accountID, model}]
	return score.micros, score.Value != nil
}
func (s *SchedPerfSnapshot) Recent(groupID, accountID int64, model string) SchedPerfRecent {
	if s != nil {
		if row := s.rows[schedQualityKey{accountID, model, groupID}]; row != nil {
			return row.recent
		}
	}
	return SchedPerfRecent{}
}
func (s *SchedPerfSnapshot) First15(accountID int64) int {
	if s == nil {
		return 0
	}
	return s.first15[accountID]
}

func (s *SchedPerfSnapshot) row(k schedQualityKey) *schedPerfRow {
	r := s.rows[k]
	if r == nil {
		r = &schedPerfRow{}
		s.rows[k] = r
	}
	return r
}

type perfCounts struct{ n, slow int }
type perfStrata [2]perfCounts // cold, warm
type perfTraining struct {
	strata         perfStrata
	sessions, cont map[string]struct{}
}

func perfKeyOrder(a, b schedQualityKey) int {
	if n := cmp.Compare(a.group, b.group); n != 0 {
		return n
	}
	if n := cmp.Compare(a.model, b.model); n != 0 {
		return n
	}
	return cmp.Compare(a.account, b.account)
}

func buildSchedPerf(facts map[string]SchedPerfFact, switches map[int64]SchedPerfSwitch, policies map[int64]SchedPolicyRecord, resets map[int64]time.Time, at time.Time) *SchedPerfSnapshot {
	s := &SchedPerfSnapshot{at: at, from: at.Add(-schedPerfWindow), complete: true, policies: policies, resets: resets, rows: make(map[schedQualityKey]*schedPerfRow), scores: make(map[SchedAccountModel]schedPerfScore), first15: make(map[int64]int)}
	reach := make(map[SchedAccountModel]*schedPerfReachBuilder)
	training := make(map[schedQualityKey]*perfTraining)
	peers := make(map[schedGroupModel]perfStrata)
	limits := make(map[int64]uint64, len(policies))
	for group, record := range policies {
		if record.Policy.Enabled {
			limits[group] = perfLimitMillis(record.Policy.TTFTSeconds)
		}
	}
	end, begin := at.UnixMicro(), s.from.UnixMicro()
	for _, f := range facts {
		if f.Finish < begin || f.Finish >= end || f.Start > f.Finish || f.Start <= resets[f.Account].UnixMicro() {
			continue
		}
		am := SchedAccountModel{f.Account, f.Model}
		if reach[am] == nil {
			reach[am] = &schedPerfReachBuilder{}
		}
		reach[am].add(f)
		limit, enabled := limits[f.Group]
		if !enabled {
			continue
		}
		key := schedQualityKey{f.Account, f.Model, f.Group}
		r := s.row(key)
		r.Attempts.N++
		if f.Outcome == q.OutcomeTimeout && f.FullTier {
			r.Attempts.Timeouts++
		} else if f.Outcome != q.OutcomeOK && f.Outcome != q.OutcomeCanceled {
			r.Attempts.Failures++
		}
		if f.Delivered {
			r.Requests++
		}
		t := training[key]
		if t == nil {
			t = &perfTraining{sessions: make(map[string]struct{}), cont: make(map[string]struct{})}
			training[key] = t
		}
		if f.Input > 0 {
			r.Cont.Turns++
			r.Cont.InputTokens += f.Input
			r.Cont.ReadTokens += f.Read
			t.cont[f.Session] = struct{}{}
		}
		if !f.First || !f.Stream {
			continue
		}
		age := end - f.Start
		if age < (10 * time.Minute).Microseconds() {
			r.recent.First10++
		}
		if age < (15 * time.Minute).Microseconds() {
			s.first15[f.Account]++
		}
		if age < (30 * time.Minute).Microseconds() {
			r.recent.First30++
		}
		slow, known := perfSlow(f, limit)
		if !known {
			r.First.Unknown++
			continue
		}
		r.First.N++
		r.First.Slow += slow
		if f.Session != "" {
			t.sessions[f.Session] = struct{}{}
		}
		layer := 0
		if f.Warm {
			layer = 1
		}
		t.strata[layer].n++
		t.strata[layer].slow += slow
		gm := schedGroupModel{f.Group, f.Model}
		counts := peers[gm]
		counts[layer].n++
		counts[layer].slow += slow
		peers[gm] = counts
	}
	s.reach = make(map[SchedAccountModel]schedPerfReach, len(reach))
	for key, row := range reach {
		s.reach[key] = row.freeze()
	}
	excesses := make(map[SchedAccountModel]*big.Rat)
	for k, t := range training {
		r := s.rows[k]
		r.First.Sessions = len(t.sessions)
		r.Cont.Sessions = len(t.cont)
		total := peers[schedGroupModel{k.group, k.model}]
		n := total[0].n + total[1].n - r.First.N
		if n < 10 || r.First.N == 0 {
			continue
		}
		expected := perfExpectedSlow(t.strata, total, n, total[0].slow+total[1].slow-r.First.Slow)
		displayExpected, _ := expected.Float64()
		r.First.ExpectedSlow = &displayExpected
		r.First.ExpectedN = r.First.N
		am := SchedAccountModel{k.account, k.model}
		if excesses[am] == nil {
			excesses[am] = new(big.Rat)
		}
		residual := new(big.Rat).SetInt64(int64(r.First.Slow))
		residual.Sub(residual, expected)
		excesses[am].Add(excesses[am], residual)
		score := s.scores[am]
		score.N += r.First.N
		score.Sessions += r.First.Sessions // sessions are group scoped
		s.scores[am] = score
	}
	for k, excess := range excesses {
		score := s.scores[k]
		score.Excess, _ = excess.Float64()
		if score.N >= 20 && score.Sessions >= 2 {
			score.micros = perfScoreMicros(excess, score.N)
			value := float64(score.micros) / 1e6
			score.Value = &value
		}
		s.scores[k] = score
	}
	s.countSwitches(switches, resets)
	return s
}

// Each stratum contributes own.n * (peerSlowInStratum + 10*p) / (peerNInStratum + 10).
// Keep the fractions exact across strata and groups, including at half-micro ties.
func perfExpectedSlow(own, total perfStrata, peerN, peerSlow int) *big.Rat {
	prior := big.NewRat(int64(peerSlow), int64(peerN))
	prior.Mul(prior, big.NewRat(10, 1))
	expected := new(big.Rat)
	for i, cell := range own {
		if cell.n == 0 {
			continue
		}
		term := new(big.Rat).SetInt64(int64(total[i].slow - cell.slow))
		term.Add(term, prior)
		term.Mul(term, big.NewRat(int64(cell.n), int64(total[i].n-cell.n+10)))
		expected.Add(expected, term)
	}
	return expected
}

func perfScoreMicros(excess *big.Rat, n int) int64 {
	scaled := new(big.Rat).Mul(excess, big.NewRat(1_000_000, int64(n)+20))
	var whole, rem big.Int
	whole.QuoRem(scaled.Num(), scaled.Denom(), &rem)
	rem.Abs(&rem)
	if rem.Lsh(&rem, 1).Cmp(scaled.Denom()) >= 0 {
		whole.Add(&whole, big.NewInt(int64(scaled.Sign())))
	}
	return whole.Int64()
}

// Round the published decimal policy, including exact half-millisecond ties.
func perfLimitMillis(seconds float64) uint64 {
	value, err := decimal.NewFromString(strconv.FormatFloat(seconds, 'f', -1, 64))
	if err != nil {
		return math.MaxUint64
	}
	millis := value.Shift(3).Round(0).BigInt()
	if !millis.IsUint64() {
		return math.MaxUint64 // Beyond every signed ledger clock.
	}
	return millis.Uint64()
}

func perfSlow(f SchedPerfFact, limit uint64) (int, bool) {
	ms, censored, ok := perfFirstClock(f)
	if !ok || censored && ms < limit {
		return 0, false
	}
	if censored || ms > limit {
		return 1, true
	}
	return 0, true
}

func perfFirstClock(f SchedPerfFact) (ms uint64, censored, ok bool) {
	if f.TTFO > 0 {
		return uint64(f.TTFO), false, true
	}
	if f.Outcome == q.OutcomeTimeout && f.FullTier && f.Censored >= 0 {
		return uint64(f.Censored), true, true
	}
	return 0, false, false
}
func (s *SchedPerfSnapshot) countSwitches(events map[int64]SchedPerfSwitch, resets map[int64]time.Time) {
	type commit struct {
		group, from, to           int64
		request, kind, generation string
	}
	seen := make(map[commit]bool)
	// Older copies of the same committed event win deterministically.
	ids := make([]int64, 0, len(events))
	for id := range events {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		e := events[id]
		if e.Account <= 0 || e.Account == e.Target || e.Kind == "create" || !s.policies[e.Group].Policy.Enabled || e.At.Before(s.from) || !e.At.Before(s.at) || !e.At.After(resets[e.Account]) {
			continue
		}
		k := commit{e.Group, e.Account, e.Target, e.Request, e.Kind, e.Generation}
		if e.Request != "" || e.Generation != "" {
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		sw := &s.row(schedQualityKey{e.Account, e.Model, e.Group}).Switches
		switch e.Kind {
		case stickyFollow:
			sw.Handoff++
		case "takeover":
			sw.Takeover++
		case "migrate":
			sw.Migrate++
		case "owner_released":
			if e.Reason == "profit_floor" {
				sw.ProfitFloor++
			} else {
				sw.Other++
			}
		default:
			sw.Other++
		}
	}
}
