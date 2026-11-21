// Mobile list plan, item 19: no text under 12px. The two usage pages, what they show and what they open (the errors
// tables and their details, the balance history, the charts) are read at the source: no 10px or 11px size is left.
import { describe, expect, it } from 'vitest'

const sources = import.meta.glob(
  [
    '../*.vue',
    '../../../charts/*.vue',
    '../../../user/UserErrorRequestsTable.vue',
    '../../../user/UserErrorDetailModal.vue',
    '../../user/UserBalanceHistoryModal.vue',
    '../../../common/IpGeo*.vue',
    '../../../../views/admin/UsageView.vue',
    '../../../../views/user/UsageView.vue',
    '../../../../views/admin/ops/components/OpsErrorLogTable.vue',
    '../../../../views/admin/ops/components/OpsErrorDetailModal.vue',
  ],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>

describe('usage pages: no text under 12px', () => {
  it('reads every file it should', () => {
    const files = Object.keys(sources)
    for (const name of ['UsageTable.vue', 'TokenUsageTrend.vue', 'OpsErrorLogTable.vue', 'OpsErrorDetailModal.vue', 'UserBalanceHistoryModal.vue', 'UserErrorRequestsTable.vue']) {
      expect(files.some((file) => file.endsWith(`/${name}`))).toBe(true)
    }
  })

  // The usage table's model, token, cost and latency cells are back to their original desktop look (owner, 2026-11-21):
  // their 10px badges, the 11px upstream-answer line and the 11px account cost "A $…" are the one exception.
  const checked = (name: string, source: string) =>
    name.endsWith('/UsageTable.vue')
      ? source
        .replace(/<template #cell-model[\s\S]*?<template #cell-reasoning_effort/, '')
        .replace(/<template #cell-tokens[\s\S]*?<template #cell-created_at/, '')
      : source

  it.each(Object.entries(sources))('%s', (name, source) => {
    expect(checked(name, source)).not.toMatch(/text-\[1[01]px\]|text-2xs|font-size:\s*1[01]px|\bsize:\s*1[01]\b/)
  })
})
