export default {
  groupQualityGate: {
    title: '质量门槛',
    hint: '超线账号暂时移出本组调度，到期后观察 30 分钟；快档线用于低倍率优先排序和门槛组慢号迁出。',
    enable: '启用质量门槛',
    disabledHint: '关闭门槛后，本组选号不受质量门槛影响。',
    loading: '正在读取门槛配置…',
    needsAccountView: '需要账号查看权限才能配置质量门槛',
    loadFailed: '读取门槛配置失败：{message}',
    retry: '重试',
    maxErrorRate: '错误率熔断线（%）',
    maxErrorRateHint: '默认 10%',
    maxWarmTtft: '续轮可见首字熔断线（秒）',
    maxWarmTtftHint: '默认 20 秒',
    fastWarmTtft: '快档线（秒）',
    fastWarmTtftHint: '默认 10 秒；实际不高于组内最快 + 4 秒',
    pinned: '固定账号',
    pinnedHint: '熔断时仍参与本组调度',
    pinnedCount: '已选 {count}',
    pinnedSearch: '搜索成员名称或 #ID',
    pinnedNoMatch: '没有匹配的成员',
    membersLoading: '正在读取本组成员…',
    membersEmpty: '本组还没有 OpenAI 账号，加入成员后才能固定',
    membersFailed: '读取本组成员失败：{message}',
    errorRateRange: '错误率熔断线须大于 0 且不超过 100%',
    maxTtftRange: '熔断线须大于 1 秒且不超过 600 秒',
    fastTtftMin: '快档线不能小于 1 秒',
    fastTtftBelowMax: '快档线须小于续轮可见首字熔断线',
    fixErrors: '质量门槛的设置有误，请先修正',
    groupSavedGateConflict: '分组已保存，门槛未保存：门槛配置已被其他人修改，已刷新版本，请确认后再次保存',
    groupSavedGateFailed: '分组已保存，门槛未保存：{message}',
    column: '门槛',
    summaryTitle: '观察中 {trial} 个',
    pinnedMark: '固定',
    state: {
      eligible: '合格',
      ejected: '熔断',
      ejectedUntil: '熔断至 {time}',
      trial: '观察中',
      trialUntil: '观察至 {time}',
      guardHeld: '保底保留',
      pending: '等待评估',
      failed: '读取失败'
    },
    reason: {
      error_rate: '错误率超线',
      warm_ttft: '续轮可见首字超线'
    },
    tooltip: {
      title: '{group} · {state}',
      strikes: '（第 {count} 次）',
      pinned: '固定账号，熔断时仍调度',
      guardHeld: '为保住最少可用账号仍调度',
      metric: '指标',
      measured: '实测',
      limit: '上限',
      ejectionWindow: '熔断时窗口',
      errorRate: '错误率',
      warmTtft: '可见首字 p90',
      tierFast: '快档',
      tierOther: '其他档',
      fastLine: '{tier}，实时快档线 {line}',
      fastLineNoBest: '{tier}，实时快档线 {line}（组内暂无足够样本）',
      noData: '无数据',
      notEnoughSamples: '样本不足',
      belowThreshold: '未达判定条件',
      headroom: '余量 {value}',
      stale: '评估超过 5 分钟未更新，沿用上一轮',
      pending: '每分钟评估一次',
      failed: '刷新列表后重试'
    },
    indicator: {
      attention: '质量门槛：{count} 个分组异常',
      ok: '质量门槛：无异常'
    },
    panel: {
      eligible: '合格 {count}',
      window: '账号整体'
    },
    metrics: {
      errorRate: '错 {value}',
      errorRateTitle: '错误率（近 15 分钟）：{calls} 次调用，软失败 {failures} 次',
      warmTtft: '可见首字 {value}',
      warmTtftTitle: '续轮用户可见首字 p90（近 30 分钟）：流式样本 {count} 个',
      zeroCache: '零缓存 {value}',
      zeroCacheTitle: '续轮零缓存率（近 2 小时，提示 ≥ 2048 token）：样本 {count} 个；超过 30% 不算快档'
    }
  }
}
