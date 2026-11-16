/** GET /admin/groups/:id/profit-control-preview?margin=&floor= */
export interface GroupProfitControlPreview {
  members: number
  schedulable: number
  passing: number
  /** 按当前时刻换算的上游倍率上限，只显示、不存储 */
  ceiling: number
  /** 本次试算用的最低上游倍率；0 表示不设 */
  floor: number
  /** 可调度成员中上游倍率低于下限、被排除的号数（固定号不算） */
  below_floor: number
  /** 本组的固定号数 */
  pinned: number
  /** 下限高于上限：倍率区间为空，本组没有号可用 */
  range_empty: boolean
  /** 上游倍率高于上限的固定号（固定号不受区间限制，会亏损） */
  pinned_loss: GroupProfitPinnedLoss[]
}

export interface GroupProfitPinnedLoss {
  account_id: number
  name: string
  rate: number
}
