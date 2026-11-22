# Collaboration foundation contracts

These foundations leave forwarding on its existing path. Eight settings default
false/off except `records=true`. All honor the master switch; `lossless_first`,
`record_routing` and `local_ending` additionally require `ladder`.

Settings freeze once at ingress by retaining the immutable published pointer.
`CollabSettingsSnapshot` is read-only, like `Current`; clone explicitly before
mutation. It allocates nothing. A nil state returns nil, whose accessors are off.
Stored documents tolerate future fields and repair only unusable collaboration
fields to defaults, counted by `CollabSafetySnapshot`. Admin PUT first calls
`checkCollabPatch`: unknown fields, nulls and invalid modes remain errors;
omissions retain current values and an empty groups array clears the whitelist.

Paths below are relative to `backend/internal/`.

| Foundation | Symbols and file | Implementation owner |
| --- | --- | --- |
| Evidence | `CollabEvidenceClassifier`, `ResolveCollabNamedItem` in `resilience/collab_evidence.go` | Evidence and controller stream |
| Controller | `CollabController`, `CollabCandidate`, `CollabOptionalPolicy` in `resilience/collab_controller.go`; attempt gates in `collab_attempt.go` | Evidence and controller stream |
| Records | `CollabRecordsStore`, `CollabSharedRecordsStore` in `resilience/collab_records.go`; safe adapter in `collab_records_safe.go`; `CollabDigest` in `service/openai_collab_contracts.go` | Records and routing stream |
| State and observation | `CollabRequestState` in `resilience/collab_state.go`; `CollabObservation` in `collab_observation.go`; safe observer in `collab_safety.go` | Observation stream; controller owns transient state |
| Shared plan | `CollabPlan`, `CollabToolState` in `service/openai_collab_contracts.go` | Request planning and response restoration streams |
| Delivery | `CollabDeliveryHooks` in `resilience/collab_delivery.go` | Response restoration stream; records, affinity and local completion consumers |
| Scheduling | `CollabScheduler`, `CollabThreadPolicy` in `resilience/collab_scheduling.go` | Records/routing and tree affinity streams |
| Settings | `Settings.Collab`, accessors in `resilience/collab_settings.go`; snapshot in `collab_state.go` | Preparation; frontend consumes JSON |

## Ordering and admission

With ladder enabled, `CollabOrderLegacyFirst` means today's repair runs first,
unchanged: same accounts, bytes, ordering, quotas, learning, admission and time.
Only its error endpoint, or newly recognized ciphertext evidence it does not
claim, calls `TakeOver`. Capture `CollabSavedTerminal` before the existing terminal
commits: original failure object, status, cloned headers, retry advice, account,
rejection evidence and retryability. Keep it in `CollabLegacyEndpoint.Saved` and
return its original failure/headers/advice if takeover fails. Never omit a legacy send because an
optional send used the same bytes. Local completion replaces only that endpoint.

`lossless_first` opts into `CollabOrderLosslessFirst`: optional work precedes
legacy repair. It has residual risks from semantic output, upstream shared quota
and concurrency; it is not the default. Restore elapsed optional time to legacy
with `CollabSlotPolicy.LegacyCompensation`, measured across selection, reads,
sending and cancellation. Do not enter if compensation would exceed client idle
headroom. Explicit presence flags distinguish absent deadlines from exhausted
ones. Lossless-first has one 60-second cap for the entire optional phase, including
cancellation. In the default order, the deadline to an upstream acceptance signal
is 15 seconds for a retryable saved error, otherwise 60 seconds. Earlier budget
and client-idle deadlines still win. Do not renew the deadline per action, and do
not treat a mere first byte/error frame as acceptance. No headers or heartbeats
may commit while taking over; accepted output resumes normal delivery. The
optional send quota remains three. User-concurrency waiters prevent entry and
must end an active optional phase promptly.

Hold the first slot only with spare capacity and no waiters. Recheck throughout
optional work; a waiter, lost capacity or revoked hold stops lossless-first and
resumes legacy. Optional targets are immediate-slot-only. The default order can
proceed without holding the first account. Hard admission restrictions remain.

`NextStep` requests account selection using preferences (possibly empty),
exclusions and `ImmediateOnly`. The handler admits a target before calling
`AccountAdmitted`. Only then plan from original input plus that account's records;
never derive a switched account's body from the previous account's body. The
current body can serve same-account repairs. Adopt before `CommitOnAdopt`, reserve
before send, confirm only an actual send, release an unsent reservation, and
settle once by sequence. Send history contains only a keyed body digest. Retain
only the immutable original and current body; drop candidate bytes after use.

The classifier extracts a raw named identifier. The controller calls
`ResolveCollabNamedItem` against original items before any edits. Exactly one
matching original item is required; zero or multiple matches are unlocatable.
Repeatedly named items reach their own endpoint, not unrelated replacements.
Existing confirmed-rejection learning uses `CollabLearningConfirmedRejection`
and retains pre-send timing. New replacement state uses
`CollabLearningReplacementAccepted` and commits only after acceptance.
`CommitReady` distinguishes them and excludes optional probes from old learning.

## Safety, records and delivery

Use `SafeCollabObserver` at observation call sites. `SetCollabState` wraps its
observer automatically. Every delivery consumer is recovered independently;
later consumers still run. Panic values and payloads never enter counters/logs.

Hooks receive bytes separately, copy at most once into an immutable string, and
share that string among consumers. No consumers means no allocation. Deltas and
unrelated/image items carry metadata only; payload is retained only for an
executable call's completion/final frames. Pass `Executable=true` only when a
consumer needs that call snapshot. Registration uses copy-on-write; callbacks
run outside its lock. Consumers must do bounded local work, never network I/O.

Construct one `SafeCollabRecordsStore` per service and close it at shutdown.
Its local implementation must be in-process and bounded. Writes mutate local
state synchronously, then enqueue shared persistence without waiting. A single
background writer preserves order, uses short timeouts, recovers panics and
counts errors. The bounded queue drops excess shared writes; local success is
not converted into a delivery error. Shared writes must be idempotent; confirm
without pending cannot create evidence, and voids invalidate pending tokens.
Local lookup results must include tombstones to override stale shared state.

At the first read, use `WithCollabRecordReadBudget` once and retain its context
for the request. Build one batch for all items/accounts, dispositions and the
tree counter. The adapter permits at most one shared batch per request under
one 250ms deadline, including nested contexts. Later lookups use local state.
At most four shared readers run per store; even a reader ignoring cancellation
cannot block the request past its deadline or create unbounded workers. Shared
failures are absence. Disposition flags remain independent of the records flag.
Record wrappers recover every local/shared call. No wrapper logs error contents.

Terminal-rejection history is a disposition independent of the records switch.
`CollabTerminalRejectionCounter` is an optional bounded local component: it keys
by full delivery identity plus rejection digest, deduplicates terminal event
digests, saturates at two and expires within the record TTL. Full capacity means
absence. Integrate its `Record` and `Counts` into the local store's
`TerminalRejected` and `LookupBatch`. The safe adapter queues the mutation to the
shared store only after local success. A shared replay uses the same event digest.
The current request is not part of the prior-count snapshot used for eligibility.

After all other local-ending gates pass, `CollabLocalEndingPermitted` allows a
non-retryable saved error immediately. For a retryable error, it requires two
prior terminal rejections matching the exact delivery and rejection digest, still
unexpired. New items with identical content inherit nothing. Do not mark the
delivery terminated, or increment the local-ending counter, until notice delivery
succeeds. `CollabNamedNextStep` sends a handled named item to its own endpoint;
zero/multiple matches stay unlocatable and never enter the unnamed branch.

## Main-agent integration

- `handler/openai_gateway_handler.go`: choose the frozen order, preserve the
  legacy endpoint before terminal commitment. Claim persistent effects once at
  the original boundary (including when takeover is skipped); optional failures
  never claim them. Defer delivery-only effects with `ResolveOutcome` and
  `ClaimDeliveryEffects` until the final result. Handle selection requests,
  stop for user waiters, recheck revocable holds, and use
  `CollabAttemptFrom` before failure, cooldown, avoidance or binding effects.
- `service/openai_gateway_forward.go`: attach `WithCollabAttempt` to each actual
  attempt; optional sends cannot use legacy repair quotas or confirmed-rejection
  learning. Allocate a new physical response generation; retain body digests.
- `service/openai_gateway_response_handling.go`: notify all four hook phases with
  the generation/sequence. A failed write cannot confirm issuance or a lease.
  Invoke observations/records only through safe adapters. Preserve original
  failure identity on optional failure. Check `CustomerSettlementAllowed` before
  customer charges; optional sends never settle or debit the customer. The
  ingress request's existing owner performs the single final settlement.
- `service/openai_collaboration_plaintext.go`: use the frozen settings for new
  gates; share the restorer within a generation; migrate the legacy plan to
  `CollabPlan` jointly with request and response implementations.
- Scheduling: `CollabThreadPolicy` requests thread-only avoidance at today's
  rejection timing and thread-only routing to the recovered account on success.
  This does not wait for final failure. Optional attempts make neither change.
  `CollabTreeHintUsable` requires an immediate slot; otherwise use normal
  selection. Borrowing preserves both admission and success bindings. Quality
  migration suppression and queue reselect remain separate flags.
  Lease suppression additionally uses `CollabLeaseMigrationSuppressionOn`
  (tree-affinity plus lossless-first); redispatch borrowing uses
  `CollabRedispatchBorrowOn` (record-routing plus lossless-first). Failover
  candidate ordering still uses `CollabRecordRoutingOn` without lossless-first.
- Before billing ownership is claimed, safely freeze/deep-copy the bounded
  observation. Records/observers never turn their failures into request errors.

## Signature migration

Changed or removed public signatures:

- `(*RequestState).CollabSettingsSnapshot() Settings` becomes `() *Settings`.
- `CollabOptionalPolicy(time.Duration)` becomes `(CollabOptionalBudget)`; return
  type remains `CollabSlotPolicy`.
- `(*CollabDeliveryHooks).Received`, `AboutToWrite`, `Written`, `Completed` each
  become `(context.Context, CollabDeliveryEvent, []byte)`; consumer methods retain
  their existing signatures, but `CollabDeliveryEvent.Payload` becomes `string`.
- `CollabRecordsStore.LookupRecords`, `LookupDispositions` and
  `TreeLocalEndingCount` are replaced by
  `LookupBatch(context.Context, CollabRecordBatch) CollabRecordBatchResult`.
  `NoopCollabRecordsStore` implements the same replacement. Both add
  `TerminalRejected(context.Context, CollabTerminalRejectionRecord) (int, bool)`.
- `CollabController` and `NoopCollabController` add
  `TakeOver(CollabLegacyEndpoint) bool` and
  `AccountAdmitted(CollabAdmittedAccount) bool`.
- `CollabScheduler` and `NoopCollabScheduler` add
  `BindRecoveredThread(context.Context, CollabThreadRoute) bool` and
  `LookupThreadRoute(context.Context, CollabRecordScope, CollabDigest) (CollabThreadRoute, bool)`.

New public functions/methods:

- `CheckCollabSettingsJSON([]byte) error`; `(*Settings).CollabLosslessFirstOn() bool`;
  `(*Settings).CollabOrder() CollabOrder`;
  `(*Settings).CollabLeaseMigrationSuppressionOn() bool`;
  `(*Settings).CollabRedispatchBorrowOn() bool`.
- `CollabSafetySnapshot() CollabSafetyCounts`;
  `SafeCollabObserver` implements unchanged `CollabObserver` signatures.
- `NewSafeCollabRecordsStore(CollabRecordsStore, CollabSharedRecordsStore, int) *SafeCollabRecordsStore`;
  `(*SafeCollabRecordsStore).Close()` plus all revised `CollabRecordsStore` methods;
  `WithCollabRecordReadBudget(context.Context) (context.Context, context.CancelFunc)`.
- `CollabSharedRecordsStore.LookupBatch(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error)`;
  `CollabSharedRecordsStore.Apply(context.Context, CollabRecordMutation) error`.
- `WithCollabAttempt(context.Context, CollabAttempt) context.Context`;
  `CollabAttemptFrom(context.Context) CollabAttempt`;
  `(CollabAttempt).AccountEffectsAllowed() bool`, `.CustomerSettlementAllowed() bool`.
- `(CollabSlotPolicy).LegacyCompensation(time.Duration) time.Duration`;
  `(CollabLearning).CommitReady(accepted, optional bool) bool`.
- `ResolveCollabNamedItem(CollabNamedItem, []CollabOriginalItem) CollabNamedItem`;
  `CollabTreeHintUsable(CollabTreeHint, bool) bool`;
  `CollabThreadPolicy(CollabRole, CollabAttempt, bool, bool) CollabThreadRoutingPolicy`.
- `NewCollabSavedTerminal(CollabTerminalSnapshot) *CollabSavedTerminal`;
  `(*CollabSavedTerminal).ClaimPersistentEffects() bool`,
  `.ResolveOutcome(CollabTerminal) bool`, `.ClaimDeliveryEffects() bool`.
- `CollabLocalEndingPermitted(*CollabSavedTerminal, CollabDeliveryKey, CollabDigest, []CollabTerminalRejectionCount, time.Time) bool`;
  `CollabNamedNextStep(CollabNamedItem, map[CollabDigest]bool) CollabNamedNext`.
- `NewCollabTerminalRejectionCounter(int) *CollabTerminalRejectionCounter`;
  `(*CollabTerminalRejectionCounter).Record(CollabTerminalRejectionRecord, time.Time) (int, bool)`;
  `(*CollabTerminalRejectionCounter).Counts(CollabDeliveryKey, time.Time) []CollabTerminalRejectionCount`.

Data contract changes: `CollabSettings.LosslessFirst`; explicit order/deadline/
compensation/slot policy fields; `CollabNextStep.Selection` and `Saved`;
`CollabCandidateInput.Records`; `CollabSettlement.Optional`/`Abandoned`;
`CollabRequestState.Order`/`LegacyEndpoint`/`CurrentBody` replace archived derived
bodies and final bytes; `CollabSendState.BodyDigest` replaces `Body`;
`CollabLearning.Timing`; `CollabNamedItem.MatchCount`/`Item`;
`CollabSchedulingFlags.TreeHintImmediateOnly`; typed body and rejection digest domains; `CollabDispositionLookup.TerminalRejections`;
`CollabRecordMutation.TerminalRejection`; saved-terminal and user-waiter policy fields.

## Observation aggregate additions

CollabObservation now includes Counts (*CollabObservationCounts), DeliveriesTotal,
EventsTotal, EventsOverflow, HintsTotal and HintsOverflow. The counts document
preserves all fixed-enum classifications, nullable call/marker sums, delivery
bytes, and selectable/current hint totals before item or byte caps. Overflow
details remain unavailable to per-delivery reconciliation. These additive fields
retain observation version 2; older truncated records can have Counts=nil.

The existing single/batch usage and unbilled inserts bind canonical observation
JSON directly. Invalid UTF-8, NUL escapes, invalid surrogate pairs, excessive
depth and malformed optional documents bind NULL. Read-back allows 128 KiB of
formatting overhead, then enforces 32 KiB on the canonical document. No second
database write or asynchronous queue participates. Billing freeze unwraps the
standard SafeCollabObserver only for the local bounded accumulator.
