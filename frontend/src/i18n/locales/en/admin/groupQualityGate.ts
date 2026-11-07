export default {
  groupQualityGate: {
    title: 'Quality gate',
    hint: 'Accounts are ranked in three tiers by this group\'s standards. Accounts below standard go last; none are removed or paused. Sessions on an account that is below standard or held back as slow move to a qualified account in this group with spare capacity. Recovery requires consecutive checks meeting the recovery threshold: 15 for first token, 2 each for errors and cache. After a restart, the system tries to rebuild error and cache states and recent failure flags from the last 6 hours of data; first token assessments start afresh.',
    enable: 'Enable quality gate',
    disabledHint: 'While the gate is off, this group has no tiers and accounts judged slow go to the end of the candidates.',
    loading: 'Loading gate settings…',
    needsAccountView: 'Configuring the quality gate requires account view permission',
    loadFailed: 'Could not load gate settings: {message}',
    retry: 'Retry',
    maxTtft: 'First-token limit (s)',
    maxTtftHint: 'P90 first token in this group stays at or below this; whole seconds 5–60, default 15',
    maxErrorRate: 'Error limit (%)',
    maxErrorRateHint: 'Above this failure rate with at least 5 failures is below standard; 5–50%, default 10%',
    minCacheKeep: 'Cache floor (%)',
    minCacheKeepHint: 'A cache keep rate clearly below this is below standard; 50–70%, default 50%',
    strict: 'Strict mode',
    strictHint: 'Accounts with any dimension undecided always sit in the "Retest" tier, and sessions only move to accounts measured on all three dimensions and within every recovery line.',
    maxTtftRange: 'The first-token limit must be a whole number from 5 to 60',
    maxErrorRateRange: 'The error limit must be between 5% and 50%',
    minCacheKeepRange: 'The cache floor must be between 50% and 70%',
    fixErrors: 'Fix the quality gate settings first',
    groupSavedGateConflict: 'The group was saved, but the gate was not: someone else changed the gate settings. Refreshed to the latest version; review and save again.',
    groupSavedGateFailed: 'The group was saved, but the gate was not: {message}',
    column: 'Gate',
    summaryTitle: '{counts}; {composition}',
    groupSamplesShort: 'Too few group samples',
    unknownBadgeTitle: '{group} · Unknown: no verdict yet (too few samples or in the middle band), ranked as tier 0 for now',
    pinnedMark: 'Pinned',
    state: {
      passing: 'Passing',
      unknown: 'Unknown',
      retest: 'Retest',
      failing: 'Failing',
      pinned: 'Pinned',
      pending: 'Awaiting evaluation',
      failed: 'Load failed'
    },
    dimension: {
      pass: 'Pass',
      fail: 'Fail',
      none: 'Undecided'
    },
    dimensionName: {
      ttft: 'First token',
      error: 'Errors',
      cache: 'Cache'
    },
    tooltip: {
      title: '{group} · {state}',
      strict: 'Strict',
      pinned: 'Pinned account: its tier in this group stays 0; the table shows its actual state. Slow-account hold and acute protection still apply.',
      dimension: 'Metric',
      stateColumn: 'State',
      measured: 'Measured',
      samples: 'Samples',
      limitValue: '{limit}',
      ttft: 'TTFT P90',
      errorRate: 'Errors',
      cacheKeep: 'Cache kept',
      noData: '—',
      groupSamplesShort: 'Group too thin',
      groupSamplesInsufficient: 'First token: too few group samples ({count}/50)',
      composition: 'Group mix P90 {value} ({count} samples)',
      factor: 'factor {value}',
      interval: 'range {range}',
      ttftSamples: '{samples} · {sessions} sess.',
      badMemory: 'Recent failure: {items}',
      badMemoryItem: '{dimension} until {time}',
      separator: ', ',
      stale: 'Evaluation data is stale or could not be read; this round was skipped and the previous state is kept',
      pending: 'Evaluated every minute',
      failed: 'Refresh the list to retry'
    },
    latencyHold: {
      text: 'Held back as slow: {models}, until {time}',
      note: 'When it ends the account only stops ranking last; its gate state does not change'
    },
    indicator: {
      attention: 'Quality gate: {count} groups need attention',
      hold: 'Quality gate: held back as slow',
      ok: 'Quality gate: nothing needs attention'
    },
    alert: {
      ttft: 'First token {value}',
      error: 'Err {value}',
      cache: 'Cache {value}',
      latencyHold: 'Slow until {time}',
      title: '{dimension} failing in {count} gated groups'
    }
  }
}
