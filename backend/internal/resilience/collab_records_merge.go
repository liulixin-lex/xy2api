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

// Local voids mask only the matching issue, never unrelated remote evidence.
func collabMergeIssues(remote, local []CollabIssuedRecord) []CollabIssuedRecord {
	result := slices.Clone(remote)
	for _, v := range local {
		found := false
		for i, r := range result {
			if collabIssueID(r) == collabIssueID(v) {
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
func collabMergeRecords(remote, local CollabRecordLookup) CollabRecordLookup {
	return CollabRecordLookup{
		Issued:    collabMergeIssues(remote.Issued, local.Issued),
		Plaintext: collabUnion(remote.Plaintext, local.Plaintext, func(r CollabPlaintextRecord) string { return string(r.Call) + ":" + string(r.Arguments) }),
		Consumed:  collabUnion(remote.Consumed, local.Consumed, func(r CollabConsumedRecord) string { return collabDeliveryField("c", r.Key, r.Account) }),
	}
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
		Terminated:         collabUnion(remote.Terminated, local.Terminated, func(r CollabTerminatedRecord) string { return collabDeliveryField("t", r.Key, "") }),
	}
}
