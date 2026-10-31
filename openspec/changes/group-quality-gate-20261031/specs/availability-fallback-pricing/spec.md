# Availability fallback pricing

## ADDED Requirements

### Requirement: Servable membership

One shared evaluation MUST decide whether a member can serve a group. A servable member MUST be:

- usable: platform match, enabled, schedulable, free of hard states, and within the group's profit ceiling when its profit gate is on;
- free of resilience soft penalties;
- not vetoed by the group's quality gate. Trial, guard-held and pinned accounts are not vetoed.

The fallback trigger, the ops group availability count and its alert MUST all use servable membership. A pinned account outside the limits MUST count as servable, but the quality-gate guard MUST NOT count it as passing.

#### Scenario: Profit gate blocks the whole group

- **WHEN** every member of a group with a fallback group is above the group's profit ceiling
- **THEN** the group is not servable and requests move to the fallback group.

#### Scenario: Both gates off

- **WHEN** neither the quality gate nor the profit gate is enabled for a group
- **THEN** its available account count equals the previous release.

### Requirement: Fallback decision and selection

The fallback middleware MUST keep running after authentication and before model resolution and selection. It decides at group level, without looking at the model:

- origin group servable: the request stays there. The last resort relaxes quality ejection only; if nothing remains, the request MUST fail with no available account.
- origin group not servable, fallback group servable: the whole request MUST move to the fallback group (a WebSocket request moves for the whole connection). Selection MUST then follow the fallback group's rules: quality gate state, guard, pinned accounts, fast-tier line, profit gate switch, margin and price. If the fallback group has no account, the request MUST fail without chaining.
- origin group not servable, no servable fallback group: the origin group's last resort MUST apply, then no available account.

Key memory, the 120-second return to the origin group and the one-fallback-per-request limit MUST stay unchanged. A group whose servable accounts do not support the requested model MUST NOT trigger the fallback, as before. Composite groups MUST keep their current fallback decision.

#### Scenario: Fallback group with a higher rate

- **WHEN** a request falls back to a group whose rate is higher than the origin group's
- **THEN** accounts are admitted by the fallback group's profit ceiling.

#### Scenario: Return to the origin group

- **WHEN** the origin group has been servable for 120 seconds
- **THEN** new requests of remembered keys return to the origin group.

### Requirement: Pricing follows the serving group

A request MUST carry two identities: the origin group, used for authorization and limits through `BillingAPIKey`, and the serving group, used for pricing. Price points MUST NOT fall back to the origin group.

A request served by the fallback group MUST be priced by the fallback group:

- group rate, the user's override for the fallback group and the peak factor;
- image and video prices and long-context rules;
- channel pricing and billing model;
- the fallback group's force-Fast and Fast-at-standard rules.

In-flight reservation amounts MUST be estimated at the fallback group's prices. Each WebSocket turn MUST freeze the fallback group's pricing snapshot and profit gate together with the origin group's authorization snapshot.

The usage row MUST record:

- `group_id` as the fallback group and `origin_group_id` as the origin group;
- `rate_multiplier`, `actual_cost` and the image and video amounts at the fallback group's values;
- account-side cost and `account_rate_multiplier` unchanged;
- quota and balance deductions against the origin key and the user, at the fallback group's price.

#### Scenario: Forced Fast in the fallback group

- **WHEN** the fallback group forces Fast and the client did not request priority
- **THEN** the charge follows the fallback group's Fast rules, and the platform does not absorb a difference.

### Requirement: Authorization and limits stay with the origin group

The origin group MUST govern:

- model allow-lists and model lists;
- image-generation and `/v1/messages` eligibility (eligibility by the origin group, capability by the fallback group);
- group RPM and user-by-group counters;
- API key quota and balance checks.

Reservation ownership and counters MUST belong to the origin group.

#### Scenario: Model not allowed in the origin group

- **WHEN** the origin group does not allow the requested model and the fallback group does
- **THEN** the request is rejected as if the origin group had served it.

### Requirement: Display

- User-facing pages MUST keep showing the origin group and MUST NOT reveal the fallback group. Rate and charge MUST show the actual values.
- The admin usage table MUST show the origin group with a yellow "fallback → <fallback group>" line below it. Its tooltip MUST say the request was billed by the fallback group.
- Admin filters, statistics and exports MUST stay by serving group.

#### Scenario: User views a fallback request

- **WHEN** a user's request was served by a fallback group with a different rate
- **THEN** the user's usage list shows the origin group with the actual rate and charge.

### Requirement: Profit gate cost and price

- The profit gate cost MUST be `openAISchedulingRate` frozen at the gate's pricing time. That is a fresh probe value including the upstream peak; otherwise the account's `rate_multiplier`; for OAuth accounts, the existing runtime reference rate.
- Startup settings loading MUST warm the existing OAuth reference cache with the shared bounded loader, including instances that never run OpenAI scheduling. Profit checks MUST remain cache-only.
- A nil, negative, NaN or infinite raw multiplier MUST be rejected and MUST NOT default to 1.0. Zero remains a valid free rate.
- The price MUST come from the pricing group, matching billing. Under fallback that is the fallback group; otherwise the key's group, which is the parent for composite requests. The price is the user's override or the group rate, times that group's peak factor.
- The switch, margin and buffer MUST come from the scheduled group.
- The guard, the servable check and the preview MUST estimate with the group's default rate.
- Enabling the profit gate MUST default the margin to 20%. The UI MUST hide the buffer and save it as 0.
- `GET /api/v1/admin/groups/:id/profit-control-preview?margin=` MUST return member, schedulable and passing counts and the derived upstream rate ceiling at the current time. The UI MUST show the ceiling with the counts, show zero passing in red and ask for confirmation on save, and show one passing in yellow.
- The preview MUST use the request margin (or the saved margin when omitted) and a zero safety buffer, without persisting either. The member count includes disabled members. Schedulable members exclude hard states and soft penalties; passing counts the subset admitted by profit alone, without quality ejection.
- The margin MUST stay the configured quantity; the ceiling is derived and displayed, not stored.
- The profit gate MUST NOT admit loss-making accounts, even when it blocks the whole group.

#### Scenario: Missing account cost

- **WHEN** an account's raw `rate_multiplier` is nil and no fresh probe value exists
- **THEN** the profit gate rejects the account instead of treating its cost as 1.0.

#### Scenario: Mixed and composite member eligibility

- **WHEN** a group contains healthy concrete-platform members, including mixed-scheduling Antigravity members in Anthropic/Gemini groups or concrete members in a composite group
- **THEN** fallback, operations counts, the guard denominator and preview reuse scheduler platform eligibility without excluding those members solely for platform inequality. With both gates disabled, composite availability stays consistent with the previous version.

## REMOVED Requirements

### Requirement: Origin-group pricing for fallback requests

Pricing fallback requests at the origin group's prices, introduced in 0.4.6, MUST be removed. This includes the standard-tier charge absorbed by the platform when priority came only from the fallback group's forced Fast. Historical usage records MUST NOT be repriced.
