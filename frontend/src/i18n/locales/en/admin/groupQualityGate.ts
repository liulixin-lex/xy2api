export default {
  groupQualityGate: {
    title: 'Quality gate',
    hint: 'Takes over-limit accounts out of this group for a while, then observes them for 30 minutes. The fast tier line orders low-rate priority and moves sessions off slow accounts in gated groups.',
    enable: 'Enable quality gate',
    disabledHint: 'While the gate is off, it does not affect account selection in this group.',
    loading: 'Loading gate settings…',
    needsAccountView: 'Configuring the quality gate requires account view permission',
    loadFailed: 'Could not load gate settings: {message}',
    retry: 'Retry',
    maxErrorRate: 'Error rate limit (%)',
    maxErrorRateHint: 'Default 10%',
    maxWarmTtft: 'Warm visible first-token limit (s)',
    maxWarmTtftHint: 'Default 20 s',
    fastWarmTtft: 'Fast tier threshold (s)',
    fastWarmTtftHint: 'Default 10 s; in effect at most the group\'s fastest + 4 s',
    pinned: 'Pinned accounts',
    pinnedHint: 'Keep serving this group while ejected',
    pinnedCount: '{count} selected',
    pinnedSearch: 'Search by name or #ID',
    pinnedNoMatch: 'No matching members',
    membersLoading: 'Loading group members…',
    membersEmpty: 'This group has no OpenAI accounts yet. Add members to pin them.',
    membersFailed: 'Could not load group members: {message}',
    errorRateRange: 'The error rate limit must be above 0 and at most 100%',
    maxTtftRange: 'The limit must be above 1 s and at most 600 s',
    fastTtftMin: 'The fast tier line must be at least 1 s',
    fastTtftBelowMax: 'The fast tier line must be below the warm visible first-token limit',
    fixErrors: 'Fix the quality gate settings first',
    groupSavedGateConflict: 'The group was saved, but the gate was not: someone else changed the gate settings. Refreshed to the latest version; review and save again.',
    groupSavedGateFailed: 'The group was saved, but the gate was not: {message}',
    column: 'Gate',
    summaryTitle: '{trial} under observation',
    pinnedMark: 'Pinned',
    state: {
      eligible: 'OK',
      ejected: 'Ejected',
      ejectedUntil: 'Ejected until {time}',
      trial: 'Observing',
      trialUntil: 'Observing until {time}',
      guardHeld: 'Held by floor guard',
      pending: 'Awaiting evaluation',
      failed: 'Load failed'
    },
    reason: {
      error_rate: 'error rate over limit',
      warm_ttft: 'warm visible first token over limit'
    },
    tooltip: {
      title: '{group} · {state}',
      strikes: ' (strike {count})',
      pinned: 'Pinned, keeps serving while ejected',
      guardHeld: 'Kept to hold the minimum of usable accounts',
      metric: 'Metric',
      measured: 'Measured',
      limit: 'Limit',
      ejectionWindow: 'at ejection',
      errorRate: 'Error rate',
      warmTtft: 'Visible first token p90',
      tierFast: 'Fast tier',
      tierOther: 'Other tier',
      fastLine: '{tier}, current fast line {line}',
      fastLineNoBest: '{tier}, current fast line {line} (too few samples in group)',
      noData: 'No data',
      notEnoughSamples: 'Too few samples',
      belowThreshold: 'below threshold',
      headroom: 'Headroom {value}',
      stale: 'Not evaluated for 5+ min, previous state kept',
      pending: 'Evaluated every minute',
      failed: 'Reload the list to retry'
    },
    indicator: {
      attention: 'Quality gate: {count} groups need attention',
      ok: 'Quality gate: no issues'
    },
    panel: {
      eligible: 'Eligible {count}',
      window: 'Account overall'
    },
    metrics: {
      errorRate: 'Err {value}',
      errorRateTitle: 'Error rate (last 15 min): {calls} calls, {failures} soft failures',
      warmTtft: 'Visible TTFT {value}',
      warmTtftTitle: 'Warm user-visible first-token p90 (last 30 min): {count} streaming samples',
      zeroCache: 'No-cache {value}',
      zeroCacheTitle: 'Warm zero-cache rate (last 2 h, prompt ≥ 2048 tokens): {count} samples. Above 30% the account is not fast tier.'
    }
  }
}
