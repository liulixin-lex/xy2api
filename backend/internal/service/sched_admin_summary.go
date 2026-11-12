package service

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

func (s *SchedAdminService) Summary(_ context.Context, ids []int64, model string) (SchedAdminSummary, error) {
	out := SchedAdminSummary{Accounts: []SchedAdminAccountSummary{}}
	if len(ids) > schedquality.SummaryMaxAccounts {
		return out, infraerrors.BadRequest("TOO_MANY_ACCOUNTS", "At most 1000 account ids are allowed")
	}
	for _, id := range ids {
		if id <= 0 {
			return out, infraerrors.BadRequest("INVALID_ACCOUNT_ID", "Invalid account id")
		}
	}
	p, now := s.snapshot(), s.d.Now()
	out.At, out.Stale, out.GlobalEnabled = schedAdminStamp(p.At), now.Sub(p.At) > schedquality.ViewMaxAge, p.GlobalEnabled
	model = strings.TrimSpace(model)
	type modelEvidence struct {
		model   string
		samples int
	}
	var defaults map[int64]modelEvidence
	if model == "" && len(ids) > 0 {
		defaults = make(map[int64]modelEvidence, len(ids))
		for _, id := range ids {
			defaults[id] = modelEvidence{}
		}
		for key, evidence := range p.Evidence {
			best, requested := defaults[key.AccountID]
			if requested && evidence.Samples > 0 && (evidence.Samples > best.samples || (evidence.Samples == best.samples && key.Model < best.model)) {
				defaults[key.AccountID] = modelEvidence{model: key.Model, samples: evidence.Samples}
			}
		}
	}
	for _, id := range ids {
		x := SchedAdminAccountSummary{AccountID: id, Cooling: []SchedAdminAccountSummaryCooling{}, Overrides: []SchedAdminAccountSummaryOverride{}}
		m := model
		if m == "" {
			m = defaults[id].model
		}
		v := p.Evidence[SchedAccountModel{id, m}]
		if m != "" && v.Samples > 0 {
			x.Model = schedAdminString(m)
		}
		x.Samples, x.TTFO.N, x.Error.N, x.Cache.N = v.Samples, v.Speed.N, v.Error.N, v.Cache.Turns
		if schedAdminEnough(v.Speed.N, v.Speed.Sessions) {
			x.TTFO.P90Ms, x.TTFO.Timeout = schedAdminInt(v.Speed.P90Ms), v.Speed.P90Timeout
		}
		if schedAdminEnough(v.Error.N, v.Error.Sessions) {
			x.Error.Rate = schedAdminFloat(v.Error.Rate)
		}
		if schedAdminEnough(v.Cache.Turns, v.Cache.Sessions) {
			x.Cache.Ratio = schedAdminFloat(v.Cache.Ratio)
		}
		groups := map[int64]int{}
		for _, group := range p.Members[id] {
			groups[group] = 0
		}
		rows := append([]SchedGroupRow(nil), p.GroupRows[id]...)
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].GroupID == rows[j].GroupID {
				return rows[i].Model < rows[j].Model
			}
			return rows[i].GroupID < rows[j].GroupID
		})
		coolingAt := map[int64]int{}
		for _, r := range rows {
			state, member := groups[r.GroupID]
			if !member {
				continue
			}
			if r.View.State == schedquality.StateCooling {
				groups[r.GroupID] = 2
				testing := p.Tests[SchedAccountModel{id, r.Model}].Running
				cooling := SchedAdminAccountSummaryCooling{GroupID: r.GroupID, Model: r.Model, Reason: string(r.View.Reason), Until: schedAdminTime(r.View.Until), WaitingTest: r.View.WaitingTest, Testing: testing}
				if state != 2 {
					coolingAt[r.GroupID] = len(x.Cooling)
					x.Cooling = append(x.Cooling, cooling)
				} else if index, ok := coolingAt[r.GroupID]; ok && testing && !x.Cooling[index].Testing {
					x.Cooling[index] = cooling
				}
			} else if state != 2 && schedAdminHasReal(r.Summary) {
				groups[r.GroupID] = 1
			}
		}
		for _, state := range groups {
			x.Groups.Total++
			switch state {
			case 2:
				x.Groups.Cooling++
			case 1:
				x.Groups.Normal++
			default:
				x.Groups.Nodata++
			}
		}
		for _, o := range p.Overrides {
			if o.AccountID != id || (o.GroupID != 0 && !slices.Contains(p.Members[id], o.GroupID)) || (o.ExpiresAt != nil && !now.Before(*o.ExpiresAt)) {
				continue
			}
			var expiresAt *string
			if o.ExpiresAt != nil {
				expiresAt = schedAdminTime(*o.ExpiresAt)
			}
			x.Overrides = append(x.Overrides, SchedAdminAccountSummaryOverride{GroupID: o.GroupID, Mode: schedAdminOverrideMode(o.Mode), ExpiresAt: expiresAt})
		}
		sort.Slice(x.Overrides, func(i, j int) bool { return x.Overrides[i].GroupID < x.Overrides[j].GroupID })
		out.Accounts = append(out.Accounts, x)
	}
	return out, nil
}
func schedAdminHasReal(v schedquality.Summary) bool {
	return schedAdminEnough(v.Speed.N, v.Speed.Sessions) || schedAdminEnough(v.Error.N, v.Error.Sessions) || schedAdminEnough(v.Cache.Turns, v.Cache.Sessions)
}
func (s *SchedAdminService) Attention(_ context.Context) (SchedAdminAttention, error) {
	p := s.snapshot()
	out := SchedAdminAttention{Cooling: []int64{}, Exempt: []int64{}, Demote: []int64{}}
	now := s.d.Now()
	exempt, demote := map[int64]bool{}, map[int64]bool{}
	covered := map[[2]int64]bool{}
	for _, o := range p.Overrides {
		if (o.GroupID != 0 && !slices.Contains(p.Members[o.AccountID], o.GroupID)) || (o.ExpiresAt != nil && !now.Before(*o.ExpiresAt)) {
			continue
		}
		switch o.Mode {
		case schedquality.OverrideExempt:
			if !exempt[o.AccountID] {
				exempt[o.AccountID] = true
				out.Exempt = append(out.Exempt, o.AccountID)
			}
		case schedquality.OverrideDemote:
			if !demote[o.AccountID] {
				demote[o.AccountID] = true
				out.Demote = append(out.Demote, o.AccountID)
			}
		default:
			continue
		}
		covered[[2]int64{o.AccountID, o.GroupID}] = true
	}
	for id, rows := range p.GroupRows {
		if covered[[2]int64{id, 0}] {
			continue
		}
		for _, r := range rows {
			if r.View.State == schedquality.StateCooling && slices.Contains(p.Members[id], r.GroupID) && !covered[[2]int64{id, r.GroupID}] {
				out.Cooling = append(out.Cooling, id)
				break
			}
		}
	}
	sort.Slice(out.Cooling, func(i, j int) bool { return out.Cooling[i] < out.Cooling[j] })
	sort.Slice(out.Exempt, func(i, j int) bool { return out.Exempt[i] < out.Exempt[j] })
	sort.Slice(out.Demote, func(i, j int) bool { return out.Demote[i] < out.Demote[j] })
	return out, nil
}

func schedAdminMetric(n, sessions, value int) *int {
	if !schedAdminEnough(n, sessions) {
		return nil
	}
	return schedAdminInt(value)
}
func schedAdminRatio(n, sessions int, value float64) *float64 {
	if !schedAdminEnough(n, sessions) {
		return nil
	}
	return schedAdminFloat(value)
}

func (s *SchedAdminService) Detail(ctx context.Context, id int64) (SchedAdminAccountDetail, error) {
	out := SchedAdminAccountDetail{Models: []SchedAdminAccountDetailModels{}, Groups: []SchedAdminAccountDetailGroups{}, AutoStates: []SchedAdminAccountDetailAutoStates{}, RecentAttempts: []SchedAdminAccountDetailRecentAttempts{}}
	a, e := s.account(ctx, id)
	if e != nil {
		return out, e
	}
	p, now := s.snapshot(), s.d.Now()
	rate, _ := openAISchedulingRate(a, now, s.d.Gateway.openAIOAuthSchedulingRateMultiplier(ctx))
	out.Account = SchedAdminAccountDetailAccount{ID: id, Name: a.Name, Priority: a.Priority, Rate: rate, Concurrency: a.Concurrency}
	if s.d.InFlight != nil {
		out.Account.InFlight, e = s.d.InFlight(ctx, id)
		if e != nil {
			return out, e
		}
	}
	out.At, out.Stale, out.GlobalEnabled, out.TestsEnabled, out.ResetAt = schedAdminStamp(p.At), now.Sub(p.At) > schedquality.ViewMaxAge, p.GlobalEnabled, p.TestsEnabled, schedAdminTime(p.ResetAt[id])
	models := map[string]bool{}
	for k := range p.Evidence {
		if k.AccountID == id {
			models[k.Model] = true
		}
	}
	for k := range p.Tests {
		if k.AccountID == id {
			models[k.Model] = true
		}
	}
	keys := make([]string, 0, len(models))
	for m := range models {
		keys = append(keys, m)
	}
	sort.Strings(keys)
	for _, m := range keys {
		v := p.Evidence[SchedAccountModel{id, m}]
		x := SchedAdminAccountDetailModels{Model: m, Samples: v.Samples, Sessions: v.Sessions, From: schedAdminTime(v.From), To: schedAdminTime(v.To), TTFO: SchedAdminAccountDetailModelsTTFO{N: v.Speed.N, Sessions: v.Speed.Sessions, Timeouts: v.Speed.Timeouts, P90Ms: schedAdminMetric(v.Speed.N, v.Speed.Sessions, v.Speed.P90Ms), P90Timeout: v.Speed.P90Timeout, FailBoundMs: schedAdminMetric(v.Speed.N, v.Speed.Sessions, v.Speed.FailBoundMs), FailBoundTimeout: v.Speed.FailBoundTimeout, ByClass: []SchedAdminAccountDetailModelsTTFOByClass{}}, Error: SchedAdminAccountDetailModelsError{N: v.Error.N, Sessions: v.Error.Sessions, Bad: v.Error.Bad, Rate: schedAdminRatio(v.Error.N, v.Error.Sessions, v.Error.Rate), Lcb: schedAdminRatio(v.Error.N, v.Error.Sessions, v.Error.LCB)}, Cache: SchedAdminAccountDetailModelsCache{Turns: v.Cache.Turns, Sessions: v.Cache.Sessions, Ratio: schedAdminRatio(v.Cache.Turns, v.Cache.Sessions, v.Cache.Ratio)}}
		x.RequestModel = openAIRequestModelForEvidence(a, m)
		for c, stat := range v.Speed.ByClass {
			var value *int
			if stat.N > 0 {
				value = schedAdminInt(stat.P90Ms)
			}
			x.TTFO.ByClass = append(x.TTFO.ByClass, SchedAdminAccountDetailModelsTTFOByClass{Class: schedquality.RequestClass(c).String(), N: stat.N, P90Ms: value})
		}
		if t, ok := p.Tests[SchedAccountModel{id, m}]; ok {
			x.Test = schedAdminTestDetail(t)
		}
		out.Models = append(out.Models, x)
	}
	groupRows := map[int64][]SchedGroupRow{}
	for _, r := range p.GroupRows[id] {
		groupRows[r.GroupID] = append(groupRows[r.GroupID], r)
	}
	groups := make([]*Group, 0, len(a.Groups))
	for _, group := range a.Groups {
		if group != nil && group.Platform == PlatformOpenAI {
			groups = append(groups, group)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	out.AllGroupsOverride = s.override(ctx, p, id, 0, now)
	for _, gr := range groups {
		g := gr.ID
		policy := p.Policies[g].Policy
		x := SchedAdminAccountDetailGroups{GroupID: g, Name: gr.Name, GateEnabled: policy.Enabled, Policy: SchedAdminAccountDetailGroupsPolicy{TTFTSeconds: policy.TTFTSeconds, MaxErrorRate: policy.MaxErrorRate, MinCacheRatio: policy.MinCacheRatio, TakeoverIdleSeconds: policy.TakeoverIdleSeconds}, Override: s.override(ctx, p, id, g, now), ByModel: []SchedAdminAccountDetailGroupsByModel{}}
		sort.Slice(groupRows[g], func(i, j int) bool { return groupRows[g][i].Model < groupRows[g][j].Model })
		for _, r := range groupRows[g] {
			x.ByModel = append(x.ByModel, schedAdminGroupModel(r, policy, p.Tests[SchedAccountModel{id, r.Model}].Running))
		}
		out.Groups = append(out.Groups, x)
	}
	states, e := s.autoStates(ctx, a, now)
	if e != nil {
		return out, e
	}
	for _, v := range states {
		var until *string
		if v.Until != nil {
			until = schedAdminTime(*v.Until)
		}
		out.AutoStates = append(out.AutoStates, SchedAdminAccountDetailAutoStates{Key: v.Key, Kind: v.Kind, Scope: v.Scope, Model: v.Model, Until: until, Count: v.Count, Shared: v.Shared, Clearable: v.Clearable, Why: v.Why})
	}
	if s.d.Attempts != nil {
		rows, e := s.d.Attempts.LoadRecentByAccount(ctx, id, schedquality.RecentAttemptsMax)
		if e != nil {
			return out, e
		}
		if len(rows) > schedquality.RecentAttemptsMax {
			rows = rows[:schedquality.RecentAttemptsMax]
		}
		for _, r := range rows {
			out.RecentAttempts = append(out.RecentAttempts, SchedAdminAccountDetailRecentAttempts{At: schedAdminStamp(r.StartedAt), GroupID: r.GroupID, Model: r.Model, Class: r.Class.String(), Stream: r.Stream, Warm: r.Warm, TTFOMs: r.TTFOMs, Outcome: r.Outcome.String(), Detail: r.Detail})
		}
	}
	return out, nil
}
func schedAdminTestDetail(t SchedTestRecord) *SchedAdminAccountDetailModelsTest {
	t.Turns = append([]schedquality.TestTurn(nil), t.Turns...)
	sort.SliceStable(t.Turns, func(i, j int) bool { return t.Turns[i].Turn < t.Turns[j].Turn })
	x := &SchedAdminAccountDetailModelsTest{ID: t.ID, At: schedAdminStamp(t.StartedAt), Running: t.Running, Done: t.Done, AbortReason: t.AbortReason, Auto: t.Auto, Tiers: []SchedAdminAccountDetailModelsTestTiers{}, InputTokens: int(t.InputTokens), CachedTokens: int(t.CachedTokens), OutputTokens: int(t.OutputTokens)}
	x.Clean = schedquality.TestClean(t.TestResult)
	x.Kind = "full"
	if t.Auto {
		x.Kind = "screen"
		for _, turn := range t.Turns {
			if turn.Tier == schedquality.TierMedium || turn.Tier == schedquality.TierLarge {
				x.Kind = "full"
				break
			}
		}
	}
	for tier := schedquality.TierSmall; tier <= schedquality.TierLarge; tier++ {
		v := SchedAdminAccountDetailModelsTestTiers{Tier: tier.String(), TTFOMs: []*int{}}
		input, cached, found := 0, 0, false
		for _, turn := range t.Turns {
			if turn.Tier != tier {
				continue
			}
			found = true
			if turn.Turn > 1 && turn.Turn <= schedquality.TestTurnsPerTier && len(v.TTFOMs) < schedquality.TestTurnsPerTier-1 {
				v.TTFOMs = append(v.TTFOMs, turn.TTFOMs)
				if turn.Outcome == schedquality.OutcomeOK && turn.PromptTokens != nil && turn.PrevPromptTokens != nil && turn.CacheReadTokens != nil && *turn.PromptTokens >= *turn.PrevPromptTokens && *turn.PrevPromptTokens > 0 {
					input += *turn.PrevPromptTokens
					cached += min(*turn.CacheReadTokens, *turn.PrevPromptTokens)
				}
			}
			if turn.Outcome == schedquality.OutcomeAccountFault {
				v.Faults++
			}
			if turn.Outcome == schedquality.OutcomeTimeout {
				v.Timeouts++
			}
		}
		if input > 0 {
			v.CacheRatio = schedAdminFloat(float64(cached) / float64(input))
		}
		if found {
			x.Tiers = append(x.Tiers, v)
		}
	}
	return x
}
func schedAdminSource(v schedquality.Verdict, test bool) *string {
	if v == schedquality.VerdictNoData {
		return nil
	}
	if test {
		return schedAdminString("test")
	}
	return schedAdminString("real")
}
func schedAdminGroupModel(r SchedGroupRow, p schedquality.Policy, running bool) SchedAdminAccountDetailGroupsByModel {
	v, w, t := r.Summary, r.View, r.Test
	x := SchedAdminAccountDetailGroupsByModel{Model: r.Model, State: w.State.String(), Level: w.Level, Reason: string(w.Reason), Since: schedAdminTime(w.Since), WaitingTest: w.WaitingTest, Testing: running, TTFO: SchedAdminAccountDetailGroupsByModelTTFO{Verdict: w.Verdicts.Speed.String(), From: schedAdminSource(w.Verdicts.Speed, w.FromTest[0]), N: v.Speed.N, P90Ms: schedAdminMetric(v.Speed.N, v.Speed.Sessions, v.Speed.P90Ms), P90Timeout: v.Speed.P90Timeout, LimitMs: int(p.TTFTSeconds * 1000)}, Error: SchedAdminAccountDetailGroupsByModelError{Verdict: w.Verdicts.Error.String(), From: schedAdminSource(w.Verdicts.Error, w.FromTest[1]), N: v.Error.N, Rate: schedAdminRatio(v.Error.N, v.Error.Sessions, v.Error.Rate), Limit: p.MaxErrorRate}, Cache: SchedAdminAccountDetailGroupsByModelCache{Verdict: w.Verdicts.Cache.String(), From: schedAdminSource(w.Verdicts.Cache, w.FromTest[2]), N: v.Cache.Turns, Ratio: schedAdminRatio(v.Cache.Turns, v.Cache.Sessions, v.Cache.Ratio), Limit: p.MinCacheRatio}}
	x.TTFO.LimitMs = int(math.Round(p.TTFTSeconds * 1000))
	if w.State == schedquality.StateCooling {
		x.Until = schedAdminTime(w.Until)
	}
	if w.FromTest[0] {
		x.TTFO.N = t.SpeedTurns
		x.TTFO.P90Ms = nil
		if t.SpeedWarm > 0 {
			x.TTFO.P90Ms = schedAdminInt(t.MaxTTFOMs)
		}
		x.TTFO.P90Timeout = t.AnyTimeout
	}
	if w.FromTest[1] {
		x.Error.N = t.ErrorTurns
		x.Error.Rate = nil
		if t.ErrorTurns > 0 {
			x.Error.Rate = schedAdminFloat(float64(t.Faults) / float64(t.ErrorTurns))
		}
	}
	if w.FromTest[2] {
		x.Cache.N = t.CacheTurns
		x.Cache.Ratio = nil
		if t.CacheTurns > 0 {
			x.Cache.Ratio = schedAdminFloat(t.CacheRatio)
		}
	}
	return x
}
func (s *SchedAdminService) Board(ctx context.Context, g int64, model string) (SchedAdminBoard, error) {
	out := SchedAdminBoard{GroupID: g, Models: []string{}, Accounts: []SchedAdminBoardAccounts{}}
	if _, e := s.group(ctx, g); e != nil {
		return out, e
	}
	p, now := s.snapshot(), s.d.Now()
	memberIDs := []int64{}
	for id, rows := range p.GroupRows {
		for _, row := range rows {
			if row.GroupID == g {
				memberIDs = append(memberIDs, id)
				break
			}
		}
	}
	members, e := s.d.Accounts.GetByIDs(ctx, memberIDs)
	if e != nil {
		return out, e
	}
	groupAccounts := make(map[int64]*Account, len(members))
	for _, a := range members {
		groupAccounts[a.ID] = a
	}
	out.Models = schedAdminModels(p, g, groupAccounts)
	out.Model = strings.TrimSpace(model)
	if out.Model == "" && len(out.Models) > 0 {
		out.Model = out.Models[0]
	}
	out.At, out.Stale, out.GlobalEnabled = schedAdminStamp(p.At), now.Sub(p.At) > schedquality.ViewMaxAge, p.GlobalEnabled
	rows, e := s.d.Board.Board(ctx, g, out.Model)
	if e != nil {
		return out, e
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.AccountID)
	}
	accounts, e := s.d.Accounts.GetByIDs(ctx, ids)
	if e != nil {
		return out, e
	}
	names := map[int64]string{}
	models := map[int64]string{}
	for _, a := range accounts {
		names[a.ID] = a.Name
		models[a.ID] = OpenAIAttemptModel(a, out.Model)
	}
	for _, r := range rows {
		model := models[r.AccountID]
		v := p.Evidence[SchedAccountModel{r.AccountID, model}]
		var w schedquality.StateView
		for _, gr := range p.GroupRows[r.AccountID] {
			if gr.GroupID == g && gr.Model == model {
				w = gr.View
				break
			}
		}
		x := SchedAdminBoardAccounts{AccountID: r.AccountID, Name: names[r.AccountID], Rank: r.Rank, Eligible: r.Eligible, Reject: r.Reject, Standing: r.Standing.String(), State: w.State.String(), Level: w.Level, WaitingTest: w.WaitingTest, Testing: p.Tests[SchedAccountModel{r.AccountID, model}].Running, Override: s.override(ctx, p, r.AccountID, g, now), Priority: r.Priority, Rate: r.Rate, InFlight: r.InFlight, Concurrency: r.Concurrency, RecentlyFullSeconds: r.RecentlyFullSeconds, CanTakeOver: r.CanTakeOver, TTFO: SchedAdminBoardAccountsTTFO{N: v.Speed.N, Sessions: v.Speed.Sessions, P90Ms: schedAdminMetric(v.Speed.N, v.Speed.Sessions, v.Speed.P90Ms), P90Timeout: v.Speed.P90Timeout, FailBoundMs: schedAdminMetric(v.Speed.N, v.Speed.Sessions, v.Speed.FailBoundMs), FailBoundTimeout: v.Speed.FailBoundTimeout}, Error: SchedAdminBoardAccountsError{N: v.Error.N, Sessions: v.Error.Sessions, Rate: schedAdminRatio(v.Error.N, v.Error.Sessions, v.Error.Rate), Lcb: schedAdminRatio(v.Error.N, v.Error.Sessions, v.Error.LCB)}, Cache: SchedAdminBoardAccountsCache{N: v.Cache.Turns, Sessions: v.Cache.Sessions, Ratio: schedAdminRatio(v.Cache.Turns, v.Cache.Sessions, v.Cache.Ratio)}, Verdicts: SchedAdminBoardAccountsVerdicts{TTFO: w.Verdicts.Speed.String(), Error: w.Verdicts.Error.String(), Cache: w.Verdicts.Cache.String()}}
		x.EvidenceModel = model
		if r.RejectUntil != nil {
			x.RejectUntil = schedAdminTime(*r.RejectUntil)
		}
		if w.State == schedquality.StateCooling {
			x.Until = schedAdminTime(w.Until)
		}
		out.Accounts = append(out.Accounts, x)
	}
	return out, nil
}
