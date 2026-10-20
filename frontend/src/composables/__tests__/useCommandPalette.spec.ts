import { describe, expect, it } from 'vitest'
import { flattenNav, searchEntries } from '../useCommandPalette'

describe('useCommandPalette helpers', () => {
  const nav = [
    { path: '/admin/dashboard', label: '仪表盘' },
    {
      path: '/admin/channels',
      label: '渠道管理',
      expandOnly: true,
      children: [
        { path: '/admin/channels/pricing', label: '渠道定价' },
        { path: '/admin/channels/monitor', label: '渠道监控' }
      ]
    },
    { path: '/admin/accounts', label: '账号管理' },
    { path: '/admin/accounts', label: '账号管理' }
  ]

  it('flattens groups, skips expand-only parents and de-duplicates paths', () => {
    expect(flattenNav(nav)).toEqual([
      { path: '/admin/dashboard', label: '仪表盘', section: undefined },
      { path: '/admin/channels/pricing', label: '渠道定价', section: '渠道管理' },
      { path: '/admin/channels/monitor', label: '渠道监控', section: '渠道管理' },
      { path: '/admin/accounts', label: '账号管理', section: undefined }
    ])
  })

  it('ranks prefix matches first and matches by section or path', () => {
    const entries = flattenNav(nav)
    expect(searchEntries(entries, '').map((e) => e.path)).toHaveLength(4)
    expect(searchEntries(entries, '账号')[0].path).toBe('/admin/accounts')
    expect(searchEntries(entries, '渠道').map((e) => e.path)).toEqual([
      '/admin/channels/pricing',
      '/admin/channels/monitor'
    ])
    expect(searchEntries(entries, 'pricing').map((e) => e.path)).toEqual(['/admin/channels/pricing'])
    expect(searchEntries(entries, '不存在')).toEqual([])
  })
})
