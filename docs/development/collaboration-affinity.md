# Collaboration tree affinity integration

The scheduler changes are dormant until ingress installs the affinity context.
The four forwarding/handler wiring files are intentionally unchanged in this
stream. Existing public foundation signatures are unchanged.

## Lifetime and identity

Create one `resilience.NewCollabTreeAffinity(capacity, stickyTTL)` per gateway
service, using the existing `openAIWSSessionStickyTTL()` value. Do not create it
per request. It is bounded and local, needs no worker or shutdown hook, and
implements the existing `CollabScheduler` contract. Its embedded no-op ordering
and borrowing methods must not replace the record-routing implementation.

Freeze settings and identity from the original ingress request. Use keyed
digests from `OpenAIGatewayService.CollabDigest`, keeping the existing tenant and
effective group isolation. Use the same `CollabRecordScope` as delivery records:
`CollabDigest` already versions its domains with `collab-v1`, and the independent
affinity store keeps these keys separate from existing sticky/cache keys.
Both `identity.Thread` and `identity.Parent` must use the **thread** digest
domain; a parent-domain digest cannot match a membership's thread key.
Do not change session, prompt-cache, or quality keys. Missing digest material or
ambiguous identity disables the hint. For a child whose root is unknown, pass
an empty tree: membership can resolve it from the child or its parent. Without
membership there is no hint, and ordinary routing still runs.

Before selection, retain the context returned by:

```go
ctx = service.WithOpenAICollabTreeAffinity(
    ctx, affinityStore, frozenSettings, effectiveGroupID,
    identity, role, hasEncryptedState,
)
```

`identity` is a `resilience.CollabTreeHint` with the keyed scope, thread, parent,
and optional expiry; its account/status fields are ignored here. Group changes
must install a matching context; the selector ignores a mismatched context.

## Admission, rejection, success

In `handler/openai_gateway_handler.go`, after the existing slot acquisition and
final admission check, before `Forward` can write any executable call, call:

```go
service.NoteOpenAICollabAdmission(ctx, account.ID, borrowed)
```

A root publishes its provisional hint; child admissions register membership and
renew the tree. `borrowed` must include every existing temporary-borrow policy.
After successful delivery, call
`service.NoteOpenAICollabSuccess(ctx, account.ID, borrowed)`. Roots confirm only
their current admitted account; recovering children update only their thread.
Optional attempts make neither change. Admission is not success.

At the existing state-repair avoidance call in the Responses account loop,
replace `AvoidOpenAIStateAccount` with:

```go
gateway.AvoidOpenAICollabStateAccount(
    ctx, effectiveGroupID, sessionHash, account.ID, ciphertextFailure,
)
```

The same replacement belongs at the actual avoidance write in
`noteOpenAITerminalRejectionForSession`, used by the terminal HTTP and stream
paths. Keep that function's current eligibility, threshold, counters and timing.
Pass the classified ciphertext-state evidence for the current attempt. Its
separate root-binding deletion must also be skipped when
`openAICollabPreserveBinding(ctx)` is true. This is an integration edit outside
the four main forwarding files; the affinity stream does not alter terminal
classification or counters.

For an exit with its own avoidance writer, call
`service.NoteOpenAICollabCiphertextFailure(ctx, account.ID, existingAvoidUntil)`
at that writer's current boundary. A true result means use only the thread
scope, suppress tree-wide unbinding, and retain today's request-local exclusion.
Do not add a new call at an earlier observed rejection, move the effect to a
final-response hook, or replay it during terminal rescue. All optional attempt
side effects remain forbidden by `CollabAttempt.AccountEffectsAllowed()`.

## Delivery leases

In the response writer's delivery consumer, after a complete executable call
has actually been written and flushed, pass the event and the confirmed issued
token to `service.NoteOpenAICollabLeaseWritten(ctx, event, lease, encrypted)`.
Use one `CollabDeliveryKey` per delivery, not just ciphertext or tree. The helper
rejects deltas, other phases, generation mismatches, plaintext and partial writes.
It does no parsing, network I/O or logging. Existing hooks isolate consumer panics.

At the upstream acceptance observer in `service/openai_gateway_forward.go`, call
`service.ReleaseOpenAICollabLease(ctx, key, CollabLeaseReleaseConsumed)` separately
for every accepted original delivery. A replacement that is accepted, or a local
ending successfully delivered, uses `CollabLeaseReleaseTerminated`. Failed sends,
failed notice writes and acceptance of only a replacement must not count as
consumption of the original. Completion replays cannot resurrect released leases.

Both scheduling quality migration and model-quality routing consult outstanding
leases only when `CollabLeaseMigrationSuppressionOn()` is true. Tree affinity
alone does not suppress either quality policy. Queue reselection has its own
gate and uses the existing one-shot no-wait selector after the existing wait.

## Validation and limits

The affinity-only tests exercise the real selector with the ladder disabled:
switch-off rollback with populated hints; immediate slots and occupied hints;
model, group, privacy, profit, hard exclusion and admission vetoes; parent
precedence; grandchildren; immediate child retries; root isolation and recovery.
Virtual-time queue tests compare the full 120-second timeout, exact capacity
response and retry header, with and without an immediate alternative.

Store tests cover expiry, renewal, per-delivery release, tombstones, concurrency
and bounded eviction. The process-local store offers no cross-instance hint
propagation or crash persistence. Misses resume ordinary routing. End-to-end
candidate replay requires the main stream's ingress/delivery wiring and record
adapters; baseline goldens are never regenerated here.
