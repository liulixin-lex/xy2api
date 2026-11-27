package resilience

// CollabDigest is a keyed, domain-separated digest, never raw content or an ID.
type CollabDigest string

type CollabSendSequence uint64
type CollabGeneration uint64

const (
	// Writers and the expanded admin representation remain v2 until all readers
	// have deployed support for sparse v3 documents.
	CollabObservationVersion       = 2
	collabObservationSparseVersion = 3
	CollabObservationMaxBytes      = 32 << 10
	CollabObservationMaxSends      = 16
	CollabObservationMaxDeliveries = 64
	CollabObservationMaxEvents     = 32
	CollabObservationMaxHints      = 8
)

// Pointer counts distinguish unobserved (null) from observed zero. This is the
// shared persisted document; request bytes and raw identifiers live elsewhere.
type CollabObservation struct {
	// Counts includes deduplicated observations even when their detail is trimmed.
	// Untracked occurrences are excluded because uniqueness could not be checked.
	Untracked            CollabUntrackedCounts       `json:"untracked"`
	Counts               *CollabObservationCounts    `json:"counts"`
	DeliveriesTotal      int                         `json:"deliveries_total"`
	EventsTotal          int                         `json:"events_total"`
	EventsOverflow       int                         `json:"events_overflow"`
	HintsTotal           int                         `json:"hints_total"`
	HintsOverflow        int                         `json:"hints_overflow"`
	V                    int                         `json:"v"`
	Thread               CollabDigest                `json:"thread"`
	Tree                 CollabDigest                `json:"tree"`
	Parent               CollabDigest                `json:"parent"`
	Role                 CollabRole                  `json:"role"`
	RequestKind          CollabRequestKind           `json:"request_kind"`
	Route                CollabRoute                 `json:"route"`
	Client               CollabClient                `json:"client"`
	VersionState         CollabVersionState          `json:"version_state"`
	ClientVersion        string                      `json:"client_version"`
	Covered              bool                        `json:"covered"`
	InitialReason        CollabReason                `json:"initial_reason"`
	StopReason           CollabReason                `json:"stop_reason"`
	FinalMode            CollabFinalMode             `json:"final_mode"`
	Sends                []CollabSendObservation     `json:"sends"`
	SendTotal            int                         `json:"send_total"`
	SendOverflow         int                         `json:"send_overflow"`
	Mixed                bool                        `json:"mixed"`
	Deliveries           []CollabDeliveryObservation `json:"deliveries"`
	DeliveriesOverflow   int                         `json:"deliveries_overflow"`
	Replacements         CollabReplacementCounts     `json:"replacements"`
	LocalCount           int                         `json:"local_count"`
	Execution            CollabExecution             `json:"execution"`
	Events               []CollabEventObservation    `json:"events"`
	InputTransformations *CollabInputTransformations `json:"input_transformations"`
	Hints                []CollabHintObservation     `json:"hints"`
	Truncated            bool                        `json:"truncated"`
}

type CollabSendObservation struct {
	Seq           CollabSendSequence   `json:"seq"`
	Generation    CollabGeneration     `json:"generation"`
	Account       CollabDigest         `json:"account"`
	Action        CollabAction         `json:"action"`
	Result        CollabSendResult     `json:"result"`
	RawPlan       CollabPlanOutcome    `json:"raw_plan"`
	RawReason     CollabPlanReason     `json:"raw_reason"`
	Plan          CollabPlanOutcome    `json:"plan"`
	PlanReason    CollabPlanReason     `json:"plan_reason"`
	Eligibility   CollabEligibility    `json:"eligibility"`
	RestoreError  CollabRestoreError   `json:"restore_error"`
	CallCount     *int                 `json:"call_count"`
	Marker        CollabMarkerCounts   `json:"marker"`
	MarkerReasons []CollabMarkerReason `json:"marker_reasons"`
}

type CollabMarkerCounts struct {
	UpstreamEmpty  *int `json:"upstream_empty"`
	DeliveredEmpty *int `json:"delivered_empty"`
	Add            *int `json:"add"`
	Keep           *int `json:"keep"`
	Veto           *int `json:"veto"`
}

type CollabDeliveryObservation struct {
	Delivery CollabDigest          `json:"delivery"`
	Item     CollabDigest          `json:"item"`
	Digest   CollabDigest          `json:"digest"`
	Position CollabPosition        `json:"position"`
	Form     CollabForm            `json:"form"`
	Kind     CollabMessageKind     `json:"kind"`
	Class    CollabItemClass       `json:"class"`
	Outcome  CollabDeliveryOutcome `json:"outcome"`
	Evidence CollabEvidenceGrade   `json:"evidence"`
	Stage    CollabStage           `json:"stage"`
	Scope    CollabScope           `json:"scope"`
	Bytes    int64                 `json:"bytes"`
}

type CollabReplacementCounts struct {
	Task       int `json:"task"`
	Message    int `json:"message"`
	ToolOutput int `json:"tool_output"`
	Reasoning  int `json:"reasoning"`
	Compaction int `json:"compaction"`
	CallArgs   int `json:"call_args"`
}

type CollabEventObservation struct {
	Event    CollabDigest      `json:"event"`
	Delivery CollabDigest      `json:"delivery"`
	Source   CollabEventSource `json:"source"`
}

type CollabHintObservation struct {
	Kind       CollabHintKind    `json:"kind"`
	Selectable bool              `json:"selectable"`
	IsCurrent  bool              `json:"is_current"`
	Cooldown   CollabCooldown    `json:"cooldown"`
	Attempt    CollabHintAttempt `json:"attempt"`
}

type CollabInputTransformations struct {
	Counts map[CollabTransformationReason]int `json:"counts"`
}

// Implementations seal each send once and freeze a deep copy before billing.
// Capping/truncation belongs to the observation implementation, not forwarding.
type CollabObserver interface {
	SealSend(CollabSendObservation) bool
	ObserveDelivery(CollabDeliveryObservation)
	Freeze() *CollabObservation
}

type NoopCollabObserver struct{}

func (NoopCollabObserver) SealSend(CollabSendObservation) bool       { return false }
func (NoopCollabObserver) ObserveDelivery(CollabDeliveryObservation) {}
func (NoopCollabObserver) Freeze() *CollabObservation                { return nil }

// Classified totals have fixed enum keys. Nullable call/marker totals sum only
// observed counts; nil means no send observed that field. Counts=nil identifies
// older documents whose discarded detail cannot be reconstructed.
type CollabObservationCounts struct {
	Actions           map[CollabAction]int          `json:"actions"`
	Results           map[CollabSendResult]int      `json:"results"`
	RawPlans          map[CollabPlanOutcome]int     `json:"raw_plans"`
	RawReasons        map[CollabPlanReason]int      `json:"raw_reasons"`
	Plans             map[CollabPlanOutcome]int     `json:"plans"`
	PlanReasons       map[CollabPlanReason]int      `json:"plan_reasons"`
	Eligibility       map[CollabEligibility]int     `json:"eligibility"`
	RestoreErrors     map[CollabRestoreError]int    `json:"restore_errors"`
	MarkerReasons     map[CollabMarkerReason]int    `json:"marker_reasons"`
	DeliveryPositions map[CollabPosition]int        `json:"delivery_positions"`
	DeliveryForms     map[CollabForm]int            `json:"delivery_forms"`
	DeliveryKinds     map[CollabMessageKind]int     `json:"delivery_kinds"`
	DeliveryClasses   map[CollabItemClass]int       `json:"delivery_classes"`
	DeliveryOutcomes  map[CollabDeliveryOutcome]int `json:"delivery_outcomes"`
	DeliveryEvidence  map[CollabEvidenceGrade]int   `json:"delivery_evidence"`
	DeliveryStages    map[CollabStage]int           `json:"delivery_stages"`
	DeliveryScopes    map[CollabScope]int           `json:"delivery_scopes"`
	EventSources      map[CollabEventSource]int     `json:"event_sources"`
	HintKinds         map[CollabHintKind]int        `json:"hint_kinds"`
	HintCooldowns     map[CollabCooldown]int        `json:"hint_cooldowns"`
	HintAttempts      map[CollabHintAttempt]int     `json:"hint_attempts"`
	CallCount         *int                          `json:"call_count"`
	Marker            CollabMarkerCounts            `json:"marker"`
	DeliveryBytes     int64                         `json:"delivery_bytes"`
	HintSelectable    int                           `json:"hint_selectable"`
	HintIsCurrent     int                           `json:"hint_is_current"`
}

// Untracked counts observation occurrences, not unique sends or deliveries.
// It is separate from detail overflow and must not enter delivery reconciliation.
type CollabUntrackedCounts struct {
	Sends           int `json:"sends"`
	Deliveries      int `json:"deliveries"`
	Events          int `json:"events"`
	Transformations int `json:"transformations"`
}
