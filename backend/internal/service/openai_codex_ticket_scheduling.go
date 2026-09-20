package service

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

const codexTicketSchedulingKey = "codex_ticket_readiness"

type codexTicketSchedulingEntry struct {
	Isolated  bool      `json:"isolated"`
	Ready     bool      `json:"ready"`
	Revision  string    `json:"revision"`
	ExpiresAt time.Time `json:"expires_at"`
}

// The cache contains admission hints only. Injection always reads the live row.
func CodexTicketSchedulingSummary(a *Account, now time.Time) any {
	ac := codexAccountTicketConfigOf(a)
	if !ac.Enabled {
		return nil
	}
	out := make(map[string]codexTicketSchedulingEntry, len(ac.Models))
	for _, model := range ac.Models {
		t := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[openAICodexTicketExtraKey(model)])
		entry := codexTicketSchedulingEntry{Revision: ac.Revision, Isolated: codexQualityOf(a, model).Isolated}
		if t.validFor(a, ac, now) {
			entry.Ready = true
			_, _, entry.ExpiresAt, _ = t.boundedTimes(time.Duration(t.TTLSeconds) * time.Second)
		}
		out[model] = entry
	}
	return out
}

// Scan rows omit STATE and unrelated credentials; their persisted, versioned
// metadata is sufficient to shortlist work, never to publish or inject a ticket.
func (s *OpenAIGatewayService) codexTicketScanDue(a *Account, now time.Time) bool {
	ac := codexAccountTicketConfigOf(a)
	if !ac.Enabled || !codexAccountTicketEligible(a) || codexTicketAccountRetryAfter(a, now) != nil {
		return false
	}
	if reason, _ := iqHealth(a, now); reason != "" {
		return false
	}
	if a.IQCheck.LeaseUntil != nil && a.IQCheck.LeaseUntil.After(now) {
		return false
	}
	runtimes := codexTicketRuntimes(a)
	for _, rt := range runtimes {
		if rt.LeaseUntil != nil && rt.LeaseUntil.After(now) {
			return false
		}
	}
	for _, model := range ac.Models {
		rt := runtimes[model]
		activateCodexPendingManual(&rt)
		if rt.HardRetryAfter != nil && rt.HardRetryAfter.After(now) {
			continue
		}
		if rt.RetryAfter != nil && rt.RetryAfter.After(now) && (!rt.Task.active() || rt.Task.Source != "manual") {
			continue
		}
		if rt.Requested || rt.Task.active() && (rt.NextAttemptAt == nil || !rt.NextAttemptAt.After(now)) {
			return true
		}
		if rt.NextAttemptAt != nil {
			if rt.NextAttemptAt.After(now) {
				continue
			}
			return true
		}
		if s.openAICodexTicketConfig().QualityObservationEnabled && !codexQualityOf(a, model).NextAt.After(now) {
			return true
		}
		var t openAICodexTicket
		raw, _ := json.Marshal(a.Extra[openAICodexTicketExtraKey(model)])
		if json.Unmarshal(raw, &t) != nil || !t.Verified || t.ParserVersion != codexTicketParserVersion ||
			t.ConfigRevision != ac.Revision || t.FixedProxyFingerprint != codexTicketFixedProxyFingerprint(a) ||
			t.TransportFingerprint != s.codexTicketTransportFingerprint(a) ||
			t.AccountID != a.ID || t.Model != model || t.Length != codexTicketTargetLength(ac.TicketPlan) ||
			t.needsRefresh(now, s.codexTicketRenewalWindow(&rt, now)) {
			return true
		}
	}
	return false
}

type codexTicketScanRepository interface {
	ListCodexTicketScanPage(context.Context, int64, int) ([]Account, error)
	MutateCodexTicketBatch(context.Context, []int64, int, CodexTicketMutation) ([]*Account, error)
}

func (s *OpenAIGatewayService) scanCodexTicketJobs(ctx context.Context, repo codexTicketScanRepository) {
	s.openaiCodexScanMu.Lock()
	defer s.openaiCodexScanMu.Unlock()
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if pool == "" || ValidateOpenAICodexTicketHarvestProxyURL(pool) != nil {
		return
	}
	var ids []int64
	ranks := map[int64]int{}
	for ctx.Err() == nil && len(ids) < 100 {
		page, err := repo.ListCodexTicketScanPage(ctx, s.openaiCodexScanCursor, 100)
		if err != nil {
			return
		}
		if len(page) == 0 {
			s.openaiCodexScanCursor = 0
			break
		}
		for i := range page {
			s.openaiCodexScanCursor = page[i].ID
			if s.codexTicketScanDue(&page[i], time.Now()) {
				ids = append(ids, page[i].ID)
				ranks[page[i].ID] = codexTicketUrgency(&page[i], time.Now())
				if len(ids) == 100 {
					break
				}
			}
		}
		if len(ids) == 100 {
			break
		}
		if len(page) < 100 {
			s.openaiCodexScanCursor = 0
			break
		}
	}
	if len(ids) == 0 {
		return
	}
	if s.openAICodexTicketConfig().AdaptiveSchedulingEnabled {
		slices.SortStableFunc(ids, func(a, b int64) int { return ranks[a] - ranks[b] })
	}
	jobs := make(map[int64]*codexAccountTicketJob)
	limit := codexTicketClusterLimit()
	_, err := repo.MutateCodexTicketBatch(s.codexTicketFencedContext(ctx, pool), ids, limit, func(a *Account, active int, now time.Time) (bool, error) {
		job := s.claimCodexTicket(a, active, limit, now, false, "", pool)
		if job != nil {
			jobs[a.ID] = job
		}
		return job != nil, nil
	})
	if err != nil {
		return
	}
	for id, job := range jobs {
		s.launchCodexTicketJob(ctx, id, job)
	}
}

func (s *OpenAIGatewayService) codexTicketRenewalWindow(rt *codexTicketRuntime, now time.Time) time.Duration {
	cfg := s.openAICodexTicketConfig()
	if cfg.AdaptiveSchedulingEnabled {
		return codexRenewalLead(rt, now, time.Duration(cfg.TTLSeconds)*time.Second)
	}
	return time.Duration(cfg.RefreshBeforeSeconds) * time.Second
}

// Scan projection intentionally excludes STATE. Rank from bounded ticket
// metadata; authoritative claim/publication still loads the full locked row.
func codexTicketUrgency(a *Account, now time.Time) int {
	best := 9
	ac := codexAccountTicketConfigOf(a)
	runtimes := codexTicketRuntimes(a)
	for _, model := range ac.Models {
		var t openAICodexTicket
		raw, _ := json.Marshal(a.Extra[openAICodexTicketExtraKey(model)])
		_ = json.Unmarshal(raw, &t)
		rank := 4
		if !t.Verified || t.Revoked || t.ConfigRevision != ac.Revision || t.FixedProxyFingerprint != codexTicketFixedProxyFingerprint(a) || !t.ExpiresAt.After(now) {
			rank = 0
		} else if !t.ExpiresAt.After(now.Add(5 * time.Minute)) {
			rank = 2
		}
		rt := runtimes[model]
		activateCodexPendingManual(&rt)
		if !rt.Task.active() || rt.Task.Source != "manual" {
			rank++
		}
		if rank < best {
			best = rank
		}
	}
	return best
}
