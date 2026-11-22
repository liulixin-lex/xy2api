# Codex collaboration messages and recovery

[中文](codex-collaboration.md) · [Settings reference](gateway-resilience-settings.md)

## Before enabling plaintext

- Message bodies are visible to this gateway, the relay, and its upstream provider.
- Bodies enter content moderation and its records.
- Turning the switch off cannot withdraw plaintext already sent, stored in client history, or retained in moderation records or upstream systems.

New OpenAI accounts default to plaintext off. Creation-only paths save explicit booleans; ordinary edits and account copies retain the stored setting unless the switch is changed. Session and agent-identity imports can update existing accounts, so an untouched switch sends no override. New accounts created through those imports may omit the field, which also means off. An explicit choice sends true or false. The global plaintext switch, model list, and account switch govern conversion for new conversations. Existing plaintext markers on API key conversations retain session continuity after the account switch is turned off. Turning off the global plaintext switch stops new conversions; responses already in progress still finish restoration.

Conversion only applies to eligible native Responses HTTP requests. Messages in custom collaboration namespaces always remain encrypted. An enabled switch is not evidence of successful delivery.

## What a completed task means

A child task marked “completed,” or a successful command-line exit, can mean that the gateway ended an unreadable turn normally. **It does not confirm that the task was done.** Read the returned `Gateway notice` and check actual results before continuing.

- A notice that work never began is used only when that is known. Supply a readable copy before trying again.
- If earlier work may have taken effect, check files, commits, and external actions before repeating any steps.
- A main-thread notice about missing input asks for that content in plaintext; it does not confirm completion.
- Repeated delivery failure calls for another execution path or a report of missing input, not indefinite reassignment.
- An unreadable tool-result placeholder indicates neither success nor failure. Do not blindly repeat actions with side effects.

Placeholders replace unreadable fragments while retaining the surrounding message envelope. Turning switches off cannot restore lost history, reasoning, or cache prefixes. Locally generated ending notices are not billed; real upstream work retains normal accounting, while abandoned optional attempts do not charge customers or deduct their balance.

## Acceptance for each site

Use an authorized candidate instance and test groups without moving production traffic. Test each intended site, model, and account combination, including fallback and borrowed routes. Generate fresh test phrases instead of using user tasks. Keep actual site details and request references in restricted acceptance records, outside the public repository.

1. Require passing offline tests and replay comparisons. Behavior with new features off must match the existing path. With the ladder enabled and the two risky switches below off, existing repair must run first and unchanged; requests it previously rescued must still succeed. The ladder takes over only where repair would end in an error or does not recognize new ciphertext evidence. If the ladder also fails, return the saved original error with the same status, code, and retryability. Test the risky switches separately after the owner accepts their risks.
2. Record client core version, actual outbound model, account and site, request reference, and settings snapshot. Start a persisted native HTTP conversation. Send a fresh phrase to a child, receive it through `send_message`, then complete a new phrase through `followup_task`. Verify content, not just HTTP status.
3. Continue the same conversation and child on another candidate site under controlled routing. Verify fresh phrases in both directions.
4. Resume an older conversation from its root. Confirm the request carries existing collaboration members plus a new declaration, then repeat the phrase exchange.
5. Turn off account conversion and send another message in the existing conversation. Record normal continuation, explicit error, or silent loss; silent loss fails acceptance. Also verify a new conversation after disabling.
6. Enable the ladder and local ending for test groups. Route a receiving request to an account unable to read its content. Verify existing repair runs first and a successful repair never enters the ladder. For requests reaching the ladder, check its recovery order and placeholders; if it also fails, verify the saved original error is returned. Check child ending, notice delivery to the parent, successful continuation after reassignment, and main-thread local ending with a successful command-line exit. Check that local generation and abandoned optional attempts do not charge customers, and each inbound request is settled only once. Optional failures must not write account faults, cooldowns, avoidance, or unbinding. Record or observation failures must not block delivery.
7. Test compaction, review, and structured-output requests separately: no local ending; use placeholders and existing repair as applicable. Remaining failures retain their error shape. Failures after semantic output must not be hidden behind a local success.
8. Turn off all eight settings in turn. Include ladder on with lossless-first off, ladder on with records off, ladder on with local ending off, and ladder off with dependent choices still saved. Previously ended threads must continue without blindly repeating effects.
9. Compare actual delivery outcomes: accepted intact; lost with notice returned to parent; lost with notice unconfirmed; lost with notice written to user; silently lost; interrupted. Writing a notice does not prove a human or model read it. HTTP success and “completed” are insufficient acceptance criteria.

## Dependencies and rollback

Of the eight settings under `collab`, only `records` defaults on. `local_ending.mode` defaults to `off`; all other behavior switches default off. Each request keeps its starting settings snapshot.

| Configuration | Behavior |
|---|---|
| Ladder off | Existing repair; no lossless-first handling, local ending, record routing, or reads of ended/replaced states |
| Ladder on, lossless-first off | Existing repair first; ladder only before an otherwise final error or for new ciphertext evidence |
| Ladder on, lossless-first on | Ladder before existing repair; less silent loss, but rare additional errors remain possible |
| Ladder on, records off | Ladder works without delivery records; ended/replaced states are still read and written |
| Ladder on, record routing off | Untargeted retries; no borrowing readable content for reassignment |
| Local ending off or group excluded | No new local endings; ladder still reads earlier ended states |

`collab.lossless_first` defaults to false and requires the ladder. It moves the ladder ahead of existing repair to reduce silent loss, but can add errors in rare cases: failure after semantic output, extra attempts consuming shared upstream quota, or concurrency races. Enable only after the owner accepts these risks.

`collab.strict_marker_veto` defaults to false. Stricter ciphertext checks can withhold a plaintext marker from messages that currently succeed. It can add errors and is not recommended.

Disabling the seven behavior controls restores the existing repair path. Observation, response restoration isolation, unconditional response fixes, and UI changes require a version rollback. Stored records expire; process-local records disappear on restart. Delivered plaintext, placeholders, local replies, and previous actions cannot be withdrawn. Check prior results before reassigning.

When an older backend omits `collab`, the UI shows defaults and omits the untouched section from saves. Unsupported or null values disable only the collaboration controls and are omitted from unrelated saves. Supported server fields, including unknown extensions inside local ending, survive ordinary saves and JSON round trips without added defaults. JSON edits cannot invent or alter unknown fields; explicit known-field edits are validated before submission and by the backend. If an older version drops the section on save, upgrading again restores defaults. Keep a restricted configuration backup and repeat acceptance before re-enabling.
