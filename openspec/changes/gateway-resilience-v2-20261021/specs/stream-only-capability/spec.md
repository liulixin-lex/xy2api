# 仅流式能力学习（0.4.4）

## ADDED Requirements

### Requirement: Learn stream-only accounts from deterministic rejections

With `capability.stream_only_learning` on, when a non-streaming OpenAI Responses or Chat Completions text request (not compact, image or WebSocket) receives an upstream 400 whose code or message deterministically means the upstream only accepts streaming requests, the gateway MUST mark that account as unavailable for non-streaming requests of that protocol (Responses or Chat) for `capability.learn_ttl_seconds` (default 21600). With `failure.decision_v2` on, the request MUST switch accounts immediately; the rejection MUST NOT count as an account failure, a cooldown or a session failure, and MUST count against the request budget. With `failure.decision_v2` off, the account is learned but the current request returns the 400 as before.

#### Scenario: Learn and switch (SO-1)
- **WHEN** a non-streaming Responses request is sent to account A, A returns 400 "This group only accepts streaming requests", and account B can serve
- **THEN** the request succeeds on B, A is marked stream-only for Responses, and A is not cooled down.

#### Scenario: Switch off
- **WHEN** `capability.stream_only_learning` is off and the same rejection happens
- **THEN** nothing is learned and the 400 is returned as in 0.4.3.

### Requirement: Skip learned accounts for non-streaming requests

Account selection for OpenAI groups MUST skip an account learned as stream-only for non-streaming requests of the learned protocol, and MUST NOT skip it for streaming requests. When skipping leaves no account, the request MUST fail as no-account with reason `capability_stream_only`. Explain MUST show the reason and the remaining time for skipped accounts. A learned mark MUST expire after its time to live, and MUST stop filtering while the switch is off.

#### Scenario: Next non-streaming request avoids the account (SO-1)
- **WHEN** account A is learned as stream-only and a new non-streaming request arrives
- **THEN** A is not selected, and Explain shows `capability_stream_only` with the remaining time.

#### Scenario: Streaming request still uses the account (SO-1)
- **WHEN** account A is learned as stream-only and a streaming request arrives
- **THEN** A remains eligible.

#### Scenario: Only the learned account remains
- **WHEN** the group has only account A, learned as stream-only, and a non-streaming request arrives
- **THEN** the request fails as no-account with reason `capability_stream_only`, not as a client error.

#### Scenario: Expiry
- **WHEN** the time to live of the mark passes
- **THEN** A is eligible again for non-streaming requests.

### Requirement: Administrators can see and clear learned capability

The account admin API MUST return the learned capability state with protocol, mode and expiry, and MUST provide an operation to clear it; both MUST work while the switch is off. Saving an account from an older admin page MUST NOT overwrite or restore the learned state. The admin console MUST show a stream-only badge with the remaining time and a clear action.

#### Scenario: Upstream configuration changed
- **WHEN** account A was learned as stream-only and an administrator then changes A's upstream credentials or endpoint
- **THEN** the old mark no longer filters A.

#### Scenario: Copied account
- **WHEN** an administrator copies, creates or imports an account from one that is learned as stream-only
- **THEN** the new account carries no learned state.

#### Scenario: Clear (SO-1)
- **WHEN** an administrator clears the learned state of account A
- **THEN** the next non-streaming request may select A again.
