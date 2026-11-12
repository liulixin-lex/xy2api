/** GET /admin/groups/:id/profit-control-preview?margin= */
export interface GroupProfitControlPreview {
  members: number
  schedulable: number
  passing: number
  /** 按当前时刻换算的上游倍率上限，只显示、不存储 */
  ceiling: number
}
