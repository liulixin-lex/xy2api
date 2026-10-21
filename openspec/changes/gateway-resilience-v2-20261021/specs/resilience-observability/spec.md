# 韧性事件字段（0.4.4）

## ADDED Requirements

### Requirement: Correlatable resilience events

The log events `resilience.post_output_failure`, `resilience.session_rebind`, `resilience.last_resort`, `resilience.keepalive_committed` and `resilience.turn_cap` MUST carry the request ID shared with the operations records and access logs (`request_id`), the gateway's local request ID (`gateway_request_id`) and the model, and, where known, the delivered semantic bytes, the milliseconds since the first output, the event type that released staged prelude and its milliseconds after `response.created`. They MUST NOT carry request or response content, API keys or full session values. No table or migration is added.

#### Scenario: Post-output failure after a released prelude
- **WHEN** a native Responses stream releases staging on a metadata event and later fails
- **THEN** `resilience.post_output_failure` carries `request_id`, `gateway_request_id`, the model, the delivered bytes, the release event type and its offset from `response.created`.

#### Scenario: Unknown fields
- **WHEN** a request fails before any prelude was staged
- **THEN** the release fields are empty and the other fields are present.
