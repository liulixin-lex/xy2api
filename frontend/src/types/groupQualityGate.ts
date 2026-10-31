// 分组质量门槛与利润门预览的接口类型。字段名与 group-quality-gate-20261031 的接口契约一致，不得改名。

/** 每组门槛策略；分组没有条目时视为 enabled=false。 */
export interface GroupQualityGatePolicy {
  enabled: boolean
  /** 错误率熔断线，取值 (0, 1] */
  max_error_rate: number
  /** 续轮首字 p90 熔断线（毫秒），≤ 600000 */
  max_warm_ttft_p90_ms: number
  /** 快档线（毫秒），只用于排序，≥ 1000 且小于熔断线 */
  fast_warm_ttft_p90_ms: number
  /** 固定账号，必须是本组成员 */
  pinned_account_ids: number[]
}

/** GET /admin/group-quality-gate/policies */
export interface GroupQualityGatePolicies {
  version: number
  groups: Record<string, GroupQualityGatePolicy>
}

/** PUT /admin/group-quality-gate/policies/:group_id 的请求体；版本不一致返回 409。 */
export interface UpdateGroupQualityGatePolicyRequest {
  expected_version: number
  policy: GroupQualityGatePolicy
}

export interface UpdateGroupQualityGatePolicyResponse {
  version: number
  policy: GroupQualityGatePolicy
}

export type GroupQualityGateState = 'eligible' | 'ejected' | 'trial'
export type GroupQualityGateReason = 'error_rate' | 'warm_ttft'

/** 按账号算一份、所有分组共用的完整窗口指标，供账号行和熔断中提示显示。 */
export interface GroupQualityAccountMetrics {
  calls: number
  soft_failures: number
  error_rate: number
  warm_samples: number
  warm_ttft_p90_ms: number
  zero_cache_samples: number
  warm_zero_cache_rate: number
}

/**
 * 本组判定实际使用的有效数据（有效起点之后）。计数始终是实际值；error_rate 为 null 表示未达判定条件
 * （调用 < 20 或软失败 < 5），warm_ttft_p90_ms 为 null 表示续轮样本 < 10。
 */
export interface GroupQualityMemberMetrics {
  calls: number
  soft_failures: number
  error_rate: number | null
  warm_samples: number
  warm_ttft_p90_ms: number | null
}

export interface GroupQualityGateAccountStatus {
  account_id: number
  state: GroupQualityGateState
  /** 熔断到期时刻（UTC RFC3339），只在 ejected 时有值 */
  until?: string | null
  reasons: GroupQualityGateReason[]
  strikes: number
  usable: boolean
  passing: boolean
  guard_held: boolean
  pinned: boolean
  /** 选号实际是否否决；固定与保底保留为 false */
  vetoed: boolean
  tier: number
  /** 本组判定实际使用的指标；合格/试用提示使用它，熔断中提示改看完整窗口 */
  metrics: GroupQualityMemberMetrics
  /** 按成员 metrics 里非 null 指标中最紧一项的 (1 − 实测 ÷ 上限)；全为 null 时为 null */
  headroom: number | null
}

export interface GroupQualityGateGroupStatus {
  group_id: number
  enabled: boolean
  fast_warm_ttft_p90_ms: number
  /** eligible：未否决的成员数；ejected：否决数；trial：试用数 */
  summary: { eligible: number; ejected: number; trial: number }
  accounts: GroupQualityGateAccountStatus[]
}

/** GET /admin/group-quality-gate/status */
export interface GroupQualityGateStatus {
  generated_at: string
  stale: boolean
  account_metrics: Record<string, GroupQualityAccountMetrics>
  groups: GroupQualityGateGroupStatus[]
}

/** GET /admin/groups/:id/profit-control-preview?margin= */
export interface GroupProfitControlPreview {
  members: number
  schedulable: number
  passing: number
  /** 按当前时刻换算的上游倍率上限，只显示、不存储 */
  ceiling: number
}
