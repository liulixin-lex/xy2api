Rule B fixtures retain 20 default, 10 wide and 10 narrow assessments, plus both
fixed contexts evaluated at all three settings (46 assessments, 274,137 bytes).
Expected values come from the frozen reference-engine-checked vector outputs,
not from this Go implementation. Scheduler identities are deliberately omitted:
these tests check the assessment, not the state machine.

`rule_b_vectors.json.gz` is deterministic gzip (mtime 0) of compact JSON:

- `Samples`: columns for 16,158 unique, referenced `RuleBSample` values. Identity,
  measurement and flag columns retain their original types and values.
- `AvailableDeltaNS`: signed nanoseconds since the preceding sample's available
  time (the first delta is relative to zero). `StartedBeforeAvailableNS`: available
  time minus start time, also in nanoseconds. Accumulate using int64, subtract the
  start offset, then divide each timestamp by 1e9 to reconstruct seconds. The
  converter checks exact float64 round trips; no timestamp precision is removed.
- `Cases`: name, parameters, scope, relative time, cutoff, target limit, target
  and prior-group sample indices, full peer membership/identity/epoch/indices,
  and expected counts, priors, Beta parameters, probabilities, gates and state.
- `S` and `D` are the reference engine's session counts capped at 3 and 2.
- Null peer posterior/probability fields mean fewer than 20 known peer mass.

Retained reference coverage (the test asserts each feature remains present):

| Feature | Example |
| --- | --- |
| No usable peer evidence; N=0; prior fallback | default/22 |
| Known peer mass below 20 | default/39 |
| Insufficient own mass/sessions; absolute or relative confirmation fails | default/02 |
| One slow session alone vetoes bad | wide/32 |
| Own unknown results prevent a good assessment | default/43 |
| Prior clipping; proven-slow censoring | default/23 |
| Padding changes both probabilities | default/02 |
| Peer unknown results count as bad | default/17 |
| Good | default/21 |
| Bad/good/unknown with identical inputs; relative veto | both three_states contexts |

The existing hand-written tests also retain empty membership, all-rear peers,
upper prior clipping, invalid parameters, exact time/limit/known-mass boundaries,
and the other edge cases not represented by the archived checkpoint contexts.

All accounts and groups use synthetic numeric IDs, models and sessions use
synthetic names, and sample references are zero-based local indices. Times remain
relative seconds; original timestamps, request IDs, account IDs, model names,
session hashes and row IDs are excluded. Samples already meet the frozen
preparer's evidence contract; hand-written tests separately exercise filtering
and inclusive time/limit boundaries.

All 138 original assessments passed before reduction (maximum absolute error
1.3642420526593924e-12, relative error 3.9702446755774805e-13). The full fixture and
validation log remain outside the repository at `质量门槛分析/实施/工具/ruleb/`
as `full_rule_b_vectors.json.gz` and `full_vectors_validation.log`; the full fixture
is also recoverable from commit a8209c027. `convert_vectors.py` regenerates that archive;
`shrink_vectors.py` uses `subset_selection.json` to produce this subset and writes
`subset_manifest.json` with coverage witnesses and SHA-256 hashes. It verifies
that every selected sample, input (including excluded peers) and expectation
matches the full archive exactly, and that no unreferenced samples remain.
