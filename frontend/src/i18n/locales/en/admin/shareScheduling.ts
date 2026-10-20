export default {
  shareScheduling: {
    title: 'Share Scheduling',
    // Page header subtitle.
    subtitle: 'Tiers accounts by priority, splits new sessions by load factor and keeps sessions on their account to protect the cache',
    global: {
      title: 'Main switch',
      hint: 'Off sends every group back to the original scheduler at once; group settings are kept.',
      onTitle: 'Turn on share scheduling',
      onBody: 'Groups set to Share start running share scheduling at once.',
      offTitle: 'Turn off share scheduling',
      offBody: 'Every group returns to the original scheduler at once. Existing sessions are not moved and group settings are kept.',
      turnOn: 'Turn on',
      turnOff: 'Turn off',
      cancel: 'Cancel',
      saving: 'Saving…',
      failed: 'Failed to save the main switch.',
      loadFailed: 'Failed to read the main switch.',
      reload: 'Reload'
    },
    accountRules: {
      title: 'Share scheduling',
      share: 'Expected new sessions {value}',
      notEffective: 'not in effect',
      open: 'Open',
      hint: 'Share groups tier accounts by priority and split new sessions by load factor; changing either affects every group the account serves.'
    },
    accountDisabled: '{name} stopped taking requests: its new sessions in {groups} now go to the other accounts of the same priority, by load factor.',
    accountColumn: {
      header: 'Share',
      hint: 'Expected share of new sessions in the share group selected in the filter.'
    },
    schedulers: {
      sub2api: 'Original',
      share: 'Share'
    },
    schedulerHints: {
      sub2api: 'Picks by priority and load (Sub2API original).',
      share: 'Tiers by priority; within a priority, new sessions are split by load factor and sessions stay on their account to keep the cache.'
    },
    groups: {
      title: 'Groups',
      search: 'Search groups',
      empty: 'No groups to configure.',
      noMatch: 'No matching groups.',
      loadFailed: 'Failed to load groups.',
      retry: 'Retry',
      selectHint: 'Select a group on the left.',
      active: 'In use',
      idle: 'Not enabled',
      members: '{count} accounts',
      cacheHit: 'Cache hit {value}',
      switchLoss: 'Lost to switching {value}',
      invalid: 'Settings unreadable',
      notEffective: 'Not in effect'
    },
    mode: {
      title: 'Scheduler',
      sessionKeep: 'Session keeping',
      sessionKeepHint: 'On keeps a session on the same account and re-splits it once its cache expires; turn it off only for stateless groups such as images or embeddings.',
      switchToShare: 'Switch to share scheduling',
      rollback: 'Back to original',
      updated: 'Version {version} · updated {time}',
      neverSaved: 'Not set yet; the group uses the original scheduler.',
      notEffective: 'Main switch off: this group runs the original scheduler for now.',
      legacyShadow: 'This group used the shadow mode, which was removed; it now runs the original scheduler.',
      invalid: 'The stored settings cannot be read ({reason}); the group runs the original scheduler. Choosing a scheduler again overwrites them.',
      conflict: 'Another administrator changed this group.',
      reload: 'Reload',
      loadFailed: 'Failed to load the group settings.',
      unavailable: 'Share scheduling storage is unavailable; try again later.'
    },
    change: {
      titleShare: 'Switch to share scheduling',
      titleSub2api: 'Back to the original scheduler',
      titleKeepOn: 'Turn on session keeping',
      titleKeepOff: 'Turn off session keeping',
      shareBody: 'Takes effect at once: new sessions are split as below; existing sessions stay on their account and are re-split once their cache expires.',
      sub2apiBody: 'Takes effect at once: the group returns to the original scheduler. Existing sessions are not moved.',
      keepOnBody: 'Sessions stay on the same account and are re-split once their cache expires.',
      keepOffBody: 'Every request is re-split, so prompt caches will rarely hit. Use this only for stateless groups such as images or embeddings.',
      enableGlobal: 'The main switch is off and will be turned on too; other groups set to Share start running as well.',
      previewing: 'Working out the expected shares…',
      previewFailed: 'Could not work out the expected shares: {error}',
      expected: 'Expected share of new sessions',
      risks: 'Risks',
      noRisks: 'No risks found.',
      columns: {
        account: 'Account',
        priority: 'Priority',
        loadFactor: 'Load factor',
        share: 'Expected share'
      },
      others: '{count} more accounts expect no new sessions (standby or unavailable).',
      base: 'unadjusted {value}',
      confirm: 'Confirm',
      cancel: 'Cancel',
      saving: 'Saving…',
      failed: 'Save failed: {error}'
    },
    risks: {
      global_switch_off: 'The main switch is off; this group runs the original scheduler for now.',
      no_healthy_account: 'No available account can take new sessions; requests can only use the last resort.',
      top_priority_unhealthy: 'Priority {priority} has no available account; new sessions go to priority {value}.',
      single_account_dominant: '{name} is expected to take {value} of new sessions; traffic is too concentrated.',
      high_load_factor: '{name} has load factor {value} and takes most new sessions of its priority whenever available.',
      health_downweighted: '{name} is down-weighted automatically (×{value}); if it lasts, check the upstream or disable the account.',
      ramping: '{name} just came back and is ramping up (×{value}); its new sessions return to full weight within about 10 minutes.'
    },
    health: {
      title: 'Health',
      window: 'Window',
      windows: {
        h1: '1 hour',
        h6: '6 hours',
        h24: '24 hours'
      },
      refresh: 'Refresh',
      levels: {
        ok: 'Healthy',
        watch: 'Needs attention',
        rollback: 'Go back to original',
        insufficient: 'Not enough data'
      },
      insufficient: {
        requests: 'Too few requests (each window needs at least {min}) to judge yet.',
        baseline: 'No baseline to compare with yet.',
        window: 'Only {minutes} minutes of data since the switch (at least {min} needed) to judge yet.'
      },
      truncated: 'Only {minutes} minutes of data since the switch.',
      baselineUnavailable: {
        never_switched: 'This group has never switched, so there is nothing to compare with.',
        out_of_retention: 'Cache attribution before the switch is past retention (about 8 days); cache lost to switching cannot be compared.'
      },
      sub2api: 'Running the original scheduler; these are the current figures. After switching to share, they are compared with a window of the same length before the switch.',
      baseline: 'Compared with {from} – {to} before the switch.',
      metrics: {
        cacheHit: 'Cache hit ratio',
        switchLoss: 'Cache lost to switching',
        ttft: 'First token',
        errors: 'Error rate'
      },
      parts: {
        p50: 'Typical',
        p95: 'Slow'
      },
      referenceOnly: 'Moves with the time of day and model mix; for reference only, never a reason to go back.',
      vsBaseline: 'baseline {value}',
      noBaseline: 'no baseline',
      calibrating: 'Thresholds are initial values, pending calibration on production data.',
      loadFailed: 'Failed to load the health data.',
      anchorEstimated: 'The switch time is estimated from the policy update time.',
      hourRounded: 'The baseline and windows over 2 hours are summed by whole hours and may differ from the exact window by up to 30 minutes.',
      countersError: 'Some counters failed to load: {error}'
    },
    attention: {
      title: 'Needs attention',
      none: 'Nothing needs attention.'
    },
    traffic: {
      title: 'Traffic split',
      hintShare: 'Lower priority numbers are used first; within a priority, new sessions are split by load factor.',
      hintSub2api: 'Running the original scheduler: the expected shares show what share scheduling would do.',
      priority: 'Priority {priority}',
      active: 'Takes new sessions',
      standby: 'Standby: used when the priority above is unavailable or full',
      columns: {
        account: 'Account',
        status: 'Status',
        loadFactor: 'Load factor',
        expected: 'Expected new sessions',
        actual: 'Actual share',
        failure: 'Failure rate'
      },
      actualHint: 'Share of the group’s successful requests the account served in the last hour.',
      failureHint: 'Last 30 minutes, counting only upstream failures that trigger down-weighting (transient errors, first output timeouts, stalls), over the account’s requests in every group.',
      factors: {
        health: 'Health ×{value}',
        ramp: 'Ramping ×{value}, full weight in about {minutes} min',
        rampNoTime: 'Ramping ×{value}',
        insufficient: 'Not enough data (not down-weighted)',
        breakdown: 'Load factor {loadFactor} → health ×{health} → ramp ×{ramp} → weight {weight} → expected {share} (unadjusted {base})',
        healthReason: 'Upstream failures {rate} over the last {minutes} minutes, lowest in the priority {baseline}; recovers within about {minutes} minutes as failures fall.',
        insufficientReason: 'Too few samples over the last {minutes} minutes; not down-weighted.',
        rampReasons: {
          balance_pause: 'Balance pause ended {since} min ago; ',
          hard_penalty: 'Rate limit or pause ended {since} min ago; ',
          reenabled: 'Available again {since} min ago; '
        },
        rampRemaining: 'full weight in about {minutes} min.'
      },
      ifHealthy: '{value} when back',
      healthy: 'Schedulable',
      recoverAt: 'Back at {time}',
      empty: 'This group has no accounts yet.',
      manageAccounts: 'Manage accounts'
    },
    loadFactor: {
      edit: 'Edit the load factor of {name}',
      fromConcurrency: 'Unset; counts as the concurrency limit {value}',
      alsoAffects: 'Also affects: {groups}',
      otherGroups: '+{count} groups',
      clearHint: 'Leave empty to clear and use concurrency',
      invalid: 'The load factor must be a whole number from 1 to {max}; leave empty to clear.',
      failed: 'Failed to save the load factor.',
      saved: 'Load factor of {name} set to {value}.',
      cleared: 'Load factor of {name} cleared; concurrency is used.'
    },
    details: {
      title: 'Detailed report',
      cacheClasses: 'Cache hits and misses (by input tokens)',
      reasons: 'Selection reasons',
      skipped: 'Why accounts were passed over',
      shareOnly: 'The figures below exist in share mode only.',
      hotMigrationsActive: 'Warm sessions share scheduling moved itself: {count} ({ratio} of requests)',
      hotMigrationsOver: 'above the rollback line ({limit})',
      hotMigrationsTotal: 'Sessions switched while the cache was warm: {count} (including passive causes such as unavailable accounts and failovers)',
      hotMigrationReasons: 'Why they switched',
      waits: 'Waited for a full account {count} times, got it {hit} times, {avg} on average.',
      attempts: '{count} account attempts over {requests} selections.',
      sampledNote: 'Cache attribution samples 1 in 4 sessions.',
      noData: 'No data in this window.'
    },
    explain: {
      title: 'Troubleshooting',
      hint: 'Simulates one selection with the current settings and account state, without taking a slot or writing a session binding.',
      simulate: 'Simulate a new session',
      running: 'Working…',
      more: 'More inputs (model, session hash, request sample)',
      model: 'Model',
      sessionHash: 'Session hash (optional)',
      sessionHashHint: 'The session hash stored in the binding; empty means a new session.',
      estimatedTokens: 'Estimated input tokens (optional)',
      cacheOneHour: 'Request declares a 1-hour cache',
      body: 'Request sample (optional, JSON)',
      bodyHint: 'Paste a request body to estimate input tokens and detect a 1-hour cache; it overrides the token estimate above.',
      bodyInvalid: 'The request sample is not valid JSON.',
      run: 'Simulate with these inputs',
      failed: 'Simulation failed.',
      summary: {
        random: 'This is a random new session: each simulation lands by load factor share and may differ.',
        target: 'Goes to {account} (priority {priority}).',
        bound: 'The session is bound to {account}.',
        none: 'No account is available; the request cannot be placed.',
        fallback: 'If it is full or unavailable, tries in turn: {list}.'
      },
      details: 'Full result',
      result: {
        reason: 'Selection reason',
        keySource: 'Session identification',
        session: 'Session fingerprint',
        bound: 'Current binding',
        none: 'None',
        idle: 'Idle for',
        cacheTTL: 'Cache lifetime',
        cache: 'Cache state',
        notCacheable: 'Not cacheable',
        warm: 'Still cached',
        cold: 'Expired',
        waitBudget: 'Longest wait when full',
        write: 'Session binding',
        spill: 'When borrowing another account',
        target: 'Target account',
        ideal: 'Account by share',
        entries: 'Candidates'
      },
      entryColumns: {
        rank: 'Rank',
        account: 'Account',
        priority: 'Priority',
        weight: 'Load factor',
        healthFactor: 'Health',
        rampFactor: 'Ramp',
        effective: 'Weight',
        loadRate: 'Load rate',
        state: 'Eligibility'
      },
      saturated: 'Full',
      eligible: 'Available',
      excluded: 'Excluded',
      writes: {
        none: 'No write',
        refresh: 'Refresh',
        if_absent: 'Create if absent',
        replace: 'Rebind'
      }
    },
    keySources: {
      key_continuation: 'Protocol continuation',
      key_session: 'Session ID',
      key_anchor: 'Conversation anchor',
      key_none: 'No session'
    },
    reasons: {
      owner_continuation: 'Protocol continuation',
      affinity_hit: 'Session kept',
      affinity_wait: 'Kept after waiting',
      spill_rebind: 'Switched (full)',
      soft_spill: 'Briefly unavailable, borrowed',
      new_session: 'New session',
      sessionless: 'No session',
      realign_cold: 'Re-split after cache expired',
      return_cold: 'Back to higher priority after cache expired',
      rebind_unavailable: 'Bound account unavailable',
      tier_overflow_saturated: 'Priority full, next priority',
      tier_overflow_unavailable: 'Priority unavailable, next priority',
      failover_next: 'Failover',
      model_routing: 'Model routing',
      model_routing_fallback: 'Model routing fallback',
      resil_failure_streak: 'Rebound after repeated session failures',
      last_resort_soft: 'Last resort (soft-penalized accounts)',
      disabled: 'Disabled',
      inactive: 'Inactive',
      not_schedulable: 'Not schedulable',
      expired: 'Expired',
      rate_limited: 'Rate limited',
      overloaded: 'Overload cooldown',
      temp_unschedulable: 'Temporarily unschedulable',
      balance_exhausted: 'Upstream balance exhausted',
      quota_exceeded: 'Quota exceeded',
      model_unsupported: 'Model not supported',
      model_rate_limited: 'Model rate limited',
      window_cost: 'Window cost limit',
      rpm_limit: 'RPM limit',
      session_limit: 'Session limit',
      iq_degraded: 'IQ check degraded',
      quota_auto_pause: 'Quota auto-paused',
      shadow_parent_unhealthy: 'Shadow parent unhealthy',
      grok_quota: 'Grok quota',
      channel_restricted: 'Channel restricted',
      profit_gate: 'Profit gate',
      privacy_required: 'Privacy mode required',
      platform_mismatch: 'Platform mismatch',
      transport_mismatch: 'Transport mismatch',
      capability_mismatch: 'Capability mismatch',
      runtime_blocked: 'Runtime blocked',
      proxy_quarantined: 'Proxy quarantined',
      scheduling_threshold: 'Scheduling threshold',
      excluded_in_request: 'Already failed in this request',
      saturated: 'Full',
      codex_pressure: 'Codex quota pressure',
      ineligible: 'Unavailable'
    },
    cacheClasses: {
      cache_hit: 'Cache hit',
      below_min_cacheable: 'Below minimum cacheable length',
      miss_sessionless: 'No session',
      miss_new_session: 'New session',
      miss_ttl_expired: 'Cache expired',
      miss_switch_policy: 'Switched by share',
      miss_switch_saturated: 'Switched (full)',
      miss_switch_unavailable: 'Switched (unavailable)',
      miss_switch_failover: 'Switched (failover)',
      miss_switch_other: 'Switched (other)',
      miss_prefix_drift: 'Prefix changed'
    },
    settings: {
      title: 'Share Scheduling',
      hint: 'Share scheduling tiers accounts by priority and splits new sessions by load factor. Each group’s scheduler is set on the Share Scheduling page.',
      openPage: 'Open share scheduling'
    }
  }
}
