export default {
  resilience: {
    settings: {
      title: '网关韧性',
      hint: '控制上游失败分类、冷却与余额恢复、会话改绑、请求预算、首包前保活、断开计费与无号兜底。关闭总开关即恢复原有行为。',
      openPanel: '查看运维面板',
      enabled: '总开关',
      enabledHint: '关闭后所有韧性能力停用，网关按原有逻辑运行。',
      sections: {
        session: '会话失败改绑',
        sessionHint: '同一会话在同一账号上连续失败 2 次后改绑，并在 10 分钟内避开该账号。',
        lastResort: '末位兜底',
        lastResortHint: '没有健康账号时，在冷却期最短的软罚账号上继续服务（单账号分组不再整组不可用）。',
        balance: '余额可恢复',
        balanceHint: '上游余额不足时按 1/2/5/10/15 分钟复检，充值后自动恢复。',
        budget: '请求预算',
        budgetHint: '每个请求最多尝试的次数与首输出前的总时长；同号重试仅用于容量降载。',
        keepalive: '首包前保活',
        keepaliveHint: '经 CDN 的流式请求在首个输出前定时发送心跳，避免被 CDN 空闲超时切断。',
        stall: '停滞检测',
        stallHint: '已输出后长时间无数据即判定断流并结束请求。',
        disconnect: '断开计费',
        disconnectHint: '客户端在任何输出前断开时立即取消上游且不计费；部分输出后最多再读取一段时间并按已得用量计费。',
        errors: '错误语义与防风暴',
        errorsHint: '无号与限流应答带 Retry-After，短时负缓存，同一 Key 的重复 503 快速返回。',
        fallbackGroup: '无号兜底分组',
        fallbackGroupHint: '分组无可用账号时由其配置的兜底分组承接，主分组稳定后切回。',
        cacheAware: '缓存感知预留',
        cacheAwareHint: '在途预留按会话最近一次的缓存读比例估算输入费用。',
        outputP99: '输出 P99 上限',
        outputP99Hint: '在途预留的输出 Token 取模型近 7 天 P99（下限 1024）。'
      },
      fields: {
        maxAttempts: '最多尝试次数',
        preOutputBudget: '首输出前总时长（秒）',
        keepaliveDelay: '首次心跳延迟（秒）',
        keepaliveInterval: '心跳间隔（秒）',
        edges: '视为 CDN 的边缘标记',
        edgesHint: '与反向代理注入的 X-XY-Edge 值一致，多个用逗号分隔。',
        textStall: '文本停滞阈值（秒）',
        partialDrain: '部分输出后最长读取（秒）',
        abuseThreshold: '未送达滥用阈值（次/小时/Key）',
        negativeCache: '无号负缓存（秒）',
        returnStable: '主分组稳定多久后切回（秒）'
      },
      advanced: '高级（完整 JSON）',
      advancedHint: '包含全部配置项；保存时由服务端校验，未通过不会生效。',
      applyAdvanced: '应用 JSON 到表单',
      invalidJson: 'JSON 格式错误',
      resetDefaults: '恢复默认值',
      save: '保存',
      saved: '已保存',
      saveFailed: '保存失败',
      loadFailed: '无法读取网关韧性设置。',
      reload: '重新加载',
      invalidNumber: '{field} 需为 {min}–{max} 之间的整数'
    },
    panel: {
      title: '网关韧性',
      hint: '统计来自当前实例的内存计数（最近 1 小时），多实例部署时为单实例口径。',
      window: '时间窗口',
      refresh: '刷新',
      loadFailed: '无法读取韧性统计。',
      empty: '窗口内没有韧性事件。',
      metric: '指标',
      group: '分组',
      account: '账号',
      label: '细分',
      count: '次数',
      balanceStates: '账号余额状态',
      metrics: {
        keepalive_committed: '首包前心跳提交',
        first_output_timeout: '首输出超时',
        budget_exhausted: '预算耗尽',
        last_resort: '末位兜底',
        fallback_group: '兜底分组承接',
        fallback_return: '切回主分组',
        session_rebind: '会话改绑',
        undelivered: '未送达',
        cdn_cut: '推断的 CDN 切断',
        fast_fail: '快速失败',
        turn_cap: '失败回合封顶',
        abuse_guard: '滥用保护计费',
        stall: '停滞断流'
      },
      states: {
        OK: '正常',
        SUSPECT: '疑似不足',
        EXHAUSTED: '余额不足',
        RECOVERING: '恢复中'
      }
    },
    undelivered: {
      title: '未送达请求',
      hint: '客户端在任何输出前断开的请求：上游已取消，不计费（超过滥用阈值的除外）。',
      empty: '暂无未送达请求。',
      loadFailed: '无法读取未送达请求。',
      time: '时间',
      stage: '断开阶段',
      duration: '耗时',
      user: '用户',
      apiKey: 'Key',
      model: '模型',
      endpoint: '入口',
      cost: '上游成本估算',
      billed: '已计费',
      notBilled: '未计费',
      stages: {
        select: '选号或等待槽位',
        header_wait: '等待上游响应头',
        pre_output: '首输出前',
        streaming: '输出中'
      }
    },
    account: {
      balance: '上游余额',
      nextCheck: '下次复检 {time}',
      recheck: '已充值',
      recheckHint: '立即复检该账号的上游余额',
      rechecking: '复检中…',
      recheckDone: '复检完成：{state}',
      recheckFailed: '复检失败'
    },
    usage: {
      fallback: '兜底：{origin} → {group}',
      fallbackTitle: '原分组无可用账号，由兜底分组承接并按其倍率计费'
    }
  }
}
