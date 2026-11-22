# Collaboration records integration

The service constructor creates one `SafeCollabRecordsStore`; server shutdown
closes it. `GatewayCache` has no new required methods. The Redis cache supplies
the optional `CollabRecordsCache` capability and ordered batch persistence.

All existing forwarding paths remain untouched by this stream. Integrate the
following calls in the final-client writer and the takeover selection loop:

1. Retain the ingress settings pointer. Reuse the existing shape scan's native
   collaboration-tool and agent-ciphertext flags; do not scan ordinary requests
   just for records. For each physical response, obtain account/site digests with
   `gateway.CollabRecordAccount(account.ID, resolvedBaseURL)`. This deliberately
   excludes credential versions and tokens.
2. Call `gateway.NewCollabRecordDelivery(CollabRecordDeliveryInput{Settings:
   settings, HasTools: hasTools, AgentCiphertext: agentCiphertext, Scope: scope,
   Account: accountDigest, Site: siteDigest, Generation: generation, SendSeq:
   sendSeq})`. Register the returned consumer with `hooks.Register(consumer)`
   only when it is non-nil. Construct it again for every new physical response.
3. The writer calls `hooks.AboutToWrite(ctx, event, finalBytes)` immediately
   before writing. Only after the complete write and flush succeed, call
   `hooks.Written(ctx, event, finalBytes)`. Use identical generation, send
   sequence, frame sequence and bytes at both phases. Set `Executable=true`
   for native collaboration call completion/final frames; metadata alone
   suffices for other frames. Supported event types are
   `response.output_item.done`, `response.completed`, `response.done`, and
   `response.json`. Payload may be JSON data or a single SSE frame.
4. Call `hooks.Completed(ctx, event, nil)` on every exit, including disconnect
   and failure. It voids pending issues; successful call writes remain confirmed
   even if the response later fails. Hooks independently recover consumers.
5. After normal upstream completion, call
   `gateway.RecordCollabConsumed(ctx, settings, normalCompletion, accountDigest,
   unchangedCiphertextKeys)`. Pass only items actually forwarded unchanged on
   the successful attempt. Local completions and abandoned attempts do not
   count. Build keys from the decoded `encrypted_content` string without
   trimming or normalization using `CollabDigestDomainCiphertext`.
6. When routing/dispositions need evidence, create one
   `readCtx, cancel := resilience.WithCollabRecordReadBudget(ctx)` and retain it
   for the request. Call `gateway.CollabRecords().LookupBatch(readCtx, batch)`
   once with all item/account/event queries and the tree scope. Set `Records`
   and `Dispositions` independently. No budget context means local-only reads.
7. Parse `gateway.ParseCollabSeenNotices(original, verifiedSenderThreads)` before
   that batch. Include each notice event in a `CollabRecordQuery{Key:
   CollabDeliveryKey{Scope: scope}, Event: notice.Event}` disposition query.
   Resolve envelope sender names from verified tree membership. An unknown
   name cannot authorize borrowing. Historical notices before the latest model
   output cannot authorize another borrow. Pass returned terminations to
   `resilience.CollabSeenEvent` for observation and to the borrow helper.
8. Within each existing admission/quality round, use
   `resilience.OrderCollabFailoverCandidates(settings, candidates,
   accountDigests, originalOrderItems, now)`. Supply protected items, their
   records and known rejection digests. It never adds/removes candidates.
9. Use `resilience.DecideCollabRedispatchBorrow(input, candidates, now)` only
   with already-admitted candidates. `Convertible` must come from the actual
   planner after mapped-model, protocol, flattening and account/global checks;
   `Immediate` must reflect immediate slot availability. Supply every carried
   compaction/call-argument ciphertext and the parent's unread-ciphertext flag.
   Honor both `PreserveAdmissionBinding` and `PreserveSuccessBinding`. End the
   preference after the first collaboration call. On any borrow failure call
   `resilience.CollabRedispatchFallback(originalAccountID, original)` and run
   normal admission/forwarding again with those exact bytes.

Records, routing, dispositions and borrowing keep their existing independent
settings gates. Borrowing also requires lossless-first. Reads never wait more
than one request budget of 250 ms; at most four shared readers and one writer
exist per store. The writer pipelines up to 64 ordered mutations. Queue overflow
drops shared persistence, never the local write or client response.

Capacity is 256 trees with 256 entries each, both locally and in Redis. Full
capacity is absence. Records expire within seven days. Issuance tokens are
independent and voids cannot be confirmed or revived. Terminal counts have a
delivery index; they do not scan the global table for each item.

No existing contract signatures changed. New exports are concrete helpers or
optional cache/batch capabilities. Final-frame observation is bounded at 1 MiB
and notice scanning at 4 MiB; exceeding those bounds yields absent hints. The
seam writer still owns byte delivery, settlement and all persistent account
effects. This stream neither changes those decisions nor tests live upstreams.
