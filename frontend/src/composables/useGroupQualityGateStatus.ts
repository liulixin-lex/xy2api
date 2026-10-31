import { ref, shallowRef } from 'vue'
import { adminAPI } from '@/api/admin'
import type { GroupQualityGatePolicies, GroupQualityGateStatus } from '@/types/groupQualityGate'

/** 评估器每分钟算一轮；列表按需拉取，两次之间至少隔 60 秒。 */
export const GROUP_QUALITY_STATUS_MIN_INTERVAL_MS = 60_000

/**
 * 分组质量门槛的运行状态。账号列表还需要策略里的上限做悬停说明，传 withPolicies。
 * 失败时保留上一轮数据，只置 failed。
 */
export function useGroupQualityGateStatus(options: { withPolicies?: boolean } = {}) {
  const status = shallowRef<GroupQualityGateStatus | null>(null)
  const policies = shallowRef<GroupQualityGatePolicies | null>(null)
  const failed = ref(false)
  let startedAt = 0
  let inflight: Promise<void> | null = null

  /** force 只给刚保存过门槛配置的场景用，跳过 60 秒间隔。 */
  function refresh(force = false): Promise<void> {
    if (inflight) return inflight
    if (!force && startedAt && Date.now() - startedAt < GROUP_QUALITY_STATUS_MIN_INTERVAL_MS) return Promise.resolve()
    startedAt = Date.now()
    inflight = (async () => {
      try {
        const [nextStatus, nextPolicies] = await Promise.all([
          adminAPI.groupQualityGate.getStatus(),
          options.withPolicies ? adminAPI.groupQualityGate.getPolicies() : Promise.resolve(null)
        ])
        status.value = nextStatus
        policies.value = nextPolicies
        failed.value = false
      } catch (error) {
        failed.value = true
        console.error('Failed to load group quality gate status:', error)
      } finally {
        inflight = null
      }
    })()
    return inflight
  }

  return { status, policies, failed, refresh }
}
