import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OperatorPermissionsTable from '../OperatorPermissionsTable.vue'
import zhOperatorAccess from '@/i18n/locales/zh/admin/operatorAccess'
import enOperatorAccess from '@/i18n/locales/en/admin/operatorAccess'
import { ADMIN_FLAG_IDS, ADMIN_MODULE_IDS, type AdminPermissionsWire } from '@/types/adminAccess'

const i18nState = vi.hoisted(() => ({ messages: {} as Record<string, unknown> }))

// The test build of vue-i18n has no message compiler: look the real locale
// strings up by key instead.
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => {
      const value = key
        .split('.')
        .reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], i18nState.messages)
      return typeof value === 'string' ? value : key
    }
  })
}))

function mountTable(initial: AdminPermissionsWire, locale: 'zh' | 'en' = 'zh') {
  i18nState.messages = { admin: locale === 'zh' ? zhOperatorAccess : enOperatorAccess }
  const emitted: AdminPermissionsWire[] = []
  const wrapper = mount(OperatorPermissionsTable, {
    props: {
      modelValue: initial,
      'onUpdate:modelValue': (value: AdminPermissionsWire) => {
        emitted.push(value)
        void wrapper.setProps({ modelValue: value })
      }
    }
  })
  return { wrapper, emitted }
}

function checkbox(wrapper: ReturnType<typeof mountTable>['wrapper'], testId: string) {
  return wrapper.get(`[data-test="${testId}"]`).element as HTMLInputElement
}

const ROW_FLAGS = {
  users: 'users.balance_adjust',
  accounts: 'accounts.upstream_address',
  redeem_codes: 'redeem_codes.issue',
  orders: 'orders.refund'
} as const

describe('OperatorPermissionsTable', () => {
  it('checking manage also checks and locks view', async () => {
    const { wrapper, emitted } = mountTable({ modules: {}, flags: [] })

    expect(checkbox(wrapper, 'operator-view-users').checked).toBe(false)
    expect(checkbox(wrapper, 'operator-manage-users').checked).toBe(false)

    await wrapper.get('[data-test="operator-manage-users"]').setValue(true)
    expect(emitted.at(-1)).toEqual({ modules: { users: 'manage' }, flags: [] })
    expect(checkbox(wrapper, 'operator-view-users').checked).toBe(true)
    expect(checkbox(wrapper, 'operator-view-users').disabled).toBe(true)
    expect(checkbox(wrapper, 'operator-manage-users').checked).toBe(true)

    // Unchecking manage keeps view and unlocks it.
    await wrapper.get('[data-test="operator-manage-users"]').setValue(false)
    expect(emitted.at(-1)).toEqual({ modules: { users: 'view' }, flags: [] })
    expect(checkbox(wrapper, 'operator-view-users').checked).toBe(true)
    expect(checkbox(wrapper, 'operator-view-users').disabled).toBe(false)

    await wrapper.get('[data-test="operator-view-users"]').setValue(false)
    expect(emitted.at(-1)).toEqual({ modules: {}, flags: [] })
  })

  it('keeps each flag in its own row with one on and off sentence', () => {
    const { wrapper } = mountTable({ modules: {}, flags: [] })

    for (const moduleId of ADMIN_MODULE_IDS) {
      const moduleRow = wrapper.get(`[data-test="operator-module-row-${moduleId}"]`)
      expect(moduleRow.findAll('td').every(cell => cell.classes().includes('py-0.5'))).toBe(true)
      // A module row holds only its view and manage boxes.
      expect(moduleRow.findAll('input[type="checkbox"]').map((input) => input.attributes('data-test'))).toEqual([
        `operator-view-${moduleId}`,
        `operator-manage-${moduleId}`
      ])
      const flag = ROW_FLAGS[moduleId as keyof typeof ROW_FLAGS]
      const nextRow = moduleRow.element.nextElementSibling
      if (flag) {
        // The flag sits in the row right under its module.
        expect(nextRow?.getAttribute('data-test')).toBe(`operator-flag-row-${flag}`)
      } else {
        expect(nextRow).toBeNull()
      }
    }

    const flagRows = wrapper.findAll('[data-test^="operator-flag-row-"]')
    expect(flagRows.map((row) => row.attributes('data-test'))).toEqual([
      'operator-flag-row-users.balance_adjust',
      'operator-flag-row-accounts.upstream_address',
      'operator-flag-row-redeem_codes.issue',
      'operator-flag-row-orders.refund',
      'operator-flag-row-data.export'
    ])
    expect(new Set(flagRows.map((row) => row.attributes('data-test')!.replace('operator-flag-row-', '')))).toEqual(
      new Set(ADMIN_FLAG_IDS)
    )

    for (const row of flagRows) {
      // One two-state control per flag row, and one hint.
      expect(row.findAll('input[type="checkbox"]')).toHaveLength(1)
      const hints = row.findAll('[data-test="operator-flag-hint"]')
      expect(hints).toHaveLength(1)
      const hint = hints[0].text()
      expect(hint.split('开：')).toHaveLength(2)
      expect(hint.split('关：')).toHaveLength(2)
      expect(hint.indexOf('开：')).toBe(0)
      expect(hint.indexOf('关：')).toBeGreaterThan(0)
    }
    expect(wrapper.get('[data-test="operator-flag-row-accounts.upstream_address"]').text()).toContain('显示上游地址')
    expect(wrapper.get('[data-test="operator-flag-row-users.balance_adjust"]').text()).not.toContain('把账号加入或移出分组')
  })

  it('writes one On and one Off sentence per flag in English', () => {
    const { wrapper } = mountTable({ modules: {}, flags: [] }, 'en')

    const hints = wrapper.findAll('[data-test="operator-flag-hint"]').map((hint) => hint.text())
    expect(hints).toHaveLength(ADMIN_FLAG_IDS.length)
    expect(wrapper.get('[data-test="operator-flag-row-users.balance_adjust"]').text()).not.toContain('put accounts into or out of groups')
    for (const hint of hints) {
      expect(hint.startsWith('On: ')).toBe(true)
      expect(hint.split('On: ')).toHaveLength(2)
      expect(hint.split(' Off: ')).toHaveLength(2)
    }
  })

  it("clears a row's flag when the row is cleared", async () => {
    const { wrapper, emitted } = mountTable({
      modules: { users: 'view', accounts: 'manage' },
      flags: ['accounts.upstream_address', 'data.export', 'users.balance_adjust']
    })

    expect(checkbox(wrapper, 'operator-flag-users.balance_adjust').checked).toBe(true)
    expect(checkbox(wrapper, 'operator-flag-users.balance_adjust').disabled).toBe(false)

    await wrapper.get('[data-test="operator-view-users"]').setValue(false)
    expect(emitted.at(-1)).toEqual({
      modules: { accounts: 'manage' },
      flags: ['accounts.upstream_address', 'data.export']
    })
    expect(checkbox(wrapper, 'operator-flag-users.balance_adjust').checked).toBe(false)
    expect(checkbox(wrapper, 'operator-flag-users.balance_adjust').disabled).toBe(true)

    // Dropping manage to view keeps the row, so its flag stays.
    await wrapper.get('[data-test="operator-manage-accounts"]').setValue(false)
    expect(emitted.at(-1)).toEqual({
      modules: { accounts: 'view' },
      flags: ['accounts.upstream_address', 'data.export']
    })

    // A flag can be turned on only while its row is granted; data.export has no row.
    expect(checkbox(wrapper, 'operator-flag-orders.refund').disabled).toBe(true)
    const emittedBefore = emitted.length
    await wrapper.get('[data-test="operator-flag-orders.refund"]').trigger('change')
    expect(emitted).toHaveLength(emittedBefore)
    await wrapper.get('[data-test="operator-flag-data.export"]').setValue(false)
    expect(emitted.at(-1)).toEqual({ modules: { accounts: 'view' }, flags: ['accounts.upstream_address'] })
  })
})
