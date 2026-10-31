import { computed, ref, shallowRef } from 'vue'
import { adminAPI } from '@/api/admin'
import type { GroupQualityGatePolicies, GroupQualityGatePolicy, GroupQualityGateStatus } from '@/types/groupQualityGate'
import type { GroupQualitySources } from '@/components/admin/account/groupQualityPresentation'

/** 评估器每分钟算一轮；列表按需拉取，两次之间至少隔 60 秒。 */
export const GROUP_QUALITY_STATUS_MIN_INTERVAL_MS = 60_000

/**
 * 分组质量门槛的策略与运行状态。门槛开没开以策略为准，状态只给出评估结果；
 * 两者分别记录读取失败，界面据此区分“关闭 / 等待评估 / 读取失败”。
 */
export function useGroupQualityGateStatus() {
  const status = shallowRef<GroupQualityGateStatus | null>(null)
  const policies = shallowRef<GroupQualityGatePolicies | null>(null)
  const statusFailed = ref(false)
  const policiesFailed = ref(false)
  let startedAt = 0
  let inflight: Promise<void> | null = null

  function refresh(): Promise<void> {
    if (inflight) return inflight
    if (startedAt && Date.now() - startedAt < GROUP_QUALITY_STATUS_MIN_INTERVAL_MS) return Promise.resolve()
    startedAt = Date.now()
    inflight = (async () => {
      const [nextStatus, nextPolicies] = await Promise.allSettled([
        adminAPI.groupQualityGate.getStatus(),
        adminAPI.groupQualityGate.getPolicies()
      ])
      statusFailed.value = nextStatus.status === 'rejected'
      if (nextStatus.status === 'fulfilled') status.value = nextStatus.value
      else console.error('Failed to load group quality gate status:', nextStatus.reason)
      policiesFailed.value = nextPolicies.status === 'rejected'
      if (nextPolicies.status === 'fulfilled') policies.value = nextPolicies.value
      else console.error('Failed to load group quality gate policies:', nextPolicies.reason)
      inflight = null
    })()
    return inflight
  }

  /** 门槛保存成功后用返回的策略更新本地状态；评估结果等下一次按间隔刷新。 */
  function applyPolicy(groupId: number, version: number, policy: GroupQualityGatePolicy) {
    if (!policies.value) return
    policies.value = { version, groups: { ...policies.value.groups, [String(groupId)]: policy } }
  }

  const sources = computed<GroupQualitySources>(() => ({
    policies: policies.value,
    policiesFailed: policiesFailed.value,
    status: status.value,
    statusFailed: statusFailed.value
  }))

  return { sources, refresh, applyPolicy }
}
