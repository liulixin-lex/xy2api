export default {
  groupQualityGate: {
    title: 'Quality gate',
    hint: 'Takes accounts whose error rate or warm first-token latency exceed the limits out of this group\'s scheduling for a while. When the ejection ends they return on trial, then fully. Account schedulability and group membership are not changed.',
    enable: 'Enable quality gate',
    disabledHint: 'While off, account selection in this group works exactly as before.',
    loading: 'Loading gate settings…',
    loadFailed: 'Could not load gate settings: {message}',
    retry: 'Retry',
    maxErrorRate: 'Error rate limit (%)',
    maxErrorRateHint: 'Soft failures in the last 15 minutes. Default 10%',
    maxWarmTtft: 'Warm first-token limit (s)',
    maxWarmTtftHint: 'Warm first-token p90 over 30 minutes. Default 20 s',
    fastWarmTtft: 'Fast tier line (s)',
    fastWarmTtftHint: 'Used for ordering when low-rate priority is on. Default 10 s',
    pinned: 'Pinned accounts',
    pinnedHint: 'Pinned accounts keep serving this group while ejected; their state still reflects real metrics. Only members of this group can be pinned.',
    membersLoading: 'Loading group members…',
    membersEmpty: 'This group has no OpenAI accounts yet. Add members to pin them.',
    membersFailed: 'Could not load group members: {message}',
    errorRateRange: 'The error rate limit must be above 0 and at most 100%',
    maxTtftRange: 'The limit must be above 1 s and at most 600 s',
    fastTtftMin: 'The fast tier line must be at least 1 s',
    fastTtftBelowMax: 'The fast tier line must be below the warm first-token limit',
    fixErrors: 'Fix the quality gate settings first',
    groupSavedGateConflict: 'The group was saved, but the gate was not: someone else changed the gate settings. Refresh and set them again.',
    groupSavedGateFailed: 'The group was saved, but the gate was not: {message}',
    column: 'Gate',
    summaryTitle: '{trial} on trial',
    summaryFailed: 'Could not load gate status',
    pinnedMark: 'Pinned',
    state: {
      eligible: 'OK',
      ejected: 'Ejected',
      ejectedUntil: 'Ejected until {time}',
      trial: 'Trial',
      guardHeld: 'Kept for floor'
    },
    reason: {
      error_rate: 'error rate over limit',
      warm_ttft: 'warm first token over limit'
    },
    listSeparator: ', ',
    tooltip: {
      state: '{group}: {state}',
      pinned: 'Pinned: keeps serving this group while ejected',
      guardHeld: 'Over the limits, but kept in scheduling so the group keeps its minimum of usable accounts',
      strikes: 'Ejection {count} in a row',
      reasons: 'Reason: {reasons}',
      errorRate: 'Error rate {value}, limit {limit}',
      errorRateNoData: 'Error rate: no data yet, limit {limit}',
      warmTtft: 'Warm first-token p90 {value}, limit {limit}',
      warmTtftNoData: 'Warm first-token p90: no data yet, limit {limit}',
      headroom: 'Headroom {value}',
      stale: 'Evaluation data is more than 5 minutes old; the previous state is kept'
    },
    metrics: {
      errorRate: 'Errors {value}',
      errorRateTitle: '{calls} calls in the last 15 minutes, {failures} soft failures',
      warmTtft: 'Warm TTFT p90 {value}',
      warmTtftTitle: '{count} warm streaming samples in the last 30 minutes',
      zeroCache: 'Zero cache {value}',
      zeroCacheTitle: 'Share of warm requests (prompt ≥ 2048 tokens) in the last 2 hours that read no cache, {count} samples. Above 30% the account is not fast tier.'
    }
  }
}
