# Anthropic cache TTL

## ADDED Requirements

### Requirement: Key preference and immutable request policy

The system MUST store inherit/off/5m/1h per API key, expose the global 5m/1h default through key list metadata, and freeze the resolved policy before reservation. Create omission MUST mean inherit; update omission MUST preserve the value. Invalid explicit values MUST return 400. Existing keys MUST migrate to off and new keys to inherit without rewriting published migrations.

#### Scenario: Settings change during retry

- **WHEN** a request is accepted with inherit and the administrator changes the default during a retry
- **THEN** every attempt uses the accepted policy and each final route independently determines applicability.

### Requirement: Final Claude wire policy

All currently supported actual Claude Messages route/provider combinations MUST apply one shared TTL rule after existing transformations and breakpoint insertion, before capability cleanup and signing. This includes existing Anthropic native/conversion builders, supported Vertex native/conversion builders, and Bedrock native Messages builders (Bearer/SigV4). Chat/Responses-to-Bedrock has no conversion builder or event-stream decoder and is outside this change; the currently reachable combination may surface existing upstream-auth or generic-forwarding errors and is not an explicit unsupported response. This change MUST NOT add that conversion. Off MUST preserve client TTL intent; forced modes MUST unify legal ephemeral declarations without creating breakpoints. Protocol traversal MUST NOT alter tool schema/input data. Top-level automatic MUST count toward four slots; deferred/thinking blocks MUST NOT receive new markers. Cache mutations MUST NOT contaminate retry/shared bodies. Count_tokens and non-Claude routes MUST NOT apply TTL rewriting.

#### Scenario: No cache declarations

- **WHEN** a key forces 1h but the request has no cache declarations and no existing insertion path
- **THEN** no cache breakpoint is created and applied is false.

### Requirement: Provider usage and billing remain distinct

Internal conversions MUST preserve TTL buckets and presence for streaming and buffered responses. Omission MUST NOT clear prior values; explicit zero MUST clear; cumulative events MUST NOT be added. Missing aggregate MAY be derived internally, explicit zero MUST remain zero. Request policy MUST NOT invent usage. Unknown and partial remainder MUST use the base write price; inconsistent totals MUST be capped deterministically. Commercial overrides MUST affect a billing copy only, preserving upstream response semantics and adjustment attribution. Site explicit prices MUST take priority.

#### Scenario: Partial TTL breakdown

- **WHEN** C creation tokens exceed known A+B buckets
- **THEN** known buckets use their respective rates and C-A-B uses the base write rate, without fabricating raw 5m tokens.

### Requirement: Reservation and truthful observability

Existing reservation MUST account for possible 5m/1h cold writes and disable historical cache discounts when cold-write risk exists. No-risk requests MUST NOT receive an unconditional 1h premium. Final charge MUST remain based on usage and explicit billing rules. Policy/applied MUST describe only the settling final request after capability cleanup; history/non-target MUST be NULL. UI MUST NOT equate applied with provider caching or infer TTL from cache reads.

#### Scenario: Historical record

- **WHEN** policy and applied are NULL
- **THEN** the usage API returns both as null, meaning not recorded, without claiming a cache failure or disabled cache. The usage pages do not display policy or applied (removed 2026-10-31).

## REMOVED Requirements

### Requirement: Legacy global TTL injection and message breakpoint rebuild

The old OAuth-only Boolean and message declaration strip/rebuild policy MUST leave runtime, API and UI. Upgrade MUST NOT automatically enable group fallback or claim semantic preservation for deployments using old injection/subsidy. Rollback MUST preserve new columns and historical charges while explicitly restoring intended legacy behavior.
