package resilience

// CollabDigest is a keyed, domain-separated digest, never raw content or an ID.
type CollabDigest string

type CollabSendSequence uint64
type CollabGeneration uint64

const (
	CollabObservationVersion       = 2
	CollabObservationMaxBytes      = 32 << 10
	CollabObservationMaxSends      = 16
	CollabObservationMaxDeliveries = 64
	CollabObservationMaxEvents     = 32
	CollabObservationMaxHints      = 8
)

// Pointer counts distinguish unobserved (null) from observed zero. This is the
// shared persisted document; request bytes and raw identifiers live elsewhere.
type CollabObservation struct {
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
