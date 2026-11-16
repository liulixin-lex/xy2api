Rule B fixtures contain 44 assessments for each of three parameter settings and
two additional fixed contexts evaluated at all three settings (138 assessments).
Expected values come from the frozen reference-engine-checked vector outputs,
not from this Go implementation. Scheduler identities are deliberately omitted:
these tests check the assessment, not the state machine.

`rule_b_vectors.json.gz` is deterministic gzip (mtime 0) of compact JSON:

- `Samples`: unique `RuleBSample` values.
- `Cases`: name, parameters, scope, relative time, cutoff, target limit, target
  and prior-group sample indices, full peer membership/identity/epoch/indices,
  and expected counts, priors, Beta parameters, probabilities, gates and state.
- `S` and `D` are the reference engine's session counts capped at 3 and 2.
- Null peer posterior/probability fields mean fewer than 20 known peer mass.

All accounts and groups use synthetic numeric IDs, models and sessions use
synthetic names, and sample references are zero-based local indices. Times remain
relative seconds; original timestamps, request IDs, account IDs, model names,
session hashes and row IDs are excluded. Samples already meet the frozen
preparer's evidence contract; hand-written tests separately exercise filtering
and inclusive time/limit boundaries.

The untracked conversion tool lives outside the repository at
`质量门槛分析/实施/工具/ruleb/convert_vectors.py` in the analysis workspace.
