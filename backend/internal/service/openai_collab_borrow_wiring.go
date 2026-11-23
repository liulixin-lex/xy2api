package service

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// SelectOpenAICollabBorrow returns only an already admitted immediate slot.
// It is called before the original send solely under the explicit lossless gate.
func (s *OpenAIGatewayService) SelectOpenAICollabBorrow(ctx context.Context, c *gin.Context, group *int64, original *Account, model string) (selection *AccountSelectionResult) {
	defer func() {
		if recover() != nil {
			if selection != nil && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			selection = nil
			resilience.Count("collab_wiring_panic", 0, 0, "borrow")
		}
	}()
	r := openAICollabRequestFrom(ctx)
	if r == nil || !r.settings.CollabRedispatchBorrowOn() || r.scope.Tree == "" || original == nil || r.borrowAttempted {
		return nil
	}
	r.borrowAttempted = true
	convertible := func(account *Account) bool {
		mapped := OpenAIAttemptModel(account, model)
		return openAICollaborationEligible(c, account, mapped, OpenAIUpstreamTransportHTTPSSE, account.IsOpenAIPassthroughEnabled(), false, false) && PlanCodexCollaborationDeclarations(r.state.Original, CollabDeclarationOptions{IncrementalDeclarations: r.settings.CollabIncrementalDeclarationsOn(), ClientVersion: r.version}).Plan != nil
	}
	if convertible(original) {
		return nil
	}
	senders := map[string]resilience.CollabDigest{}
	for _, item := range gjson.GetBytes(r.state.Original, "input").Array() {
		for _, field := range []string{"author", "recipient"} {
			if raw := item.Get(field).String(); raw != "" {
				digest := s.CollabDigest(resilience.CollabDigestDomainThread, raw)
				if digest == r.state.Thread {
					senders[raw] = digest
					continue
				}
				if member, ok := s.CollabTreeAffinity().LookupThread(ctx, r.scope, digest); ok && member.Parent == r.state.Thread {
					senders[raw] = digest
				}
			}
		}
	}
	notices := s.ParseCollabSeenNotices(r.state.Original, senders)
	if len(notices) == 0 {
		return nil
	}
	store := s.CollabRecords()
	if store == nil {
		return nil
	}
	if r.readCtx == nil {
		r.readCtx, r.readCancel = resilience.WithCollabRecordReadBudget(ctx)
	}
	queries := make([]resilience.CollabRecordQuery, 0, len(notices)+len(r.state.Items))
	for _, notice := range notices {
		queries = append(queries, resilience.CollabRecordQuery{Key: resilience.CollabDeliveryKey{Scope: r.scope}, Event: notice.Event})
	}
	for _, item := range r.state.Items {
		queries = append(queries, resilience.CollabRecordQuery{Key: item.Key})
	}
	lookup := store.LookupBatch(r.readCtx, resilience.CollabRecordBatch{Scope: r.scope, Queries: queries, Records: r.settings.CollabRecordsOn(), Dispositions: true})
	if !lookup.DispositionsComplete {
		return nil
	}
	input := resilience.CollabBorrowInput{Settings: r.settings, Scope: r.scope, Thread: r.state.Thread, OriginalAccountID: original.ID, Notices: notices, Convertible: map[int64]bool{}, Immediate: map[int64]bool{}, Accounts: map[int64]resilience.CollabDigest{}}
	for _, disposition := range lookup.Dispositions {
		input.Terminated = append(input.Terminated, disposition.Terminated...)
	}
	for _, item := range r.state.Items {
		if item.Class == resilience.CollabItemClassProtected && len(lookup.Records[resilience.CollabRecordQuery{Key: item.Key}].Consumed) == 0 {
			input.ParentUnreadCiphertext = true
		}
	}
	// Unknown compaction/call ciphertext readability cannot authorize a borrow.
	parsed := ParseCollabBodyWithContext(r.state.Original, CollabBodyFactsForRequest(ctx))
	if parsed.Shape.CompactionCiphertext || parsed.Shape.CallArgumentsCiphertext {
		return nil
	}
	eligibleNotice := false
	for _, notice := range notices {
		eligibleNotice = eligibleNotice || notice.Current && resilience.CollabSeenEvent(notice, r.scope, r.state.Thread, input.Terminated, time.Now()).Source == resilience.CollabEventSourceSeenParentNotice
	}
	if !eligibleNotice || input.ParentUnreadCiphertext {
		return nil
	}
	excluded := map[int64]bool{original.ID: true}
	for range 32 {
		candidate, err := s.SelectCollabAccount(ctx, group, model, OpenAIEndpointCapabilityResponses, 0, excluded)
		if err != nil || candidate == nil || candidate.Account == nil {
			return nil
		}
		account := candidate.Account
		excluded[account.ID] = true
		input.Convertible[account.ID], input.Immediate[account.ID] = convertible(account), candidate.Acquired && candidate.WaitPlan == nil
		input.Accounts[account.ID], _ = s.CollabRecordAccount(account.ID, collabAccountOrigin(account))
		decision := resilience.DecideCollabRedispatchBorrow(input, []int64{account.ID}, time.Now())
		if decision.AccountID == account.ID {
			return candidate
		}
		if candidate.ReleaseFunc != nil {
			candidate.ReleaseFunc()
		}
		if ctx.Err() != nil {
			return nil
		}
	}
	return nil
}
