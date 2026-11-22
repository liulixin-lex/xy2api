package service

import (
	"context"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// CollabLadder runs only after a terminal is saved. The handler owns admission,
// delivery, settlement and restoring that terminal; this loop owns derivations.
// Select must return immediately and Send must report actual client delivery.
type CollabLadder struct {
	Body        CollabBody
	First       int64
	Evidence    resilience.CollabEvidence
	MaxAttempts int
	Select      func(context.Context, int64, map[int64]bool) (int64, func())
	Send        func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool)
	Continue    func() bool
}

func (l CollabLadder) Run(ctx context.Context) bool {
	if l.First == 0 || l.Body.Shape.Ambiguous || l.Body.Shape.ContinuationPointer || l.Select == nil || l.Send == nil {
		return false
	}
	claimed := l.Evidence.Grade == resilience.CollabEvidenceGradeClaimed
	eligible := l.Evidence.Grade == resilience.CollabEvidenceGradeVerified || claimed && (l.Body.Shape.AgentCiphertext || l.Body.Shape.ToolOutputCiphertext)
	if !eligible {
		return false
	}
	derived := map[int64][]byte{}
	probed := map[int64]bool{}
	replaced := map[int64]bool{}
	handled := map[int64]map[string]bool{}
	namedFamilies := map[int64]map[resilience.CollabFamily]bool{}
	var switched, dropped bool
	optional, replacements := 0, 0
	current := l.First
	evidence := l.Evidence
	var switchedNamed *resilience.CollabNamedItem
	for ctx.Err() == nil && (l.Continue == nil || l.Continue()) {
		if evidence.Ownership == resilience.CollabOwnershipClassCapacity || evidence.Ownership == resilience.CollabOwnershipClassContinuation {
			return false
		}
		if evidence.Grade != resilience.CollabEvidenceGradeVerified && evidence.Grade != resilience.CollabEvidenceGradeClaimed {
			return false
		}
		var action resilience.CollabAction
		named := ""
		prefer := current
		var exclude map[int64]bool
		if evidence.Named != nil {
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
			if item == nil || handled[current][named] {
				return false
			}
			if item.Family != resilience.CollabFamilyReasoning && item.Class != resilience.CollabItemClassReplaceable && !switched && l.MaxAttempts != 1 {
				action = resilience.CollabActionSwitchOriginal
			} else {
				action = resilience.CollabActionNamedRepair
				if item.Family == resilience.CollabFamilyCompaction || item.Class == resilience.CollabItemClassProtected || item.Class == resilience.CollabItemClassUnreadMessage {
					prefer = l.First
				}
				if namedFamilies[current][item.Family] {
					if item.Family == resilience.CollabFamilyReasoning {
						action = resilience.CollabActionProbeReasoning
					}
					if item.Family == resilience.CollabFamilyAgentMessage && item.Class == resilience.CollabItemClassReplaceable {
						action = resilience.CollabActionDropUnprotected
					}
				}
			}
		} else if evidence.Ownership == resilience.CollabOwnershipClassUnknownSuffix || evidence.Family == resilience.CollabFamilyOwnership && !l.Body.Shape.AgentCiphertext {
			if switched || l.MaxAttempts == 1 {
				return false
			}
			action = resilience.CollabActionSwitchOriginal
		} else if !claimed && evidence.Ownership == resilience.CollabOwnershipClassSourceConflict && !dropped {
			action = resilience.CollabActionDropUnprotected
		} else if !claimed && l.Body.Shape.ReasoningCiphertext && !probed[current] && l.MaxAttempts != 1 {
			action = resilience.CollabActionProbeReasoning
		} else if !switched && l.MaxAttempts != 1 {
			action = resilience.CollabActionSwitchOriginal
		} else if !dropped && l.MaxAttempts != 1 {
			action = resilience.CollabActionDropUnprotected
		} else {
			action = resilience.CollabActionReplaceAll
		}
		if action != resilience.CollabActionReplaceAll && optional >= resilience.CollabOptionalSendLimit {
			action = resilience.CollabActionReplaceAll
			named = ""
		}
		if action == resilience.CollabActionSwitchOriginal {
			switchedNamed = evidence.Named
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
			if replacements >= 2 {
				return false
			}
			prefer = l.First
			exclude = replaced
			if replaced[prefer] {
				prefer = 0
			}
		}
		account, release := l.Select(ctx, prefer, exclude)
		if account == 0 && prefer > 0 && (action == resilience.CollabActionDropUnprotected || action == resilience.CollabActionReplaceAll) {
			account, release = l.Select(ctx, 0, exclude)
		}
		if account == 0 {
			if action == resilience.CollabActionSwitchOriginal || action == resilience.CollabActionProbeReasoning || action == resilience.CollabActionDropUnprotected {
				continue
			}
			return false
		}
		if release == nil {
			release = func() {}
		}
		body := derived[account]
		if body == nil {
			body = l.Body.Original
		}
		next, changed := body, action == resilience.CollabActionSwitchOriginal
		if !changed {
			next, changed = l.Body.Derive(body, action, named, claimed)
		}
		if !changed {
			release()
			if action == resilience.CollabActionReplaceAll || action == resilience.CollabActionNamedRepair {
				return false
			}
			continue
		}
		if action == resilience.CollabActionReplaceAll {
			replacements++
			replaced[account] = true
		} else {
			optional++
		}
		if handled[account] == nil {
			handled[account] = map[string]bool{}
		}
		if action != resilience.CollabActionSwitchOriginal {
			for _, item := range l.Body.Items {
				path := "input." + strconv.Itoa(item.Index)
				if item.Identity != "" && gjson.GetBytes(body, path).Raw != gjson.GetBytes(next, path).Raw {
					handled[account][item.Identity] = true
				}
			}
			if named != "" {
				if namedFamilies[account] == nil {
					namedFamilies[account] = map[resilience.CollabFamily]bool{}
				}
				for _, item := range l.Body.Items {
					if item.Identity == named {
						namedFamilies[account][item.Family] = true
					}
				}
			}
		}
		derived[account] = next
		nextEvidence, delivered := func() (resilience.CollabEvidence, bool) { defer release(); return l.Send(ctx, account, next, action) }()
		if delivered {
			return true
		}
		if action == resilience.CollabActionSwitchOriginal && nextEvidence.Named == nil && nextEvidence.Grade == resilience.CollabEvidenceGradeVerified {
			nextEvidence.Named = switchedNamed
		}
		current, evidence = account, nextEvidence
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
