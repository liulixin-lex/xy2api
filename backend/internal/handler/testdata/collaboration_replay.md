The collaboration replay harness characterizes production code at `b3300787c`. It does not implement the proposed collaboration changes. Every request passes through `ClientRequestID`, `OpenAIGatewayHandler.Responses`, real selection, and `OpenAIGatewayService.Forward`. Only repositories, concurrency storage, and the upstream transport are replaced. The transport never opens a connection.

Run from the backend directory:

```sh
go test -tags=unit ./internal/handler -run '^TestCollaborationReplay' -count=3
go test -race -tags=unit ./internal/handler -run '^TestCollaborationReplay' -count=1
```

The suite re-enters its own test binary to isolate process-local learned capabilities. The race build re-enters the same instrumented binary. Each case has a distinct, deterministic account pair and priorities. Request execution uses `testing/synctest`; UUID and cryptographic ID readers use separately seeded, synchronized readers. Timer-dependent keepalives are disabled; explicit heartbeat frames are scripted. Baseline requests never enter the random slot-wait backoff. Constructors run outside virtual time because the existing tool cache owns a janitor. The gateway ticket worker is stopped before requests. Settings, global readers, runtime service, and log outputs are restored.

Goldens store HMAC-SHA256 of exact outbound bytes and complete client frames, with a public synthetic key. There is no JSON canonicalization or erasure of generated response IDs. They also contain per-account sends, actual triggered fake rules, client status/code/type, retry hints, stream intended status, maximum virtual idle interval, content-survival labels, existing learning before each send and after completion, sticky bindings, avoidance, rate-limit mutations, and usage attribution. Learning observation reads the existing private store at synchronous boundaries and never writes it. A future store representation change needs an explicit observer update.

Success requires a successful application terminal; HTTP 200 containing failed JSON is a failure. Retryability is the HTTP retry convention (408/409/429/5xx), explicit retry suppression, and the Codex failed-frame convention (unknown codes retry; context length, invalid prompt, and cyber policy do not). Actual error codes and retry headers remain separately recorded. Content labels distinguish task delivery from ciphertext copied into arguments, and distinguish individual reasoning summaries. They describe the final attempted input; they do not prove that an actual model understood it.

`checkReplayOff` requires exact equality. `checkReplayOn` accepts a settings snapshot and either `ladder` or `lossless_first`:

- `ladder`: each successful baseline turn must match exactly, including sends, bodies, events, existing learning, bindings, avoidance, and usage. New observational record-call counts are excluded. A failed baseline may become successful or retain the same failure form; the maximum client idle gap must remain below the fixture's idle limit. Terminal rejection counters, persistent effects, avoidance, cooldowns, and failed-result retry advice must match. Acceptance or the saved terminal must arrive within 15 seconds of the baseline terminal for retryable errors, or 60 seconds otherwise. A saved error cannot gain an earlier header/heartbeat commit. Queued users retain the immediate baseline sends, events, and terminal time.
- `lossless_first`: no successful baseline may fail; two failures retain the same failure form; with an unchanged success/failure outcome, no retained content may disappear. Recovering a previously failed request takes priority over content retention. Both modes reject newly duplicated terminals or usage attribution.

Mutation tests demonstrate rejection of changed bytes, accounts, extra sends, learning, bindings, avoidance, error status/code/type/retryability, extra content loss, duplicate terminals/usage, and a late terminal. They also reject early header commitment, changed rejection counters/retry advice, repeated persistent side effects, takeover for a queued user, and local ending before two matching failures of the same delivery. Different deliveries do not count toward that allowance. Coverage sentinels catch an omitted ingress budget, a fake that always succeeds, a shortened repair chain, or loss of cross-turn learning. Fixture hygiene rejects absolute paths, emails, UUID-shaped values, and installation/workspace fields in the corpus, generated requests, and golden data; negative examples are constructed synthetically.

To exercise a future implementation, provide a repository-local JSON settings snapshot through `COLLAB_REPLAY_SETTINGS` and select `COLLAB_REPLAY_MODE=ladder` or `lossless_first`. Unknown settings are rejected until the new fields exist. The snapshot is published before ingress; the case overrides only existing budget settings, keepalive, standalone-tool compatibility, and the legacy plaintext setting in the mixed-marker case. The baseline currently has none of the eight new switches. Running the comparator against baseline output proves the comparator contract, not the behavior of nonexistent switches.

Integration must supply `configureReplayRecords` to connect the scripted `replayRecordStore.Operation` to the real batch-read/write API, with production timeout/error/panic handling. It must supply `configureReplayHistory` for `issuance_record` and `prior_local_ending` preconditions. Candidate runs fail explicitly without these adapters. Baseline record scenarios have zero calls; slow, error, and panic scripts are independently tested. Existing sticky storage is deliberately not substituted for the absent record API. No production wiring is required to run baseline tests.

`UPDATE_COLLAB_REPLAY=1` regenerates the data. Only do this against unchanged baseline production code. Never regenerate to make a candidate pass. Candidate settings and golden regeneration cannot be combined.

The corpus covers every comparison-table scenario. Each seed crosses native/passthrough, streaming/non-streaming, and attempt limits 1/2/4/off:

| Comparison scenario | Corpus seed |
|---|---|
| Only reasoning unreadable; other account cannot read anything | `reasoning_only_unreadable` |
| Task readable elsewhere; other account rejects reasoning | `task_readable_elsewhere`, `task_then_reasoning_elsewhere` |
| Current account rejects compaction but can read task | `summary_unreadable` |
| Child task unreadable with local ending off/on | `child_task_local_ending_off`, `child_task_local_ending_on` |
| Root task unreadable; named ordinary message | `root_task_unreadable`, `ordinary_agent_named_unreadable`, `ordinary_named_unreadable` |
| Maximum attempts one | `single_attempt_task` |
| Wrapped rejection: retry works, repair required, always fails, insufficient evidence | `wrapped_once`, `wrapped_until_repaired`, `wrapped_never_succeeds`, `wrapped_insufficient_evidence` |
| Ordinary unclaimed error | `ordinary_unclaimed_error` |
| Successive reasoning rejections | `successive_reasoning_rejections` |
| Target has no immediate slot | `target_slot_unavailable` |
| Unnamed task; task plus reasoning and compaction | `unnamed_task`, `unnamed_task_reasoning_summary` |
| Bad compaction; compaction request; review request | `bad_compaction`, `compact_task`, `review_body` |
| Previous reader differs from issuer; previous reader now rejected | `reader_differs_from_issuer`, `previous_reader_unnamed` |
| Outer gateway failure with encrypted rejection | `outer_gateway_inner_encrypted` |
| Provenance conflict; provenance rate limit | `provenance_conflict`, `provenance_rate_limit` |
| New task after ending; old task after compaction | `new_task_after_local_ending`, `old_task_after_compaction` |
| New account returns server error | `switched_account_server_error` |
| Next tool loop already repaired | `next_tool_loop` |
| Named item absent | `missing_named_item` |
| Argument position: ignored, replacement accepted, replacement rejected | `args_accept`, `args_placeholder_accepted`, `args_reject` and paired-copy variants |
| Plain collaboration through Chat bridge | `chat_plain_collaboration` |
| Optional target rate limit, server error, hang | `target_rate_limit`, `target_server_error`, `target_hang` |
| First rejects placeholder, second accepts | `second_accepts_placeholder` |
| Tool output, two fields, search, reasoning, task chain | `long_repair_chain` |
| Site accepts deletion only | `deletion_only` |
| Client retry succeeds | `client_retry_success`, `client_immediate_retry` |

Additional seeds cover all requested additions: `first_account_waiters`, `target_executable_then_failure`, `target_created_then_stall`, `little_time_left`, `shared_quota_extra_send`, `child_after_second_recovery`, `single_declaration_mixed_markers`, and `record_store_slow/failure/panic`. Named-rejection learning is recorded during `client_retry_success`, `client_immediate_retry`, and `next_tool_loop`. Executable-call failure and created-frame stalls also run on the first account to observe their actual baseline terminal behavior. Other seeds cover failed JSON, stream aggregation, heartbeat/created/semantic-output prefixes, pointers, conflicting identity/locators, duplicate members, structured output, and cancellation.

The queued-user fixtures exercise both retryable and non-retryable saved terminals through real user admission with a full/queued load snapshot. `handled_task_named_again` and `handled_reasoning_named_again` record each input item's exact-byte digest: `checkHandledReplay` rejects changes to any other identified item on subsequent sends to that account. Current baseline already preserves those other items. Existing retry sequences test that a local ending cannot preempt a retry that succeeds today. None of these timing or side-effect assertions relies on wall-clock sleeps.

Confirmed differences from the comparison table's description of current behavior:

- A named encrypted ordinary `message` fails in one send with non-retryable 400 `encrypted_content_rejected`. The table's successful placeholder outcome does hold when an ordinary message uses the `agent_message` envelope; both representations are tested.
- Reasoning repair removes ciphertext, retaining its item and summary. Batch repair retains all reasoning summaries. Compaction is removed as a whole item. Descriptions of deleting both reasoning and compaction overstate the content loss.
- With limit 1, reasoning and compaction repair stop at 400; with limit 2, successive reasoning failures stop after two sends. The stated successful two/three-send outcomes require enough budget. Nested task placeholder repair still succeeds with limit 1.
- Wrapped generic 400 replaces the nested encrypted task part; it preserves the task envelope, reasoning, and compaction rather than deleting the task item.
- Previously reading a task does not rescue an unnamed rejection: the task remains and the request returns 400. With other encrypted history, reasoning ciphertext and compaction are cleared when budget permits. There is no issuance record at baseline, so different-issuer setup remains pending.
- A site that rejects ciphertext-free reasoning and only accepts removing that item still returns 400; the current targeted repair retains the reasoning item. There is no automatic second deletion fallback.
- Second-account placeholder recovery succeeds at limit 4/off but fails at limits 1/2. The seven-send repair chain succeeds at limit 4/off, fails at limit 1 on standalone-tool refusal, and at limit 2 after field repairs.
- Missing named items switch once at limits 2/4/off; limit 1 returns 502 after only the first send.
- Client retry is timing-sensitive: after both accounts are avoided, immediate retry makes zero sends and returns 502. After avoidance expires it succeeds in one send. At limits 1/2 only the first account was exhausted, so immediate retry can use the second account.
- Passthrough can omit the client error code for generic 400, unnamed-task 400, and provenance 409, despite preserving the HTTP status. The golden failure contract preserves these differences.
- A request with a valid server-side continuation pointer can still repair a nested task and succeed in two sends. A conflicting locator returns non-retryable 400 without switching. The separate terminal-condition table's blanket pointer-error and ambiguous-locator/failover descriptions therefore do not cover these fixtures.

Additional observed envelope distinctions: non-streaming failed JSON is returned as HTTP 200 failed JSON without repair; a streaming client receiving that JSON gets 502 after available failover. Semantic output or an executable call followed by failure cannot be retried after delivery, while buffered aggregation can recover before delivery. These are separate baseline outcomes, not interchangeable wrappers.

Limits: no real upstream decryption, database, shared record service, actual client reconnection, process crash, or distributed concurrency was tested. Slot contention and shared quota are deterministic in-memory models. Usage ownership is observed through the real simple-mode usage path; monetary debits are not exercised. The placeholder-only and future-record preconditions remain explicitly identified above. New switch behavior, records-default-on, rollback with records, and the documented switch-independent fixes require the integrated candidate.
