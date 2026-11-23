package resilience

import "slices"

type collabIssueIdentity struct {
	Scope                       CollabRecordScope
	Call, Digest, Account, Site CollabDigest
	Generation                  CollabGeneration
}

func collabIssueID(r CollabIssuedRecord) collabIssueIdentity {
	return collabIssueIdentity{r.Scope, r.Call, r.Digest, r.Account, r.Site, r.Generation}
}

func collabUnion[T any, K comparable](remote, local []T, key func(T) K) []T {
	result := slices.Clone(remote)
	for _, v := range local {
		found := false
		for i, r := range result {
			if key(r) == key(v) {
				result[i] = v
				found = true
				break
			}
		}
		if !found {
			result = append(result, v)
		}
	}
	return result
}

func collabTokenStatusRank(status CollabIssuedStatus) int {
	switch status {
	case CollabIssuedStatusVoid:
		return 3
	case CollabIssuedStatusConfirmed:
		return 2
	case CollabIssuedStatusPending:
		return 1
	default:
		return 0
	}
}
func collabMergeIssueTokens(remote, local []CollabIssuedEntry) []CollabIssuedEntry {
	result := slices.Clone(remote)
	index := make(map[CollabIssuedToken]int, len(remote)+len(local))
	for i, r := range result {
		index[r.Token] = i
	}
	for _, v := range local {
		if i, ok := index[v.Token]; ok {
			if collabTokenStatusRank(v.Record.Status) > collabTokenStatusRank(result[i].Record.Status) {
				result[i] = v
			}
		} else {
			index[v.Token] = len(result)
			result = append(result, v)
		}
	}
	return result
}
func collabAggregateIssues(tokens []CollabIssuedEntry) []CollabIssuedRecord {
	var result []CollabIssuedRecord
	index := make(map[collabIssueIdentity]int)
	rank := func(s CollabIssuedStatus) int {
		switch s {
		case CollabIssuedStatusConfirmed:
			return 3
		case CollabIssuedStatusPending:
			return 2
		default:
			return 1
		}
	}
	for _, token := range tokens {
		r := token.Record
		key := collabIssueID(r)
		if i, ok := index[key]; ok {
			if rank(r.Status) > rank(result[i].Status) {
				result[i] = r
			}
		} else {
			index[key] = len(result)
			result = append(result, r)
		}
	}
	return result
}
func collabApplyVoids(r *CollabRecordLookup) {
	for i, entry := range r.IssuedTokens {
		for _, token := range r.VoidedTokens {
			if token.Scope == entry.Token.Scope && (token.Token == "" || token == entry.Token) {
				r.IssuedTokens[i].Record.Status = CollabIssuedStatusVoid
				break
			}
		}
	}
	r.Issued = collabAggregateIssues(r.IssuedTokens)
}
func collabMergeRecords(remote, local CollabRecordLookup) CollabRecordLookup {
	result := CollabRecordLookup{
		VoidedTokens: collabUnion(remote.VoidedTokens, local.VoidedTokens, func(t CollabIssuedToken) CollabIssuedToken { return t }),
		IssuedTokens: collabMergeIssueTokens(remote.IssuedTokens, local.IssuedTokens),
		Plaintext:    collabUnion(remote.Plaintext, local.Plaintext, func(r CollabPlaintextRecord) string { return string(r.Call) + ":" + string(r.Arguments) }),
		Consumed:     collabUnion(remote.Consumed, local.Consumed, func(r CollabConsumedRecord) string { return collabDeliveryField("c", r.Key, r.Account) }),
	}
	collabApplyVoids(&result)
	return result
}

func collabMergeDispositions(remote, local CollabDispositionLookup) CollabDispositionLookup {
	// Counts are snapshots, not increments. Combining two replicas must not
	// double count the same terminal; use the larger observation.
	counts := slices.Clone(remote.TerminalRejections)
	for _, v := range local.TerminalRejections {
		found := false
		for i, r := range counts {
			if r.Key == v.Key && r.Rejection == v.Rejection {
				if v.Count > r.Count {
					counts[i] = v
				}
				found = true
				break
			}
		}
		if !found {
			counts = append(counts, v)
		}
	}
	return CollabDispositionLookup{
		TerminalRejections: counts,
		Replaced:           collabUnion(remote.Replaced, local.Replaced, func(r CollabReplacedRecord) string { return collabDeliveryField("r", r.Key, r.Account) }),
		Terminated:         collabUnion(remote.Terminated, local.Terminated, func(r CollabTerminatedRecord) string { return collabDeliveryField("t", r.Key, r.Event) }),
	}
}
