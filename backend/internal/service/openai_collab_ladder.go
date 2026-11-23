package service

import (
	"bytes"
	"context"
	"maps"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// CollabLadder runs only after a terminal is saved. The handler owns admission,
// delivery, settlement and restoring that terminal; this loop owns derivations.
// Select must return immediately and Send must report actual client delivery.
type CollabLadder struct {
	StopReason            CollabLadderStop
	LastEvidence          resilience.CollabEvidence
	Body                  CollabBody
	First                 int64
	Current               int64
	Progress              *CollabLadderProgress
	BeforeDisposal        func(context.Context, resilience.CollabEvidence) bool
	Evidence              resilience.CollabEvidence
	MaxAttempts           int
	ReturnToLegacy        bool
	NamedReasoningAllowed func(int64) bool
	Select                func(context.Context, int64, map[int64]bool) (int64, func())
	Send                  func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool)
	Continue              func() bool
	Sent                  func() bool
}

func (l *CollabLadder) Run(ctx context.Context) bool {
	l.StopReason = CollabLadderFallback
	l.LastEvidence = l.Evidence
	if l.First == 0 || l.Body.Shape.Ambiguous || l.Body.Shape.ContinuationPointer || l.Select == nil || l.Send == nil {
		return false
	}
	l.Evidence = l.Body.ResolveEvidence(l.Evidence)
	claimed := l.Evidence.Grade == resilience.CollabEvidenceGradeClaimed
	eligible := l.Evidence.Grade == resilience.CollabEvidenceGradeVerified || claimed && (l.Body.Shape.AgentCiphertext || l.Body.Shape.ToolOutputCiphertext)
	if !eligible {
		return false
	}
	progress := l.Progress
	if progress == nil {
		progress = &CollabLadderProgress{}
	}
	if progress.derived == nil {
		progress.derived = map[int64][]byte{}
	}
	if progress.probed == nil {
		progress.probed = map[int64]bool{}
		progress.replaced = map[int64]bool{}
		if progress.handled == nil {
			progress.handled = map[int64]map[string]bool{}
		}
	}
	if progress.namedFamilies == nil {
		progress.namedFamilies = map[collabNamedClass]bool{}
	}
	derived, probed, replaced := progress.derived, maps.Clone(progress.probed), progress.replaced
	switched, dropped := progress.switched, progress.dropped
	handled, namedFamilies := progress.handled, progress.namedFamilies
	current := l.Current
	if current == 0 {
		current = l.First
	}
	evidence := l.Evidence
	l.LastEvidence = evidence
	final := false
	var lastBody []byte
	lastAccount := int64(0)
	if last := progress.lastRejection; last != nil && l.Body.LadderPolicy(last.Evidence(l.Body.Shape), CollabLadderFinalDisposal).LocalEnding && !last.unlocatable {
		lastBody, lastAccount = last.Derivation, last.AccountID
	}

	for ctx.Err() == nil && (l.Continue == nil || l.Continue()) {
		reason := CollabLadderSelecting
		if final {
			reason = CollabLadderFallback
		}
		policy := l.Body.LadderPolicy(evidence, reason)
		if !policy.eligible {
			return false
		}
		// Final disposal is a request endpoint, including after an ordinary
		// account switch. A rejected replace-all never restarts selection.
		replacedAny := false
		for _, used := range replaced {
			replacedAny = replacedAny || used
		}
		if !final && replacedAny {
			l.StopReason = CollabLadderFinalDisposal
			if evidence.Named != nil && handled[current][evidence.Named.Identity] && !collabProtectedItem(l.Body, evidence.Named.Identity) {
				l.StopReason = CollabLadderHandledAgain
			}
			return false
		}
		claimed = policy.conservative
		var action resilience.CollabAction
		named := ""
		prefer := current
		var exclude map[int64]bool
		endpoint := false
		disposalReason := CollabLadderFinalDisposal
		if final {
			disposalReason = CollabLadderFallback
		}
		batchReasoning := false
		if policy.switchOnly {
			if switched || l.MaxAttempts == 1 {
				return false
			}
			action = resilience.CollabActionSwitchOriginal
		} else if final {
			action = resilience.CollabActionReplaceAll
		} else if evidence.Named != nil && !claimed {
			if evidence.Named.Ambiguous {
				return false
			}
			named = evidence.Named.Identity
			var item *CollabBodyItem
			for i := range l.Body.Items {
				if l.Body.Items[i].Identity == named {
					item = &l.Body.Items[i]
					break
				}
			}
			if item == nil {
				return false
			}
			if handled[current][named] {
				if !collabProtectedItem(l.Body, named) {
					l.StopReason = CollabLadderHandledAgain
					return false
				}
				if replaced[current] {
					l.StopReason = CollabLadderFinalDisposal
					return false
				}
				action = resilience.CollabActionReplaceAll
				endpoint = true
				named = ""
			} else if item.Family == resilience.CollabFamilyReasoning && l.NamedReasoningAllowed != nil && !l.NamedReasoningAllowed(current) {
				// Exhausted in-place reasoning admission falls back to the
				// separate final repair without authorizing local ending.
				disposalReason = CollabLadderFallback
				action = resilience.CollabActionReplaceAll
				named = ""
			} else if item.Family != resilience.CollabFamilyReasoning && item.Class != resilience.CollabItemClassReplaceable && !switched && l.MaxAttempts != 1 {
				action = resilience.CollabActionSwitchOriginal
			} else {
				action = resilience.CollabActionNamedRepair
				if item.Family != resilience.CollabFamilyReasoning && (item.Family == resilience.CollabFamilyCompaction || item.Class == resilience.CollabItemClassProtected || item.Class == resilience.CollabItemClassUnreadMessage) {
					prefer = l.First
				}
				if namedFamilies[collabNamedItemClass(*item)] {
					if item.Family == resilience.CollabFamilyReasoning {
						batchReasoning = true
					}
					if item.Class == resilience.CollabItemClassReplaceable {
						action = resilience.CollabActionDropUnprotected
					}
				}
			}
		} else if !claimed && evidence.Ownership == resilience.CollabOwnershipClassSourceConflict && !dropped {
			action = resilience.CollabActionDropUnprotected
		} else if progress.sourceBody != nil && !switched && l.MaxAttempts != 1 {
			action = resilience.CollabActionSwitchOriginal
		} else if !claimed && l.Body.Shape.ReasoningCiphertext && !probed[current] && l.MaxAttempts != 1 {
			action = resilience.CollabActionProbeReasoning
		} else if !switched && l.MaxAttempts != 1 {
			action = resilience.CollabActionSwitchOriginal
		} else if !dropped && l.MaxAttempts != 1 {
			action = resilience.CollabActionDropUnprotected
		} else {
			action = resilience.CollabActionReplaceAll
		}
		if collabOptionalAction(action) && (l.MaxAttempts == 1 || progress.optional >= resilience.CollabOptionalSendLimit) {
			action = resilience.CollabActionReplaceAll
			named = ""
		}
		if !policy.permits(action) {
			return false
		}
		if action == resilience.CollabActionSwitchOriginal {
			prefer = 0
			exclude = map[int64]bool{current: true}
			switched = true
		}
		if action == resilience.CollabActionDropUnprotected {
			dropped = true
			prefer = l.First
		}
		if action == resilience.CollabActionProbeReasoning {
			probed[current] = true
		}
		if action == resilience.CollabActionReplaceAll {
			prefer = l.First
			exclude = replaced
			if replaced[prefer] {
				prefer = 0
			}
		}
		if endpoint {
			prefer = current
		}
		// Early entry returns to the existing first-account repair planner
		// before a broad fallback could discard unrelated content. Terminal
		// entry still gets every remaining repair under the shared quotas.
		if l.ReturnToLegacy && prefer == l.First && (action == resilience.CollabActionDropUnprotected || action == resilience.CollabActionReplaceAll) {
			return false
		}
		account, release := l.Select(ctx, prefer, exclude)
		if account == 0 && prefer > 0 && action == resilience.CollabActionNamedRepair && prefer != l.First {
			account, release = l.Select(ctx, l.First, exclude)
		}
		if account == 0 && !endpoint && prefer > 0 && (action == resilience.CollabActionNamedRepair || action == resilience.CollabActionDropUnprotected || action == resilience.CollabActionReplaceAll) {
			account, release = l.Select(ctx, 0, exclude)
		}
		if account == 0 {
			if action == resilience.CollabActionSwitchOriginal || action == resilience.CollabActionProbeReasoning || action == resilience.CollabActionDropUnprotected {
				continue
			}
			l.StopReason = CollabLadderDeliveryUnavailable
			return false
		}
		if release == nil {
			release = func() {}
		}
		body := derived[account]
		if body == nil {
			body = l.Body.Original
			if progress.sourceBody != nil {
				body = progress.sourceBody
			}
		}
		body = progress.repairs.apply(l.Body, body)
		next, changed := body, action == resilience.CollabActionSwitchOriginal
		if !changed {
			derivation := policy.derivation(l.Body, action)
			deriveAction := action
			if batchReasoning {
				deriveAction = resilience.CollabActionProbeReasoning
			}
			next, changed = derivation.Derive(body, deriveAction, named, claimed)
		}
		if !changed {
			release()
			if action == resilience.CollabActionReplaceAll || action == resilience.CollabActionNamedRepair {
				if disposalReason == CollabLadderFinalDisposal && evidence.Grade == resilience.CollabEvidenceGradeVerified &&
					current == lastAccount && bytes.Equal(body, lastBody) &&
					(action == resilience.CollabActionReplaceAll || collabProtectedItem(l.Body, named)) && l.Body.fullyDisposed(body) {
					l.StopReason = CollabLadderFinalDisposal
				}
				return false
			}
			continue
		}
		if l.Body.LadderPolicy(l.LastEvidence, disposalReason).LocalEnding && l.BeforeDisposal != nil && (action == resilience.CollabActionReplaceAll || action == resilience.CollabActionNamedRepair && collabProtectedTask(l.Body, named)) {
			if l.BeforeDisposal(ctx, l.LastEvidence) {
				release()
				return true
			}
		}
		nextEvidence, delivered := func() (resilience.CollabEvidence, bool) { defer release(); return l.Send(ctx, account, next, action) }()
		if l.Sent == nil || l.Sent() {
			lastBody, lastAccount = next, account
			if action == resilience.CollabActionSwitchOriginal {
				progress.switched = true
			}
			if action == resilience.CollabActionDropUnprotected {
				progress.dropped = true
			}
			if action == resilience.CollabActionProbeReasoning {
				progress.probed[account] = true
			}
			if action == resilience.CollabActionReplaceAll {
				replaced[account] = true
			} else if collabOptionalAction(action) {
				progress.optional++
			}
			if handled[account] == nil {
				handled[account] = map[string]bool{}
			}
			if action != resilience.CollabActionSwitchOriginal && action != resilience.CollabActionProbeReasoning {
				physical, _ := l.Body.atBody(body)
				for _, item := range physical.Items {
					path := "input." + strconv.Itoa(item.Index)
					if item.Identity != "" && gjson.GetBytes(body, path).Raw != gjson.GetBytes(next, path).Raw {
						handled[account][item.Identity] = true
					}
				}
				if named != "" {
					for _, item := range l.Body.Items {
						if item.Identity == named {
							namedFamilies[collabNamedItemClass(item)] = true
						}
					}
				}
			}
			if action != resilience.CollabActionProbeReasoning {
				derived[account] = next
			}
			if action == resilience.CollabActionDropUnprotected && evidence.Ownership == resilience.CollabOwnershipClassSourceConflict {
				progress.sourceBody = next
			}
		} else if action == resilience.CollabActionNamedRepair || action == resilience.CollabActionReplaceAll {
			return false
		}
		if delivered {
			return true
		}
		l.LastEvidence = l.Body.ResolveEvidence(nextEvidence)
		if nextEvidence.Grade != resilience.CollabEvidenceGradeVerified && nextEvidence.Grade != resilience.CollabEvidenceGradeClaimed || nextEvidence.Ownership == resilience.CollabOwnershipClassCapacity {
			if action == resilience.CollabActionReplaceAll {
				return false
			}
			final = true
		}

		current, evidence = account, l.Body.ResolveEvidence(nextEvidence)
	}
	l.StopReason = CollabLadderDeliveryUnavailable
	if evidence.Named != nil && handled[current][evidence.Named.Identity] && !collabProtectedItem(l.Body, evidence.Named.Identity) {
		l.StopReason = CollabLadderHandledAgain
	}
	return false
}

// CollabTakeoverDuration includes all selection and cancellation time. Once
// output starts the caller stops this timer and normal stream rules resume.
func CollabTakeoverDuration(now, started time.Time, retryable, committed bool, deadline time.Time) time.Duration {
	limit := time.Minute
	if retryable {
		limit = 15 * time.Second
	}
	if !committed && !started.IsZero() {
		limit = min(limit, 90*time.Second-now.Sub(started))
	}
	if !deadline.IsZero() {
		limit = min(limit, deadline.Sub(now))
	}
	if limit < 0 {
		return 0
	}
	return limit
}

type collabNamedClass struct {
	family resilience.CollabFamily
	class  resilience.CollabItemClass
}

// Reasoning has its own batch rule. Replaceable bodies share one allowance
// across agent messages and tool outputs; protected and unread items stay apart.
func collabNamedItemClass(item CollabBodyItem) collabNamedClass {
	if item.Family == resilience.CollabFamilyReasoning {
		return collabNamedClass{family: item.Family}
	}
	return collabNamedClass{class: item.Class}
}

// Progress is request-owned and shared by early and terminal entry. Named and
// account-specific final repairs do not spend the optional switch/probe budget.
type CollabLadderProgress struct {
	derived           map[int64][]byte
	probed, replaced  map[int64]bool
	handled           map[int64]map[string]bool
	namedFamilies     map[collabNamedClass]bool
	switched, dropped bool
	optional          int
	sourceBody        []byte
	repairs           collabRequestRepairs
	lastRejection     *CollabRejection
}

func collabOptionalAction(action resilience.CollabAction) bool {
	return action == resilience.CollabActionSwitchOriginal || action == resilience.CollabActionProbeReasoning || action == resilience.CollabActionDropUnprotected
}
func collabProtectedItem(body CollabBody, named string) bool {
	for _, item := range body.Items {
		if item.Identity == named {
			return (item.Family == resilience.CollabFamilyAgentMessage || item.Family == resilience.CollabFamilyToolOutput) && item.Class == resilience.CollabItemClassProtected
		}
	}
	return false
}

func collabProtectedTask(body CollabBody, named string) bool {
	for _, item := range body.Items {
		if item.Identity == named {
			return item.Family == resilience.CollabFamilyAgentMessage && item.Class == resilience.CollabItemClassProtected
		}
	}
	return false
}

// RememberHandled carries confirmed legacy repairs into terminal rescue.
func (p *CollabLadderProgress) RememberHandled(account int64, items map[string]bool) {
	if len(items) == 0 {
		return
	}
	if p.handled == nil {
		p.handled = map[int64]map[string]bool{}
	}
	if p.handled[account] == nil {
		p.handled[account] = map[string]bool{}
	}
	for id, handled := range items {
		if handled {
			p.handled[account][id] = true
		}
	}
}

// RememberRejection keeps confirmed account-local edits with their handled facts.
// Re-entering the ladder must never restore bytes that this account rejected.
func (p *CollabLadderProgress) RememberRejection(rejection CollabRejection) {
	p.lastRejection = &rejection
	p.repairs.merge(rejection.repairs)
	p.RememberHandled(rejection.AccountID, rejection.Handled)
	if p.namedFamilies == nil {
		p.namedFamilies = map[collabNamedClass]bool{}
	}
	for class, used := range rejection.namedClasses {
		if used {
			p.namedFamilies[class] = true
		}
	}
	if len(rejection.Derivation) > 0 {
		if p.derived == nil {
			p.derived = map[int64][]byte{}
		}
		p.derived[rejection.AccountID] = rejection.Derivation
	}
}
