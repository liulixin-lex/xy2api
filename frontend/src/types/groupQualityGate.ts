// 分组质量门槛与利润门预览的接口类型。字段名与 group-quality-gate-v3-20261104 的接口契约（design §5.2、§5.3）一致，不得改名。

/** 每组 v3 门槛设置。界面里错误与缓存按百分比输入，接口里是 0–1 小数。 */
export interface GroupQualityGateV3Policy {
  enabled: boolean
  /** 首字上限 X：本组 P90 首字不超过此值，5–60 的整数（秒） */
  max_ttft_p90_seconds: number
  /** 错误上限 Y：0.05–0.50 */
  max_error_rate: number
  /** 缓存下限 Z：0.50–0.70 */
  min_cache_keep_rate: number
  /** 严格模式：未知的号一律放 1 档 */
  strict: boolean
}

/**
 * 每组门槛策略。v2 字段只为回滚保留：界面不展示、不编辑，保存时原样回传，服务端也只取 v3。
 * GET 总会填好生效的 v3（没有 v3 子对象时启用状态继承 v2，其余取缺省值）。
 */
export interface GroupQualityGatePolicy {
  enabled?: boolean
  max_error_rate?: number
  max_warm_ttft_p90_ms?: number
  fast_warm_ttft_p90_ms?: number
  pinned_account_ids?: number[]
  v3: GroupQualityGateV3Policy
}

/** GET /admin/group-quality-gate/policies；分组没有条目时视为未启用。 */
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

/** 账号在本组的状态：达标、未知（0 档但有维无结论）、待复测（1 档）、不达标（2 档）、固定。 */
export type GroupQualityGateAccountState = 'passing' | 'unknown' | 'retest' | 'failing' | 'pinned'
export type GroupQualityGateTier = 0 | 1 | 2
/** 维度状态：达标、不达标、无结论。 */
export type GroupQualityDimensionState = 'pass' | 'fail' | 'none'

interface GroupQualityDimensionBase {
  state: GroupQualityDimensionState
  samples: number
  /** UTC RFC3339：最后一次不达标 + 2 小时；坏记忆已清除时为 null */
  bad_until: string | null
}

export interface GroupQualityTtftDimension extends GroupQualityDimensionBase {
  /** 预计 P90（毫秒）= GM30 × 本组 e_G；没有统计时为 null */
  value: number | null
  /** 速度系数 GM30 */
  factor: number | null
  sessions: number
}

export interface GroupQualityRateDimension extends GroupQualityDimensionBase {
  /** 错误维为失败率，缓存维为保持率点估计；没有统计时为 null */
  value: number | null
  wilson_low: number | null
  wilson_high: number | null
}

export interface GroupQualityErrorDimension extends GroupQualityRateDimension {
  failures: number
}

export interface GroupQualityLatencyHold {
  /** 判慢让位的最晚到期时刻；到期只解除后排，不代表已恢复 */
  until: string
  models: string[]
}

export interface GroupQualityGateAccountStatus {
  account_id: number
  status: GroupQualityGateAccountState
  tier: GroupQualityGateTier
  pinned: boolean
  latency_hold: GroupQualityLatencyHold | null
  ttft: GroupQualityTtftDimension
  error: GroupQualityErrorDimension
  cache: GroupQualityRateDimension
}

export interface GroupQualityGateSummary {
  passing: number
  unknown: number
  retest: number
  failing: number
  pinned: number
}

export interface GroupQualityGateGroupStatus {
  group_id: number
  enabled: boolean
  policy: GroupQualityGateV3Policy
  /** 本组构成 P90 e_G（毫秒）；不足 50 条时为 null，首字维显示“本组样本不足” */
  expected_p90_ms: number | null
  expected_samples: number
  summary: GroupQualityGateSummary
  accounts: GroupQualityGateAccountStatus[]
}

/** GET /admin/group-quality-gate/status */
export interface GroupQualityGateStatus {
  generated_at: string
  stale: boolean
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
