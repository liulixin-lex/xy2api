# OpenAI Session Quality Routing

This protects a session after an upstream explicitly reports a configured lower
model. It does not assess response prose, latency, or reasoning token counts.
Existing account IQ/STATE admission checks still apply independently.

Downgrade detection and identity rotation apply only to OpenAI API Key upstream
credentials. OAuth accounts are excluded: changing a session cannot replace the
underlying OAuth account. Existing OAuth device/session fingerprint behavior is
unchanged, including when a session previously used an affected API upstream.

## Request and Response Models

There are three distinct model identities:

1. The original client model identifies the session's quality scope.
2. The final outbound model includes group, channel, account, and protocol mapping.
3. The raw upstream declaration is observed before rewriting it for the client.

The detector compares (2) against (3). Billing continues using the existing model
and response-model billing rules. A configured Sol-to-Terra mapping followed by
a Terra declaration is not a downgrade.

The strict normalizer accepts GPT-5.6 and newer text-model names, known GPT-6/Astra
and GPT-5.6/Sol aliases, registered effort suffixes, and valid date suffixes.
Unknown names, suffixes, missing declarations, malformed JSON, duplicate model
keys, and conflicting envelope declarations do not trigger enforcement. A later
conflicting declaration is logged by the existing observer but cannot undo an
already observed downgrade. Image-generation requests are excluded.

Built-in evidence pairs are Astra/Sol/Terra to GPT-5.6 Luna, including Luna-max.
Additional explicit pairs can be appended in configuration:

```yaml
gateway:
  openai_quality_routing:
    mode: enforce # off | observe | enforce; default enforce
    avoid_seconds: 300
    rules:
      - from: gpt-6-astra
        to: gpt-5.6-terra
```

## Scheduling and Affinity

`GenerateSessionHash` retains the existing explicit-session/content fallback
algorithm. Quality state uses a separate SHA-256 scope incorporating API key ID,
group ID, stable local session hash, and canonical client model. It never deletes
the old shared current/legacy sticky keys to repair one user's session.

`selectAccountWithScheduler` is the shared entry for both the default scheduler
and the advanced scheduler. The protection wraps the existing admission checks,
including model capability, group membership, existing IQ/STATE restrictions,
quota, concurrency, transport, and profit controls. Selection preferences are:

1. Keep an established healthy replacement.
2. Prefer eligible credentials on another normalized upstream address.
3. Try other credentials on the affected address.
4. If all eligible candidates are avoided, try the oldest affected eligible
   credential with the current generation's stable rotated identity.

Exclusion sets also apply to parent-session preference and weighted selection.
Avoidance defaults to five minutes; expiry does not force a healthy replacement
back to the original account. No additional model request is issued by this
feature. Successful HTTP transport does not clear quality state or increment
global account failure statistics.

The optional `OpenAIQualityRoutingStore` cache interface reads quality state and
both sticky-key versions in one MGET. Redis Lua transactions compare generations
for evidence, compare bindings for replacement, and increment revisions. Duplicate
SSE events, concurrent evidence, and old success callbacks cannot advance or
overwrite a newer generation. State TTL follows the sticky-session TTL, normally
one hour. Local state is installed before a bounded Redis synchronization attempt;
the caller waits at most 50ms, with bounded concurrent synchronization workers.
Redis outages preserve local protection but cannot provide immediate cross-instance
consistency. In-flight upstream requests are not recalled.

## Cache and Protocol Behavior

Healthy requests keep their existing body, cache keys, and session headers.
Only an affected session returning to an address with observed bad routing uses
the stable per-generation rotation. Supported session/conversation headers and
body fields are updated together; Responses may receive `prompt_cache_key`.
Other protocols only change supported fields already present. Client auth,
business content, and tool IDs are not modified. Native WS connection affinity
is scoped to the new quality generation to avoid reusing a stale preferred
connection; pool handshake identity checks remain in effect.

HTTP JSON/SSE, Responses-to-Chat/Messages conversions, native Anthropic credential
protocol, HTTP-to-WS, WS HTTP bridge, native WS, and WS passthrough attach the
observer at the outbound protocol boundary. The current response completes.
WebSocket migration occurs before writing a subsequent turn and is represented
separately from transport failover; completed turns are not replayed or billed
twice.

A request containing `previous_response_id`, opaque compaction/reference items,
or incomplete tool context stays on
its required owner. For WS, a known complete request/output chain can reconstruct
the next input before migration; replay retention is capped at 8 MiB. Missing
history remains pinned and emits `continuation_pinned`. Absence of a tool output
alone never proves that a referenced response can be removed.

## Observability and Limits

Structured `openai_quality_routing` events cover downgrade evidence, sticky
invalidation, replacement, identity rotation, pinned continuation, and storage
failure. Session identifiers and provider addresses are represented by digests.
`SnapshotOpenAIAccountSchedulerMetrics().QualityRoutingCounters` exposes event
counts even when the advanced scheduler is disabled.

An upstream can falsely report a model or ignore a new session identity. This
feature cannot identify or directly choose a third party's hidden pool account.
An abnormal migration can incur a cache cold start; unaffected sessions preserve
their existing cache identity. No production deployment, schema migration,
management page, or real-model probe is required by this implementation.

The isolated test ledger and four delivery artifacts live at
`/xy/artifacts/gpt-quality-routing/`. Their observed outputs, including environment
failures and later successful reruns, are recorded in `VERIFICATION.txt`.
