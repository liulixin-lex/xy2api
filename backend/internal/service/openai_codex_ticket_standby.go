package service

import (
	"sort"
	"time"
)

func codexStandbyKey(model string) string   { return openAICodexTicketExtraKey(model) + ":standby" }
func codexCandidateKey(model string) string { return openAICodexTicketExtraKey(model) + ":candidate" }

type CodexTicketSlotStatus struct {
	Usable       bool      `json:"usable"`
	ExpiresAt    time.Time `json:"expires_at"`
	LastReplayAt time.Time `json:"last_replay_at"`
}
type codexRenewalSample struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

func codexRenewalLead(rt *codexTicketRuntime, now time.Time, lifetime time.Duration) time.Duration {
	lead := 20 * time.Minute
	if rt.RenewalLeadSeconds > 0 {
		lead = time.Duration(rt.RenewalLeadSeconds) * time.Second
	}
	if rt.RenewalComputedAt.IsZero() || now.Sub(rt.RenewalComputedAt) >= time.Hour {
		samples := rt.Renewals[:0]
		var durations []time.Duration
		for _, sample := range rt.Renewals {
			if sample.FinishedAt.After(now.Add(-24 * time.Hour)) {
				samples = append(samples, sample)
				durations = append(durations, sample.FinishedAt.Sub(sample.StartedAt))
			}
		}
		rt.Renewals = samples
		if len(durations) >= 10 {
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p95 := durations[(len(durations)*95+99)/100-1]
			lead = 2*p95 + 5*time.Minute
			if lead < 15*time.Minute {
				lead = 15 * time.Minute
			}
			if lead > 30*time.Minute {
				lead = 30 * time.Minute
			}
		}
		rt.RenewalLeadSeconds = int(lead / time.Second)
		rt.RenewalComputedAt = now
	}
	if lifetime > 0 && lead > lifetime/2 {
		lead = lifetime / 2
	}
	return lead
}
func (s *OpenAIGatewayService) publishCodexTicket(a *Account, model string, ticket *openAICodexTicket, now time.Time) string {
	ac := codexAccountTicketConfigOf(a)
	active := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[openAICodexTicketExtraKey(model)])
	standby := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[codexStandbyKey(model)])
	if codexPreviouslyRevoked(a, model, ticket.State, now) {
		return "unchanged"
	}
	// A previously revoked value cannot regain validity through rediscovery.
	for _, prior := range []*openAICodexTicket{active, standby} {
		if prior != nil && prior.State == ticket.State {
			ticket.FirstObservedAt = prior.FirstObservedAt
			if ticket.FirstObservedAt.IsZero() {
				ticket.FirstObservedAt = prior.CapturedAt
			}
			ticket.ExpiresAt = codexTicketEarlier(ticket.ExpiresAt, prior.ExpiresAt)
			if prior.Revoked {
				return "unchanged"
			}
			if prior.Verified {
				prior.LastReplayAt = now
				return "unchanged"
			}
		}
	}
	ticket.LastReplayAt = now
	if s.openAICodexTicketConfig().StandbyEnabled && active.validFor(a, ac, now) {
		if ticket.ExpiresAt.After(active.ExpiresAt) && ticket.IssuedAt.After(active.IssuedAt) && (!standby.validFor(a, ac, now) || ticket.ExpiresAt.After(standby.ExpiresAt)) {
			a.Extra[codexStandbyKey(model)] = ticket
			return "standby"
		}
		return "unchanged"
	}
	a.Extra[openAICodexTicketExtraKey(model)] = ticket
	return "active"
}
func (s *OpenAIGatewayService) promoteCodexStandby(a *Account, model string, now time.Time) bool {
	if !s.openAICodexTicketConfig().StandbyEnabled {
		return false
	}
	ac := codexAccountTicketConfigOf(a)
	active := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[openAICodexTicketExtraKey(model)])
	standby := parseOpenAICodexTicketFromAny(a.ID, model, a.Extra[codexStandbyKey(model)])
	if active.validFor(a, ac, now) && active.ExpiresAt.After(now.Add(2*time.Minute)) {
		return false
	}
	if !standby.validFor(a, ac, now) || !standby.ExpiresAt.After(now.Add(10*time.Minute)) || standby.LastReplayAt.Before(now.Add(-5*time.Minute)) {
		return false
	}
	a.Extra[openAICodexTicketExtraKey(model)] = standby
	delete(a.Extra, codexStandbyKey(model))
	return true
}
