/** Clears what the gateway learned about an account (0.4.4 W6-lite). */

import { apiClient } from '../client'

export type { AccountCapabilityLearned, AccountWithCapability } from '@/utils/accountCapability'

export async function clearCapabilityLearning(id: number): Promise<void> {
  await apiClient.delete(`/admin/accounts/${id}/capability-learning`)
}
