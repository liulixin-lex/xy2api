package service

import (
	"context"
	"net/url"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func collabAccountOrigin(account *Account) string {
	if account == nil {
		return ""
	}
	target := account.GetOpenAIBaseURL()
	if account.IsOpenAIOAuthLike() {
		target = chatgptCodexURL
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Record ordering stays inside the scheduler's existing quality, trial,
// pinning and capability rounds. It cannot add a candidate or bypass admission.
func (s *OpenAIGatewayService) orderCollabSelection(ctx context.Context, accounts []*Account, keys map[int64]selectionKey, compact bool) {
	defer func() {
		if recover() != nil {
			resilience.Count("collab_wiring_panic", 0, 0, "routing")
		}
	}()
	r := openAICollabRequestFrom(ctx)
	if r == nil || !r.failed || !r.settings.CollabRecordRoutingOn() || len(accounts) < 2 {
		return
	}
	r.lookupRecords(ctx, true)
	items := make([]resilience.CollabRoutingItem, 0, len(r.state.Items))
	for _, item := range r.state.Items {
		items = append(items, resilience.CollabRoutingItem{Key: item.Key, Protected: item.Class == resilience.CollabItemClassProtected, UnfinishedTask: item.Kind == resilience.CollabMessageKindTask, Records: item.Records, Rejected: r.rejectedAccounts})
	}
	digests := make(map[int64]resilience.CollabDigest, len(accounts))
	byID := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		digests[account.ID], _ = s.CollabRecordAccount(account.ID, collabAccountOrigin(account))
		byID[account.ID] = account
	}
	for start := 0; start < len(accounts); {
		end := start + 1
		first := keys[accounts[start].ID]
		for end < len(accounts) {
			key := keys[accounts[end].ID]
			if key.Tier != first.Tier || key.Pinned != first.Pinned || compact && openAICompactSupportTier(accounts[start]) != openAICompactSupportTier(accounts[end]) {
				break
			}
			end++
		}
		ids := make([]int64, end-start)
		for index, account := range accounts[start:end] {
			ids[index] = account.ID
		}
		ordered := resilience.OrderCollabFailoverCandidates(r.settings, ids, digests, items, time.Now())
		for index, id := range ordered {
			accounts[start+index] = byID[id]
		}
		start = end
	}
}
