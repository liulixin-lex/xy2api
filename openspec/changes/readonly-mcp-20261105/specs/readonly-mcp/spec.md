# Requirements

## ADDED Requirements

### Requirement: MCP credentials bind to the MCP resource and current XY2API user
The server SHALL validate each remote credential for the exact MCP resource/audience, trusted issuer, expiration/revocation, and stable local-user subject mapping. It SHALL load the current active user and role on each request. Missing or different audience, untrusted issuer, or unmapped subject SHALL be rejected. It SHALL NOT accept global admin API keys, user gateway API keys, admin session tokens or credentials for another resource.

The service SHALL provide a user-consented OAuth 2.1 Authorization Code flow with PKCE for remote hosts, including protected-resource and authorization-server metadata, exact redirect URI validation, resource-bound tokens, one-time authorization codes, refresh-token rotation/revocation and client-specific consent. Client registration SHALL create public clients only. Reusing a site login session to establish the resource-owner identity SHALL NOT make that session token an MCP access token.

#### Scenario: Audience mismatch
- **WHEN** a token is valid for another HTTP resource or has no audience for this MCP server
- **THEN** the server SHALL reject the request before tool discovery or database access.

#### Scenario: Permission revoked
- **WHEN** an operator loses the required permission or a user is disabled
- **THEN** the next MCP request SHALL evaluate the new role/permission row and SHALL NOT use a cached allow or cached business data.

### Requirement: Every operator tool enforces its complete source ACL
Every tool SHALL be statically mapped to its exact catalog entry keys, source modules, levels, AnyOf/flags, target, fields, service-enforced context and output fields. A strict route mirror SHALL preserve that entry’s AnyOf semantics; a new composite tool SHALL independently check each protected source and require all its grants. Every tool SHALL run operatoraccess policy/target checks independently of Gin middleware. Unknown tool or unmapped source SHALL be denied.

The server SHALL filter `tools/list` using the current active actor and non-target ACL: root receives the full v1 allowlist, operators receive only tools whose source/module/level/flags/Fields rules are currently allowed, and ordinary users receive only owner-bound `me.*` tools. Listing evaluates the default required source set. Target, input-dependent field and optional-source checks require actual arguments and SHALL run on every `tools/call`; listing a tool SHALL NOT imply authorization for every target. `tools/call` SHALL repeat all checks regardless of the list result.

#### Scenario: Service-enforced search field rule
- **GIVEN** a tool reads the user-search source for an operator
- **WHEN** the source catalog entry applies user_search
- **THEN** the current operator Scope SHALL be present in service context
- **AND** API key value search SHALL remain hidden.

#### Scenario: Composite request tool has separate data sources
- **WHEN** a tool reads more than one catalog-protected source
- **THEN** each source Entry SHALL be independently authorized
- **AND** denial of any requested source SHALL fail the call without querying that source; authorization failure SHALL NOT be reported as ordinary partial coverage.

### Requirement: User tools are owned by the authenticated user
User-facing MCP tools SHALL bind user_id to the authenticated local user and SHALL NOT accept user_id as an ownership override. Their queries SHALL use user-safe DTOs and existing ownership checks. They SHALL preserve backend-mode restrictions and the user-error-view gate. A setting-read error SHALL deny access rather than be treated as a confirmed disabled restriction. Admin diagnostic tools SHALL preserve the existing admin compliance acknowledgement gate without modifying the acknowledgement.

#### Scenario: User requests foreign usage
- **WHEN** a user supplies a foreign resource identifier
- **THEN** the server SHALL return the same not-found result as an absent resource
- **AND** SHALL not disclose the foreign owner or record existence.

### Requirement: Query tools are read-only and bounded
MCP business tools SHALL use fixed parameterized SELECT projections via a distinct PostgreSQL read-only identity, with bounded time ranges/page sizes and statement deadlines. Redis calls SHALL use reviewed non-mutating read commands. The server SHALL NOT probe upstream services or invoke account tests, upstream-account OAuth refresh, monitor run, quota refresh, cleanup or scheduler mutations. MCP authorization-server token lifecycle and audit writes SHALL be separate control-plane interfaces, unavailable to tool handlers.

#### Scenario: Runtime slot read
- **WHEN** an account-load tool reads runtime slots
- **THEN** it SHALL use a verified no-cleanup peek
- **AND** SHALL NOT issue ZREM/ZREMRANGEBYSCORE, EXPIRE, SET, DEL or mutating scripts.

#### Scenario: Realtime concurrency monitoring is disabled
- **WHEN** either the operations-monitoring gate or realtime-monitoring switch is disabled
- **THEN** the concurrency tool SHALL return a disabled source state
- **AND** SHALL NOT query account data or call any Redis reader.

#### Scenario: Upstream health request
- **WHEN** a tool reads account usage or balance
- **THEN** it SHALL read a previously stored/passive snapshot
- **AND** SHALL not send a provider network request.

### Requirement: Correlated request evidence is explicit
Any result that combines usage, error, upstream-attempt or runtime data SHALL preserve original source identifiers and return source, time window, coverage and relation evidence/status. It SHALL distinguish the final client outcome from per-attempt upstream status and SHALL NOT normalize arbitrary request ID prefixes into exact joins. The v1 detail and attempts tools SHALL accept only the numeric `error_row_id` returned by request search; they SHALL NOT interpret a client/request ID as an error row ID.

#### Scenario: Recovered upstream error
- **WHEN** a separately queried or explicitly composite result shows one upstream attempt failing while the final client request succeeds
- **THEN** the response SHALL show the attempt and final outcome separately
- **AND** SHALL deduplicate the same persisted error row across queried sources while preserving distinct settlement and attempt rows; source-row counts SHALL NOT be presented as an authoritative unique-client-request total.

#### Scenario: Detail tool receives a client request ID
- **WHEN** a caller supplies a client/request ID to a detail or attempts tool
- **THEN** the server SHALL reject the input as an invalid `error_row_id`
- **AND** SHALL not run a source query.

#### Scenario: Candidate correlation
- **WHEN** a usage row is linked by client ID/time evidence but lacks a shared exact request ID
- **THEN** the relation SHALL be labeled candidate/corroborated with original IDs and window
- **AND** SHALL not be represented as an exact trace.

### Requirement: Default diagnostic output excludes free-text sensitive records
V1 tools SHALL not retrieve or return error_body, free upstream message/detail/JSON, system message/extra, request/prompt bodies, headers, API keys, upstream addresses, client IP or user email. JSONB attempts SHALL be projected to allowlisted scalar fields inside PostgreSQL; complete JSONB SHALL NOT be loaded into application memory for subsequent redaction. Any future proposal to read sensitive free text SHALL require a new revocable scope, exact field policy and durable access audit before query; it is outside this change.

### Requirement: Tool discovery is static and no write tools are registered
The server SHALL register a reviewed static query allowlist. MCP annotations SHALL NOT be used as the sole read-only enforcement. The server SHALL expose no arbitrary SQL, HTTP proxy, raw database connection or business mutation tool.

#### Scenario: Forged unknown tool
- **WHEN** a client calls an unregistered tool
- **THEN** the server SHALL reject it without invoking any business service or repository.

### Requirement: Recovered upstream search has its own authorized source
The default request search SHALL mirror the safe projection of GET /ops/requests. Only an explicit include_recovered_upstream option SHALL add GET /ops/upstream-errors, and that source SHALL be independently authorized. The optional endpoint is a provider-health source, not a recovered-only source. The implementation SHALL classify rows by persisted evidence, mark unproven recovery unknown, and deduplicate the same ops_error_logs storage row across sources before applying the shared limit (at most 100) and cursor. Distinct usage and attempt rows SHALL not be collapsed by request ID. A proven recovered record SHALL preserve its source and numeric error row ID and SHALL NOT count as a final client failure.

#### Scenario: Optional source denied
- **WHEN** the optional recovered source is requested and its catalog entry is denied
- **THEN** the complete call SHALL fail before querying that optional source.

### Requirement: Runtime read failures are not zero measurements
Redis/database source failures SHALL return an unavailable or explicitly partial state. They SHALL NOT be normalized to successful zero measurements. Monitoring gates SHALL be checked before account/runtime queries, and no error path SHALL trigger cleanup or upstream probing.

### Requirement: Identity-specific responses are not shared across callers
Authenticated MCP responses SHALL use HTTP private/no-store cache control. SDK cache hints for identity-specific lists SHALL set ttlMs=0 and cacheScope=private. Per-actor discovery SHALL not mutate a shared global tool registry; concurrent requests SHALL not change another actor's available tools or result data.
