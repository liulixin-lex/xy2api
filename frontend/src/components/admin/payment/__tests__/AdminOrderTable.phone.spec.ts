import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AdminOrderTable from '../AdminOrderTable.vue'

// Package D review: on a phone the actions (up to four buttons with text) do not fit the 44px frozen column, so they
// scroll with the row, as on OrderTable; on touch each is 40px to hit.

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const original = window.matchMedia
const width = (phone: boolean) => {
  window.matchMedia = ((query: string) => ({
    matches: query === '(min-width: 768px)' ? !phone : true,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {}
  })) as unknown as typeof window.matchMedia
}
afterEach(() => { window.matchMedia = original })

const mountAt = (phone: boolean) => {
  width(phone)
  return mount(AdminOrderTable, {
    props: { orders: [], loading: false, page: 1, pageSize: 20, total: 0 },
    global: { stubs: { DataTable: { name: 'DataTable', props: ['stickyActionsColumn'], template: '<div />' }, Pagination: true, Select: true, Icon: true } }
  })
}

describe('AdminOrderTable on a phone', () => {
  it('lets the actions scroll with the row below 768px, frozen from 768px', () => {
    expect(mountAt(true).findComponent({ name: 'DataTable' }).props('stickyActionsColumn')).toBe(false)
    expect(mountAt(false).findComponent({ name: 'DataTable' }).props('stickyActionsColumn')).toBe(true)
  })

  it('gives each action 40px on touch', () => {
    const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../AdminOrderTable.vue'), 'utf8')
    expect(source).toContain('<div class="order-ops flex items-center gap-2">')
    expect(source).toMatch(/@media \(pointer: coarse\) \{\s*\.order-ops > button \{\s*min-width: 40px;\s*min-height: 40px;/)
  })
})
