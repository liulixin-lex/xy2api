package schedquality

import "time"

// Summarize applies section 5 to samples of one account x model. samples = the 200 most recent by start time
// plus the first-output stream samples of the last 5 minutes (deduplicated by attempt id); since filters both.
func Summarize(samples []AttemptSample, since, now time.Time) Summary {
	var result Summary
	w0 := make([]AttemptSample, 0, WindowMaxSamples)
	streak := 0
	for _, s := range sortedSamples(samples, since, now) {
		if !s.StartedAt.Before(now.Add(-AcuteWindow)) && s.Stream && (s.TTFOMs != nil || s.Outcome == OutcomeTimeout) {
			if s.Outcome == OutcomeTimeout {
				streak++
				if streak >= AcuteTimeoutStreak {
					result.Acute = true
				}
			} else {
				streak = 0
			}
		}
		if evidenceOutcome(s.Outcome) && len(w0) < WindowMaxSamples {
			w0 = append(w0, s)
		}
	}
	cap := max(SessionShareMin, int(SessionShareCap*float64(len(w0))))
	sessions := make(map[sampleSession]int)
	speedSessions := make(map[sampleSession]bool)
	errorSessions := make(map[sampleSession]bool)
	cacheSessions := make(map[sampleSession]bool)
	var speeds []latency
	var byClass [4][]latency
	var cacheRead, cachePrompt int64
	for _, s := range w0 {
		session := sessionOf(s)
		if sessions[session] >= cap {
			continue
		}
		sessions[session]++
		result.Samples++
		if result.From.IsZero() || s.StartedAt.Before(result.From) {
			result.From = s.StartedAt
		}
		if result.To.IsZero() || s.StartedAt.After(result.To) {
			result.To = s.StartedAt
		}
		if s.Stream && s.Warm && (s.TTFOMs != nil || s.Outcome == OutcomeTimeout) {
			v := latency{timeout: s.Outcome == OutcomeTimeout}
			raw := v
			if v.timeout {
				result.Speed.Timeouts++
			} else {
				raw.ms = *s.TTFOMs
				v.ms = normalizedMs(raw.ms, Scale(s.Model, s.Class))
			}
			speeds = append(speeds, v)
			byClass[s.Class] = append(byClass[s.Class], raw)
			speedSessions[session] = true
		}
		if s.Outcome == OutcomeOK || s.Outcome == OutcomeAccountFault {
			result.Error.N++
			errorSessions[session] = true
			if s.Outcome == OutcomeAccountFault {
				result.Error.Bad++
			}
		}
		if s.Warm && s.Outcome == OutcomeOK && s.PromptTokens != nil && s.PrevPromptTokens != nil && s.CacheReadTokens != nil &&
			*s.PromptTokens >= CacheMinPromptTokens && *s.PrevPromptTokens >= CacheMinPromptTokens && *s.PromptTokens >= *s.PrevPromptTokens {
			result.Cache.Turns++
			cacheSessions[session] = true
			cacheRead += int64(min(*s.CacheReadTokens, *s.PrevPromptTokens))
			cachePrompt += int64(*s.PrevPromptTokens)
		}
	}
	result.Sessions = len(sessions)
	result.Speed.N, result.Speed.Sessions = len(speeds), len(speedSessions)
	sortLatencies(speeds)
	p, b := p90(speeds), failBound(speeds)
	result.Speed.P90Ms, result.Speed.P90Timeout = p.ms, p.timeout
	result.Speed.FailBoundMs, result.Speed.FailBoundTimeout = b.ms, b.timeout
	for class, values := range byClass {
		sortLatencies(values)
		v := p90(values)
		result.Speed.ByClass[class] = ClassStat{N: len(values), P90Ms: v.ms, P90Timeout: v.timeout}
	}
	result.Error.Sessions = len(errorSessions)
	if result.Error.N > 0 {
		result.Error.Rate = float64(result.Error.Bad) / float64(result.Error.N)
	}
	result.Error.LCB = WilsonLower(result.Error.Bad, result.Error.N)
	result.Cache.Sessions = len(cacheSessions)
	if cachePrompt > 0 {
		result.Cache.Ratio = float64(cacheRead) / float64(cachePrompt)
	}
	return result
}

// section 5; all nodata when the gate is disabled
func Judge(s Summary, p Policy) Verdicts {
	var v Verdicts
	if !p.Enabled {
		return v
	}
	if s.Speed.N >= MinSamples && s.Speed.Sessions >= MinSessions {
		switch {
		case s.Speed.FailBoundTimeout || speedLimitMs(p) < s.Speed.FailBoundMs:
			v.Speed = VerdictFail
		case s.Speed.P90Timeout || speedLimitMs(p) < s.Speed.P90Ms:
			v.Speed = VerdictMarginal
		default:
			v.Speed = VerdictPass
		}
	}
	if s.Error.N >= MinSamples && s.Error.Sessions >= MinSessions {
		switch {
		case p.MaxErrorRate < s.Error.LCB:
			v.Error = VerdictFail
		case p.MaxErrorRate < s.Error.Rate:
			v.Error = VerdictMarginal
		default:
			v.Error = VerdictPass
		}
	}
	if s.Cache.Turns >= MinSamples && s.Cache.Sessions >= MinSessions {
		v.Cache = VerdictPass
		if s.Cache.Ratio < p.MinCacheRatio {
			v.Cache = VerdictFail
		}
	}
	return v
}
