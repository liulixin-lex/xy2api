/**
 * 分组质量门槛管理接口：策略读写与运行状态。
 * 读接口跟随账号管理权限，策略写入跟随分组管理权限。
 */

import { apiClient } from '../client'
import type {
  GroupQualityGatePolicies,
  GroupQualityGateStatus,
  UpdateGroupQualityGatePolicyRequest,
  UpdateGroupQualityGatePolicyResponse
} from '@/types/groupQualityGate'

export async function getPolicies(): Promise<GroupQualityGatePolicies> {
  const { data } = await apiClient.get<GroupQualityGatePolicies>('/admin/group-quality-gate/policies')
  return data
}

/** 版本不一致返回 409，校验失败返回 400。 */
export async function updatePolicy(
  groupId: number,
  request: UpdateGroupQualityGatePolicyRequest
): Promise<UpdateGroupQualityGatePolicyResponse> {
  const { data } = await apiClient.put<UpdateGroupQualityGatePolicyResponse>(
    `/admin/group-quality-gate/policies/${groupId}`,
    request
  )
  return data
}

/** 不传 groupId 时返回全部分组。 */
export async function getStatus(groupId?: number): Promise<GroupQualityGateStatus> {
  const { data } = await apiClient.get<GroupQualityGateStatus>('/admin/group-quality-gate/status', {
    params: groupId ? { group_id: groupId } : undefined
  })
  return data
}

export const groupQualityGateAPI = {
  getPolicies,
  updatePolicy,
  getStatus
}

export default groupQualityGateAPI
