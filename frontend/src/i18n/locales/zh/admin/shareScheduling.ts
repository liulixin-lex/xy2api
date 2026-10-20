export default {
  shareScheduling: {
    title: '份额调度',
    // 页头副标题。
    subtitle: '按账号优先级分层、按负载因子分配新会话，并尽量把会话留在原账号以保住缓存',
    global: {
      title: '总开关',
      hint: '关闭后所有分组立即回到原版调度，各分组的设置保留。',
      onTitle: '开启份额调度总开关',
      onBody: '开启后，调度方式为“份额”的分组立即按份额调度运行。',
      offTitle: '关闭份额调度总开关',
      offBody: '关闭后所有分组立即回到原版调度；已有会话不迁移，各分组的设置保留。',
      turnOn: '开启',
      turnOff: '关闭',
      cancel: '取消',
      saving: '正在保存…',
      failed: '总开关保存失败。',
      loadFailed: '无法读取总开关状态。',
      reload: '重新读取'
    },
    accountRules: {
      title: '份额调度',
      share: '预计新会话 {value}',
      notEffective: '未生效',
      open: '打开',
      hint: '份额分组按账号优先级分层、按负载因子分配新会话；修改优先级或负载因子会同时影响该账号所在的全部分组。'
    },
    accountDisabled: '{name} 已停止调度：它在 {groups} 的新会话将按负载因子分给同一优先级的其他账号。',
    accountColumn: {
      header: '份额',
      hint: '按当前筛选的份额分组，该账号预计分到的新会话比例。'
    },
    schedulers: {
      sub2api: '原版',
      share: '份额'
    },
    schedulerHints: {
      sub2api: '按优先级和负载选号（Sub2API 原版）。',
      share: '按优先级分层；同一优先级内按负载因子比例分配新会话，会话尽量留在原账号以保住缓存。'
    },
    groups: {
      title: '分组',
      search: '搜索分组',
      empty: '没有可配置的分组。',
      noMatch: '没有匹配的分组。',
      loadFailed: '分组列表读取失败。',
      retry: '重试',
      selectHint: '在左侧选择一个分组。',
      active: '使用中',
      idle: '未启用',
      members: '{count} 个账号',
      cacheHit: '缓存命中 {value}',
      switchLoss: '换号损失 {value}',
      invalid: '设置无法读取',
      notEffective: '未生效'
    },
    mode: {
      title: '调度方式',
      sessionKeep: '会话保持',
      sessionKeepHint: '开启时同一会话尽量留在同一账号，缓存过期后再按比例重新分配；关闭只适合图片、向量等无状态分组。',
      switchToShare: '切换为份额调度',
      rollback: '回到原版',
      updated: '版本 {version} · {time} 更新',
      neverSaved: '尚未设置，当前为原版调度。',
      notEffective: '总开关关闭：本组暂按原版运行。',
      legacyShadow: '此分组原为“影子对比”，该模式已移除，现按原版运行。',
      invalid: '已存的设置无法读取（{reason}），本组按原版运行；重新选择调度方式即可覆盖。',
      conflict: '此分组的设置已被其他管理员修改。',
      reload: '重新载入',
      loadFailed: '分组设置读取失败。',
      unavailable: '份额调度存储暂不可用，请稍后重试。'
    },
    change: {
      titleShare: '切换为份额调度',
      titleSub2api: '回到原版调度',
      titleKeepOn: '开启会话保持',
      titleKeepOff: '关闭会话保持',
      shareBody: '确认后立即生效：新会话按下表比例分配；已有会话留在原账号，缓存过期后再按比例重新分配。',
      sub2apiBody: '确认后立即生效：本组回到原版调度，已有会话不迁移。',
      keepOnBody: '开启后同一会话尽量留在同一账号，缓存过期后再按比例重新分配。',
      keepOffBody: '关闭后每个请求都按比例重新选账号，提示缓存基本无法命中。只适合图片、向量等无状态分组。',
      enableGlobal: '总开关目前关闭，确认后会一并开启；其他调度方式为“份额”的分组也会随之生效。',
      previewing: '正在计算预计份额…',
      previewFailed: '无法计算预计份额：{error}',
      expected: '预计新会话份额',
      risks: '风险提示',
      noRisks: '没有风险提示。',
      columns: {
        account: '账号',
        priority: '优先级',
        loadFactor: '负载因子',
        share: '预计份额'
      },
      others: '其余 {count} 个账号预计份额为 0（备用或暂不可用）。',
      confirm: '确认',
      cancel: '取消',
      saving: '正在保存…',
      failed: '保存失败：{error}'
    },
    risks: {
      global_switch_off: '总开关关闭，本组暂按原版运行。',
      no_healthy_account: '没有可接新会话的可用账号，请求只能走末位兜底。',
      top_priority_unhealthy: '优先级 {priority} 没有可用账号，新会话会进入优先级 {value}。',
      single_account_dominant: '{name} 预计分到 {value} 的新会话，流量过于集中。',
      high_load_factor: '{name} 的负载因子为 {value}，可用时会拿到同一优先级的绝大多数新会话。',
      high_failure: '{name} 近 1 小时失败率 {value}（全部分组），建议调低负载因子或停用。',
      unknown: '{code}'
    },
    health: {
      title: '运行状况',
      window: '时间窗口',
      windows: {
        h1: '1 小时',
        h6: '6 小时',
        h24: '24 小时'
      },
      refresh: '刷新',
      levels: {
        ok: '运行正常',
        watch: '需要关注',
        rollback: '建议回到原版',
        insufficient: '数据不足'
      },
      insufficient: {
        requests: '请求太少（两个窗口都至少需要 {min} 次），暂不判断。',
        baseline: '还没有可对比的基线。',
        window: '切换后只有 {minutes} 分钟的数据（至少需要 {min} 分钟），暂不判断。'
      },
      truncated: '切换后的数据只有 {minutes} 分钟。',
      baselineUnavailable: {
        never_switched: '本组没有切换记录，无法对比。',
        out_of_retention: '切换前的缓存归因数据已超出保留期（约 8 天），“因换号损失的缓存”无法对比。'
      },
      sub2api: '原版运行中，下列为当前数值。切换到份额后，这里会与切换前同长度的窗口对比。',
      baseline: '对比切换前 {from} 至 {to}',
      metrics: {
        cacheHit: '缓存命中率',
        switchLoss: '因换号损失的缓存',
        ttft: '首字延迟',
        errors: '错误率'
      },
      parts: {
        p50: '一般',
        p95: '较慢'
      },
      referenceOnly: '受时段与模型构成影响，仅供参考；只提示关注，不据此建议回退。',
      vsBaseline: '基线 {value}',
      noBaseline: '无基线',
      calibrating: '判定阈值为初始值，待生产数据校准。',
      loadFailed: '运行数据读取失败。',
      hourRounded: '基线与超过 2 小时的窗口按整点小时汇总，与精确时间窗口最多相差 30 分钟。',
      countersError: '部分计数读取失败：{error}'
    },
    attention: {
      title: '需要处理',
      none: '暂无需要处理的事项。'
    },
    traffic: {
      title: '流量分配',
      hintShare: '数字越小的优先级越先使用；同一优先级内按负载因子比例分配新会话。',
      hintSub2api: '原版运行中：“预计份额”是切换到份额调度后的预计值。',
      priority: '优先级 {priority}',
      active: '接收新会话',
      standby: '备用：上一级不可用或满载时使用',
      columns: {
        account: '账号',
        status: '状态',
        loadFactor: '负载因子',
        expected: '预计新会话份额',
        actual: '近 1 小时实际',
        failure: '近 1 小时失败率'
      },
      failureHint: '该账号近 1 小时在全部分组的失败率。',
      ifHealthy: '恢复后 {value}',
      healthy: '可调度',
      recoverAt: '预计 {time} 恢复',
      empty: '该分组还没有账号。',
      manageAccounts: '前往账号管理'
    },
    loadFactor: {
      edit: '修改 {name} 的负载因子',
      fromConcurrency: '未设置，按并发 {value}',
      alsoAffects: '同时影响：{groups}',
      otherGroups: '+{count} 组',
      clearHint: '留空表示清除，改按并发',
      invalid: '负载因子需为 1 到 {max} 的整数，留空表示清除。',
      failed: '负载因子保存失败。',
      saved: '{name} 的负载因子已改为 {value}。',
      cleared: '{name} 的负载因子已清除，改按并发。'
    },
    details: {
      title: '详细报表',
      cacheClasses: '缓存命中与未命中（按输入 Token）',
      reasons: '选号原因',
      skipped: '账号被跳过的原因',
      shareOnly: '以下数据只在份额模式下产生。',
      waits: '满载等待 {count} 次，等到 {hit} 次，平均 {avg}。',
      attempts: '共尝试账号 {count} 次（{requests} 次选号）。',
      sampledNote: '缓存归因按每 4 个会话抽 1 个统计。',
      noData: '该时间窗口内暂无数据。'
    },
    explain: {
      title: '排查工具',
      hint: '用当前设置和账号状态模拟一次选号：不占用并发，也不写入会话绑定。',
      simulate: '模拟一个新会话',
      running: '计算中…',
      more: '更多输入（模型、会话哈希、请求样本）',
      model: '模型',
      sessionHash: '会话哈希（可选）',
      sessionHashHint: '会话绑定中保存的会话哈希；留空时按新会话处理。',
      estimatedTokens: '预计输入 Token（可选）',
      cacheOneHour: '请求声明 1 小时缓存',
      body: '请求样本（可选，JSON）',
      bodyHint: '粘贴请求体，用于估算输入 Token 并识别 1 小时缓存声明；填写后优先于上面的 Token 估算。',
      bodyInvalid: '请求样本不是合法 JSON。',
      run: '按以上输入模拟',
      failed: '模拟失败。',
      summary: {
        random: '这是一个随机的新会话：每次模拟的落点按负载因子比例随机，可能不同。',
        target: '会进入 {account}（优先级 {priority}）。',
        bound: '该会话当前绑定 {account}。',
        none: '没有可用账号，请求将无法放置。',
        fallback: '若它满载或不可用，依次尝试：{list}。'
      },
      details: '详细结果',
      result: {
        reason: '选号原因',
        keySource: '会话识别方式',
        session: '会话指纹',
        bound: '当前绑定',
        none: '无',
        idle: '空闲时长',
        cacheTTL: '缓存寿命',
        cache: '缓存状态',
        notCacheable: '不可缓存',
        warm: '仍在缓存期',
        cold: '已过期',
        waitBudget: '满载时最多等待',
        write: '会话绑定',
        spill: '借用其他账号时',
        target: '目标账号',
        ideal: '按比例应去的账号',
        entries: '候选明细'
      },
      entryColumns: {
        rank: '次序',
        account: '账号',
        priority: '优先级',
        weight: '负载因子',
        loadRate: '负载率',
        state: '资格'
      },
      saturated: '满载',
      eligible: '可用',
      excluded: '已排除',
      writes: {
        none: '不写入',
        refresh: '续期',
        if_absent: '仅在无绑定时新建',
        replace: '改绑'
      }
    },
    keySources: {
      key_continuation: '协议续接',
      key_session: '会话标识',
      key_anchor: '对话锚点',
      key_prefix: '提示前缀',
      key_none: '无会话'
    },
    reasons: {
      original: '原版调度',
      owner_continuation: '协议续接',
      affinity_hit: '会话保持命中',
      affinity_wait: '等待后命中',
      spill_rebind: '满载换号',
      soft_spill: '短时不可用，临时借用',
      new_session: '新会话',
      sessionless: '无会话',
      realign_cold: '缓存过期后按比例重新分配',
      return_cold: '缓存过期后回到高优先级',
      rebind_unavailable: '绑定账号不可用',
      tier_overflow_saturated: '本优先级满载，进入下一级',
      tier_overflow_unavailable: '本优先级不可用，进入下一级',
      failover_next: '故障换号',
      model_routing: '模型路由',
      model_routing_fallback: '模型路由回退',
      resil_failure_streak: '会话连续失败改绑',
      last_resort_soft: '末位兜底（软罚账号）',
      disabled: '已停用',
      inactive: '未激活',
      not_schedulable: '未开启调度',
      expired: '已过期',
      rate_limited: '限流中',
      overloaded: '过载冷却',
      temp_unschedulable: '临时不可调度',
      balance_exhausted: '上游余额不足',
      quota_exceeded: '额度用尽',
      model_unsupported: '不支持该模型',
      model_rate_limited: '模型限流',
      window_cost: '窗口费用上限',
      rpm_limit: 'RPM 上限',
      session_limit: '会话数上限',
      iq_degraded: '智力检测降级',
      quota_auto_pause: '额度自动暂停',
      shadow_parent_unhealthy: '影子父账号异常',
      grok_quota: 'Grok 额度',
      channel_restricted: '渠道限制',
      profit_gate: '利润门槛',
      privacy_required: '需要隐私模式',
      platform_mismatch: '平台不匹配',
      transport_mismatch: '传输方式不匹配',
      capability_mismatch: '能力不匹配',
      runtime_blocked: '运行时阻断',
      proxy_quarantined: '代理隔离',
      scheduling_threshold: '调度阈值',
      excluded_in_request: '本请求已失败',
      saturated: '满载',
      codex_pressure: 'Codex 限额压力',
      ineligible: '不可用'
    },
    cacheClasses: {
      cache_hit: '命中缓存',
      below_min_cacheable: '低于最小可缓存长度',
      miss_sessionless: '无会话',
      miss_new_session: '新会话',
      miss_ttl_expired: '缓存过期',
      miss_switch_policy: '按比例换号',
      miss_switch_saturated: '满载换号',
      miss_switch_unavailable: '不可用换号',
      miss_switch_failover: '故障换号',
      miss_switch_other: '其他换号',
      miss_prefix_drift: '前缀变化'
    },
    settings: {
      title: '份额调度',
      hint: '份额调度按账号优先级分层、按负载因子分配新会话。各分组的调度方式在“份额调度”页面设置。',
      openPage: '打开份额调度页'
    }
  }
}
