package iqcheck

// Outcome uses the persisted single-attempt statuses: smart, degraded, unknown.
// Retryable matters only for unknown; every entry consumes one physical request.
type Outcome struct {
	Status    string
	Retryable bool
}

func DecideRound(mode string, outcomes []Outcome) (status string, done bool) {
	correct, wrong := 0, 0
	for i, o := range outcomes {
		switch o.Status {
		case "unknown":
			if !o.Retryable {
				return "unknown", true
			}
		case "degraded":
			wrong++
			if mode == "recheck" || wrong == 2 {
				return "degraded", true
			}
		case "smart":
			correct++
			if mode == "recheck" && correct == 3 || mode == "normal" && (wrong == 0 || correct == 2) {
				return "smart", true
			}
		}
		if i+1 == MaxAttemptsPerRound {
			return "unknown", true
		}
	}
	return "", false
}

// RoundSuspicious marks a normal round recovered by two correct confirmations.
func RoundSuspicious(mode string, outcomes []Outcome) bool {
	status, done := DecideRound(mode, outcomes)
	if mode != "normal" || !done || status != "smart" {
		return false
	}
	for _, o := range outcomes {
		if o.Status != "unknown" {
			return o.Status == "degraded"
		}
	}
	return false
}

func AttemptEffort(mode, configured string, outcomes []Outcome) string {
	if mode == "normal" {
		for _, o := range outcomes {
			if o.Status == "degraded" {
				return NextEffort(configured)
			}
		}
	}
	return configured
}
