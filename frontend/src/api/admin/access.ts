/**
 * Admin access API: the caller's own admin role, permissions and the
 * settings an operator needs to render the admin console.
 */
import { apiClient } from '../client'
import type { MeAccess } from '@/types/adminAccess'

export async function getMeAccess(): Promise<MeAccess> {
  const { data } = await apiClient.get<MeAccess>('/admin/me/access')
  return data
}

export const accessAPI = {
  getMeAccess
}

export default accessAPI
