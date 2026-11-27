package resilience

// ProfitBasis freezes the selected account's admission and the last eight
// rejected decisions for a single logical request or WebSocket turn.
type ProfitBasis struct {
	Admitted *ProfitDecision  `json:"admitted,omitempty"`
	Vetoes   []ProfitDecision `json:"vetoes,omitempty"`
}

type ProfitDecision struct {
	AccountID int64    `json:"account_id"`
	Sell      float64  `json:"sell"`
	Upstream  *float64 `json:"upstream"`
	Source    string   `json:"source"`
	Ceiling   *float64 `json:"ceiling"`
	Floor     *float64 `json:"floor"`
	Verdict   string   `json:"verdict"`
}

type profitTrace struct {
	passed map[int64]ProfitDecision
	sent   *ProfitDecision
	vetoes []ProfitDecision
}

func cloneProfitDecision(in ProfitDecision) ProfitDecision {
	copyFloat := func(p *float64) *float64 {
		if p == nil {
			return nil
		}
		n := *p
		return &n
	}
	in.Upstream, in.Ceiling, in.Floor = copyFloat(in.Upstream), copyFloat(in.Ceiling), copyFloat(in.Floor)
	return in
}

func (s *RequestState) NoteProfitDecision(decision ProfitDecision) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.gatewayRecord
	if r == nil || r.final != nil {
		return
	}
	if r.profit == nil {
		r.profit = &profitTrace{passed: make(map[int64]ProfitDecision)}
	}
	decision = cloneProfitDecision(decision)
	if decision.Verdict == "pass" || decision.Verdict == "pinned" {
		r.profit.passed[decision.AccountID] = decision
	} else {
		if len(r.profit.vetoes) == 8 {
			r.profit.vetoes = append(r.profit.vetoes[:0], r.profit.vetoes[1:]...)
		}
		r.profit.vetoes = append(r.profit.vetoes, decision)
	}
}

func (p *profitTrace) noteSend(accountID int64) {
	if p == nil {
		return
	}
	p.sent = nil
	if decision, ok := p.passed[accountID]; ok {
		frozen := cloneProfitDecision(decision)
		p.sent = &frozen
	}
}

func (p *profitTrace) snapshot() *ProfitBasis {
	if p == nil {
		return nil
	}
	out := &ProfitBasis{}
	if p.sent != nil {
		v := cloneProfitDecision(*p.sent)
		out.Admitted = &v
	}
	for _, veto := range p.vetoes {
		out.Vetoes = append(out.Vetoes, cloneProfitDecision(veto))
	}
	return out
}

func CloneProfitBasis(in *ProfitBasis) *ProfitBasis {
	if in == nil {
		return nil
	}
	out := &ProfitBasis{}
	if in.Admitted != nil {
		v := cloneProfitDecision(*in.Admitted)
		out.Admitted = &v
	}
	for _, veto := range in.Vetoes {
		out.Vetoes = append(out.Vetoes, cloneProfitDecision(veto))
	}
	return out
}

// CompensationProfitPolicy is the scheduling gate's configuration, not a
// billing-time lookup. Known=false distinguishes fail-open from a disabled gate.
type CompensationProfitPolicy struct {
	GroupID   int64
	Known     bool
	Enabled   bool
	MinMargin float64
}

func (s *RequestState) NoteCompensationProfitPolicy(policy CompensationProfitPolicy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.compensationProfitPolicy = policy
	s.mu.Unlock()
}

func (s *RequestState) CompensationProfitPolicy() CompensationProfitPolicy {
	if s == nil {
		return CompensationProfitPolicy{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compensationProfitPolicy
}
