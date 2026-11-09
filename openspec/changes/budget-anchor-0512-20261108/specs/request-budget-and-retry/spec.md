# Request budget and retry delta

## MODIFIED Requirements

### Requirement: One budget per request

Text requests MUST use D=min(anchor+base,arrival+290 s). The default anchor MUST be arrival. With budget_start=body_eof, the first successful gateway-observed EOF MUST be used only when at least 1 s after arrival and before the first attempt. The anchor MUST affect only D. spent and the first lastMark MUST remain arrival-based, including the unchanged ≥60 s account-level failure rule.

#### Scenario: Default compatibility
- GIVEN arrival / observe
- THEN deadlines, retry decisions, billing and penalties MUST match 0.5.11; additional observation fields MAY differ.

#### Scenario: Slow upload
- GIVEN base=240 s and EOF at arrival+100 s in body_eof mode
- THEN D=arrival+290 s and 190 s remain after EOF.
- GIVEN EOF at arrival+30 s
- THEN D=EOF+240 s.
- GIVEN EOF at arrival+0.5 s
- THEN anchor remains arrival.

#### Scenario: Unchanged clocks and request kinds
- GIVEN an image request or nil budget
- THEN EOF MUST NOT add a deadline.
- GIVEN body_eof mode
- THEN raise-to-spent and the ≥60 s failure decision MUST still include time since arrival.

#### Scenario: Existing timer boundaries
- GIVEN a path with no single-attempt timer
- THEN in body_eof mode, no-timer attempts and attempts whose timer coverage cannot be determined MUST use the arrival upper bound T0+base−2 s for admission and waits.
- THEN an attempt with an installed timer MUST continue using D, and a running attempt without a timer MUST NOT gain a request-level cancellation.
- GIVEN a replayable Gemini non-streaming attempt
- THEN its existing attempt timer MUST remain active and the last-candidate exception MUST remain unchanged.
