package service

import "github.com/liulixin-lex/xy2api/internal/resilience"

type collabEditClass uint16

const (
	collabEditReasoning collabEditClass = 1 << iota
	collabEditReplaceableAgent
	collabEditReplaceableTool
	collabEditUnreadAgent
	collabEditUnreadTool
	collabEditProtectedAgent
	collabEditProtectedTool
	collabEditCompaction
	collabEditBodies      = collabEditReplaceableAgent | collabEditReplaceableTool | collabEditUnreadAgent | collabEditUnreadTool | collabEditProtectedAgent | collabEditProtectedTool
	collabEditUnprotected = collabEditReasoning | collabEditReplaceableAgent | collabEditReplaceableTool | collabEditUnreadAgent | collabEditUnreadTool
	collabEditAll         = collabEditReasoning | collabEditBodies | collabEditCompaction
)

// Stop reasons are part of the permission decision, including at handler entry.
type CollabLadderStop uint8

const (
	CollabLadderSelecting CollabLadderStop = iota
	CollabLadderFinalDisposal
	CollabLadderHandledAgain
	CollabLadderFallback
	// Final disposal row (c): time, admission, or response continuation unavailable.
	CollabLadderDeliveryUnavailable
)

// The table is applied before names, quotas, selection or local-ending entry.
// Named verified edits intersect the named identity; invalid names select the
// empty row. Call arguments have no edit bit, so no evidence can authorize them.
type CollabLadderPolicy struct {
	eligible, switchOnly, conservative bool
	LocalEnding                        bool
	named, probe, drop, final          collabEditClass
}

var collabPolicyTable = [...]CollabLadderPolicy{
	{},                                 // unclaimed, capacity, continuation, or unlocatable
	{eligible: true, switchOnly: true}, // unknown owner, or ownership without agent ciphertext
	{eligible: true, conservative: true, drop: collabEditUnprotected & collabEditBodies, final: collabEditBodies},                                 // claimed, with or without a name
	{eligible: true, LocalEnding: true, named: collabEditAll, probe: collabEditReasoning, drop: collabEditUnprotected, final: collabEditAll},      // verified, named or unnamed
	{eligible: true, LocalEnding: true, named: collabEditAll, probe: collabEditReasoning, drop: collabEditReplaceableAgent, final: collabEditAll}, // verified source conflict
}

func (p CollabBody) LadderPolicy(e resilience.CollabEvidence, stop ...CollabLadderStop) CollabLadderPolicy {
	e = p.ResolveEvidence(e)
	row := 0
	switch {
	case p.Shape.Ambiguous || p.Shape.ContinuationPointer || e.Named != nil && e.Named.Ambiguous,
		e.Ownership == resilience.CollabOwnershipClassCapacity || e.Ownership == resilience.CollabOwnershipClassContinuation:
	case e.Grade == resilience.CollabEvidenceGradeClaimed:
		if p.Shape.AgentCiphertext || p.Shape.ToolOutputCiphertext {
			row = 2
		}
	case e.Grade == resilience.CollabEvidenceGradeVerified:
		switch {
		case e.Ownership == resilience.CollabOwnershipClassUnknownSuffix || e.Family == resilience.CollabFamilyOwnership && !p.Shape.AgentCiphertext:
			row = 1
		case e.Ownership == resilience.CollabOwnershipClassSourceConflict:
			row = 4
		default:
			row = 3
		}
	}
	policy := collabPolicyTable[row]
	reason := CollabLadderSelecting
	if len(stop) > 0 {
		reason = stop[0]
	}
	policy.LocalEnding = policy.LocalEnding && (reason == CollabLadderFinalDisposal || reason == CollabLadderDeliveryUnavailable)
	// An ordinary failure of an optional action returns to the existing
	// final repair. It supplies no evidence for learning or local ending.
	if reason == CollabLadderFallback && (e.Grade == resilience.CollabEvidenceGradeUnclaimed || e.Grade == "") && !p.Shape.Ambiguous && !p.Shape.ContinuationPointer {
		policy = CollabLadderPolicy{eligible: true, final: collabEditAll}
	}
	return policy
}
func (p CollabLadderPolicy) edits(action resilience.CollabAction) collabEditClass {
	switch action {
	case resilience.CollabActionNamedRepair:
		return p.named
	case resilience.CollabActionProbeReasoning:
		return p.probe
	case resilience.CollabActionDropUnprotected:
		return p.drop
	case resilience.CollabActionReplaceAll:
		return p.final
	}
	return 0
}
func (p CollabLadderPolicy) permits(action resilience.CollabAction) bool {
	return p.eligible && (action == resilience.CollabActionSwitchOriginal || p.edits(action) != 0)
}
func (p CollabLadderPolicy) derivation(body CollabBody, action resilience.CollabAction) CollabBody {
	items := make([]CollabBodyItem, 0, len(body.Items))
	for _, item := range body.Items {
		var class collabEditClass
		switch item.Family {
		case resilience.CollabFamilyReasoning:
			class = collabEditReasoning
		case resilience.CollabFamilyCompaction:
			class = collabEditCompaction
		case resilience.CollabFamilyAgentMessage, resilience.CollabFamilyToolOutput:
			class = collabEditProtectedAgent
			if item.Class == resilience.CollabItemClassReplaceable {
				class = collabEditReplaceableAgent
			}
			if item.Class == resilience.CollabItemClassUnreadMessage {
				class = collabEditUnreadAgent
			}
			if item.Family == resilience.CollabFamilyToolOutput {
				class <<= 1
			}
		}
		if p.edits(action)&class != 0 {
			items = append(items, item)
		}
	}
	body.Items = items
	return body
}
