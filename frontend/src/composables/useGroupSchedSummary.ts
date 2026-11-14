import { ref, shallowRef } from 'vue'
import schedQualityAPI from '@/api/admin/schedQuality'
import type { SchedGroupSummary } from '@/types/schedQuality'

/** Scheduling summary of the OpenAI groups on the group list (FINAL §7): one request per list refresh; a late answer
 * for an older refresh is dropped. */
export function useGroupSchedSummary() {
  const byGroup = shallowRef(new Map<number, SchedGroupSummary>())
  const loading = ref(false)
  let seq = 0

  async function refresh(groupIds: number[]) {
    const current = ++seq
    if (!groupIds.length) {
      byGroup.value = new Map()
      loading.value = false
      return
    }
    loading.value = true
    try {
      const { groups } = await schedQualityAPI.groupsSummary(groupIds)
      if (current === seq) byGroup.value = new Map(groups.map((group) => [group.group_id, group]))
    } catch (error) {
      if (current === seq) console.error('Failed to load group scheduling summary:', error)
    } finally {
      if (current === seq) loading.value = false
    }
  }

  return { byGroup, loading, refresh }
}
