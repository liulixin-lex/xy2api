# Collaboration observation integration

The helpers add no repair, routing, moderation or billing decision. All
collaboration switches leave the observation rules unchanged. The shared
records switch controls routing records, not this optional request document.

## Request and send wiring

1. Before route dispatch, call resilience.InspectCollabIngress(originalBody)
   and, for a recognized Codex client, CountCollabIngress with the route and
   request kind. The inventory reads declarations and encrypted input without
   modifying bytes. Invalid JSON returns Observed=false; valid requests have no
   size cutoff. The parser uses a temporary read-only string view of the byte
   slice and retains no body or substring after returning.
   Use ParseCollabClient on the original leading User-Agent token; do not use
   a gateway or fallback version.
2. The owner of CollabRequestState creates one
   NewCollabObservationAccumulator(seed, ingress.Persist(takeoverAdopted))
   and puts it in Observer. The seed contains keyed digests, fixed enums,
   parsed version and coverage. Takeover adoption must create the observer
   even for reasoning-only, compaction-only and zero-send requests.
   Preserve this instance across forwarding calls and account changes. Reset the
   collaboration state at each new WebSocket logical turn before attaching its
   accumulator; a previous turn's frozen observer must never be reused.
3. In the forwarding service, count CollabSignalForwardCall once per forward
   call. At the end of each physical send, call SealSend with its unique send
   sequence, physical generation, account digest, action/result, separate raw
   and normalized plan reasons, eligibility and restoration error. Obtain
   these facts from each component's typed results, never by casting error text.
   Call counts and marker counts remain nil until their respective stage was
   observed; observed zero gets a pointer to zero. Only confirmed physical sends
   enter send_total; canceled reservations must use not_sent.
4. Response handling supplies upstream marker counts; the seam writer supplies
   delivered marker counts from the exact final bytes after write and flush. Do not infer delivered
   markers from a planned write or an upstream marker. Report each actual
   delivery transition once; unchanged historical replays have no new entry.
   Call ObserveEvent only with the producer's verified source classification.
   An occurrence outside the registered parent's child-completion notice uses
   seen_other, which cannot confirm notification.
5. Classify input-removal reports with ClassifyCollabTransformationReason,
   then pass typed counts to ObserveInputTransformations once per physical
   generation. Nil means no upstream report; an empty map means an observed
   report containing zero. Do not pass upstream strings as map keys.
   ObserveHint accepts classified routing facts and cannot change selection.
6. After all sends and deliveries settle, call Finish with initial/stop
   reasons, final mode, replacement counts, local count and execution state.
   This must precede the handler's first gatewayBillingContext call.
   ClaimGatewayRequest now freezes the bounded implementation before claiming
   ownership. Unknown observer implementations are omitted: their callbacks
   never execute under the billing lock.
7. ShouldLogCollabObservation implements full logging for failures, calls and
   encrypted input, plus one in sixteen other successes. Supply an unbiased
   request-local sample value. Log only the sanitized frozen document, through
   an existing nonblocking log path. Replace the old collaboration metric label
   containing model=; new counters accept only fixed enums and no model.
   Count the fixed CollabObservationSignal values only from verified facts.

The main integration owns the gateway forward, response handling, OpenAI
handler and collaboration plaintext wiring files. This change does not edit
those four files.

## Persistence and read boundaries

GatewayRequestMetadata carries an independently owned bounded JSON snapshot.
Billed usage, durable billing intents and unbilled requests retain that snapshot.
The existing hand-written single and batch INSERT statements bind the validated
document, or SQL NULL when absent or invalid. There is no observation UPDATE,
extra transaction, worker, queue, commit hook or unused write-loss counter.

Normalization checks UTF-8, NUL escapes, Unicode surrogate pairs and JSON depth
before decoding, including unknown fields. It canonicalizes the typed document
and enforces the 32 KiB encoded limit. Read-back accepts up to 128 KiB of formatted
input because PostgreSQL JSONB text includes spacing absent from compact JSON;
the canonical stored/output document still has the same 32 KiB limit.

The nullable additive migration has no default, backfill, index or constraint;
it sets local lock (1 second) and statement (5 seconds) timeouts, then takes
both table locks with NOWAIT before modifying either. An older binary
can continue using its explicit column lists. Local PostgreSQL execution is a
separate integration check.

Admin merged-query scans handle both tables and ignore malformed optional
documents. The normal user DTO and both export projections exclude the column.
ReconcileCollabDelivery takes records already scoped to one tenant and one
delivery; it gives accepted original content priority over loss, confirms only
matching issued/parent-notice events, ages pending notices after 24 hours, and
reports later loss of previously accepted history separately. Overflow detail
is not invented or included in reconciliation.

## Aggregate contract

The document adds counts, deliveries_total, events_total, events_overflow,
hints_total and hints_overflow. CollabObservationCounts has fixed-enum buckets
for send action/result, raw and normalized planning, eligibility, restoration
errors, marker reasons, delivery position/form/kind/class/outcome/evidence/stage/
scope, event source, and hint kind/cooldown/attempt. It also carries nullable
call/marker sums, delivery byte totals, and selectable/current hint totals.

Every accepted observation updates its totals before detail caps. Detail
retention remains 16 sends, 64 deliveries, 32 events and 8 hints; byte trimming
updates each corresponding overflow. Counts and totals survive both forms of
truncation, including more than 4,096 actual sends or transformation reports.
Deduplication is independent of retained detail and lasts for the request.
Repeated sends/generations, unchanged deliveries and repeated events do not
inflate totals. Request-local deduplication stores identities/fixed facts only.

A nil count means no send observed that count; zero is an observed zero.
Aggregate call/marker values sum the observed sends. Older truncated documents
without aggregate counts keep counts=null rather than inventing the lost data.
Both the accumulator and read-back clone aggregate maps and count pointers.

SetCollabState wraps observers with SafeCollabObserver. Billing recognizes that
wrapper around the local accumulator without calling arbitrary implementations.
The four main wiring files and replay goldens remain owned by the integrator.
The seam replaces the earlier controller handoff; no observation helper depends
on its candidate or saved-terminal claim methods.

The upstream input-removal report's transport shape and the producer's raw
planning/eligibility/restoration reason mappings must be supplied by their
owning streams. These helpers intentionally do not derive protocol evidence from
content appearance or substitute a final outcome for the original plan reason.
