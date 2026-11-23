package resilience

// CollabEvidenceInput contains transient request/error bytes. It must never be
// persisted. WrappedStatus is a hint; it is not evidence until Inner.Validated.
type CollabEvidenceInput struct {
	Payload       []byte
	Message       string
	HTTPStatus    int
	WrappedStatus int
	Source        CollabEvidenceSource
	Event         string // raw SSE event, required to validate proxy.upstream_error
	Shape         CollabRequestShape
}

type CollabRequestShape struct {
	Kind                    CollabRequestKind
	Route                   CollabRoute
	Codex                   bool
	AgentCiphertext         bool
	ToolOutputCiphertext    bool
	ReasoningCiphertext     bool
	CompactionCiphertext    bool
	CallArgumentsCiphertext bool
	ContinuationPointer     bool
	Ambiguous               bool
	SemanticOutput          bool
}

// Identity is the original input item's identity, never a derived array index.
// Raw named identifiers remain transient until converted to a keyed digest.
type CollabNamedItem struct {
	InputIndex *int
	Path       string
	Identity   string
	Family     CollabFamily
	Located    bool
	Ambiguous  bool
	MatchCount int
	Item       CollabDigest
}

type CollabInnerError struct {
	Validated bool
	Status    int
	Code      string
	Type      string
	Message   string
	Payload   []byte
}

type CollabEvidence struct {
	Grade     CollabEvidenceGrade
	Family    CollabFamily
	Named     *CollabNamedItem
	Ownership CollabOwnershipClass
	Source    CollabEvidenceSource
	Inner     *CollabInnerError
}

type CollabEvidenceClassifier interface {
	Classify(CollabEvidenceInput) CollabEvidence
}

type NoopCollabEvidenceClassifier struct{}

func (NoopCollabEvidenceClassifier) Classify(in CollabEvidenceInput) CollabEvidence {
	return CollabEvidence{Grade: CollabEvidenceGradeUnclaimed, Family: CollabFamilyNone, Ownership: CollabOwnershipClassNone, Source: in.Source}
}

// The classifier extracts only the raw identifier. The controller resolves it
// against the immutable original input, before edits or account selection.
// Even two entries with the same digest are ambiguous; exactly one is required.
type CollabOriginalItem struct {
	Identity string
	Item     CollabDigest
	Family   CollabFamily
}

func ResolveCollabNamedItem(named CollabNamedItem, original []CollabOriginalItem) CollabNamedItem {
	named.Located = false
	named.MatchCount = 0
	named.Item = ""
	if named.Ambiguous {
		return named
	}
	if named.Identity == "" {
		return named
	}
	for _, item := range original {
		if item.Identity == named.Identity {
			named.MatchCount++
			named.Item = item.Item
			named.Family = item.Family
		}
	}
	named.Located = named.MatchCount == 1
	named.Ambiguous = named.MatchCount > 1
	if !named.Located {
		named.Item = ""
	}
	return named
}
