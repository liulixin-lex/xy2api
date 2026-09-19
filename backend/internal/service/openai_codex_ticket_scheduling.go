package service

import (
	"context"
	"encoding/json"
	"time"
)

const codexTicketSchedulingKey = "codex_ticket_readiness"

type codexTicketSchedulingEntry struct {
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
		entry := codexTicketSchedulingEntry{Revision: ac.Revision}
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
		if rt.RetryAfter != nil && rt.RetryAfter.After(now) {
			continue
		}
		if rt.Requested {
			return true
		}
		var t openAICodexTicket
		raw, _ := json.Marshal(a.Extra[openAICodexTicketExtraKey(model)])
		if json.Unmarshal(raw, &t) != nil || !t.Verified || t.ParserVersion != codexTicketParserVersion ||
			t.ConfigRevision != ac.Revision || t.FixedProxyFingerprint != codexTicketFixedProxyFingerprint(a) ||
			t.TransportFingerprint != s.codexTicketTransportFingerprint(a) ||
			t.AccountID != a.ID || t.Model != model || t.Length != codexTicketTargetLength(ac.TicketPlan) ||
			t.needsRefresh(now, time.Duration(s.openAICodexTicketConfig().RefreshBeforeSeconds)*time.Second) {
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
