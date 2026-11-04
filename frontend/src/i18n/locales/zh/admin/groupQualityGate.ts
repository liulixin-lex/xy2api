export default {
  groupQualityGate: {
    title: '质量门槛',
    hint: '按本组标准把成员分为三档：不达标的号排到最后；绑定号不达标或判慢时，会话迁往本组留有余量的合格号。不剔除、不暂停，连续两次回到恢复线后回到前排。只用本进程启动后的数据，重启后约 30 分钟内多数号为“未知”（严格模式下为“待复测”）。',
    enable: '启用质量门槛',
    disabledHint: '关闭门槛后，本组不分档，判慢的号排到候选末尾。',
    loading: '正在读取门槛配置…',
    needsAccountView: '需要账号查看权限才能配置质量门槛',
    loadFailed: '读取门槛配置失败：{message}',
    retry: '重试',
    maxTtft: '首字上限（秒）',
    maxTtftHint: '本组 P90 首字不超过此值；5–60 的整数，默认 15',
    maxErrorRate: '错误上限（%）',
    maxErrorRateHint: '失败率超过此值且失败 ≥5 次算不达标；5–50%，默认 10%',
    minCacheKeep: '缓存下限（%）',
    minCacheKeepHint: '缓存保持率明显低于此值算不达标；50–70%，默认 50%',
    strict: '严格模式',
    strictHint: '有维度无结论的号一律放在“待复测”档；会话只迁往三维都已测到、且都在恢复线以内的号。',
    maxTtftRange: '首字上限须为 5 到 60 的整数',
    maxErrorRateRange: '错误上限须在 5% 到 50% 之间',
    minCacheKeepRange: '缓存下限须在 50% 到 70% 之间',
    fixErrors: '质量门槛的设置有误，请先修正',
    groupSavedGateConflict: '分组已保存，门槛未保存：门槛配置已被其他人修改，已刷新版本，请确认后再次保存',
    groupSavedGateFailed: '分组已保存，门槛未保存：{message}',
    column: '门槛',
    summaryTitle: '{counts}；{composition}',
    groupSamplesShort: '本组样本不足',
    unknownBadgeTitle: '{group} · 未知：暂无结论（样本不足或处于中间带），暂按 0 档参与排序',
    pinnedMark: '固定',
    state: {
      passing: '达标',
      unknown: '未知',
      retest: '待复测',
      failing: '不达标',
      pinned: '固定',
      pending: '等待评估',
      failed: '读取失败'
    },
    dimension: {
      pass: '达标',
      fail: '不达标',
      none: '无结论'
    },
    dimensionName: {
      ttft: '首字',
      error: '错误',
      cache: '缓存'
    },
    tooltip: {
      title: '{group} · {state}',
      strict: '严格',
      pinned: '固定账号：本组质量档固定为 0 档，下表为实际状态；判慢与急性保护照常生效',
      dimension: '维度',
      stateColumn: '状态',
      measured: '实测',
      samples: '样本',
      limitValue: '标准 {limit}',
      ttft: '首字 P90',
      errorRate: '错误率',
      cacheKeep: '缓存保持',
      noData: '—',
      groupSamplesShort: '本组样本不足',
      groupSamplesInsufficient: '首字：本组样本不足（{count}/50 条）',
      composition: '本组构成 P90 {value}（{count} 条）',
      factor: '系数 {value}',
      interval: '区间 {range}',
      ttftSamples: '{samples} · {sessions} 会话',
      badMemory: '坏记忆：{items}',
      badMemoryItem: '{dimension}至 {time}',
      separator: '、',
      stale: '评估数据陈旧或读取失败，本轮未评估，沿用上一轮状态',
      pending: '每分钟评估一次',
      failed: '刷新列表后重试'
    },
    latencyHold: {
      text: '判慢让位：{models}，后排至 {time}',
      note: '到期只解除后排，不改变门槛状态'
    },
    indicator: {
      attention: '质量门槛：{count} 个分组需关注',
      hold: '质量门槛：判慢让位中',
      ok: '质量门槛：无需关注'
    },
    alert: {
      ttft: '首字 {value}',
      error: '错 {value}',
      cache: '缓存 {value}',
      latencyHold: '判慢至 {time}',
      title: '{dimension}在 {count} 个门槛分组不达标'
    }
  }
}
