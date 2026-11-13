import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { SchedAccountDetail } from '@/types/schedQuality'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import { recoveryLines } from '@/composables/useAccountRecovery'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn(), reset: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
// Reactive, so a permission change after mount reaches the mounted drawer.
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ granted: [] as string[] })
})
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return {
    useAdminPermission: (module: string) => ({
      canView: computed(() => perms.granted.includes(`${module}:view`)),
      canManage: computed(() => perms.granted.includes(`${module}:manage`))
    })
  }
})
vi.mock('@/stores/adminAccess', () => ({ useAdminAccessStore: () => ({ can: (key: string) => perms.granted.includes(key) }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: () => true
  })
}))

enableAutoUnmount(afterEach)

const detail = () => structuredClone(detailFixture) as SchedAccountDetail
const panel = () => document.querySelector<HTMLElement>('[data-drawer-panel]')!
const group = (groupId: number) => document.querySelector<HTMLElement>(`[data-sq-group-row="${groupId}"]`)!
const modelRow = (groupId: number, model = 'gpt-6.1-sol') => document.querySelector<HTMLElement>(`[data-sq-model-row="${groupId}:${model}"]`)!
const trigger = (name: string) => document.querySelector<HTMLElement>(`[data-sq-trigger="${name}"]`)
const mountDrawer = (props: Record<string, unknown> = {}) =>
  mount(SchedAccountDrawer, { props, attachTo: document.body, global: { stubs: { RouterLink: { template: '<a data-router-link><slot /></a>' } } } })
async function openWith(d: SchedAccountDetail, props: Record<string, unknown> = {}) {
  api.accountDetail.mockResolvedValue(d)
  const wrapper = mountDrawer(props)
  openSchedDrawer({ id: 479, name: 'acc-03' })
  await flushPromises()
  return wrapper
}

const ALL = ['accounts:view', 'accounts:manage', 'groups:view', 'usage:view']

beforeEach(() => {
  closeSchedDrawer()
  closeSchedBoard()
  perms.granted = ALL
  document.documentElement.dataset.motion = 'reduced'
  Element.prototype.scrollIntoView = vi.fn()
  api.accountDetail.mockReset().mockResolvedValue(detail())
  api.clearOverride.mockReset()
  api.setOverride.mockReset()
  api.reset.mockReset()
  recoveryLines.clear()
})

afterEach(() => {
  document.querySelectorAll('body > button').forEach((el) => el.remove())
})

afterAll(() => {
  delete document.documentElement.dataset.motion
})

describe('SchedAccountDrawer — groups (FINAL §7)', () => {
  it('puts each group\'s three lines over the columns they apply to, then one row per model with its evidence', async () => {
    await openWith(detail())
    expect(Array.from(group(53).querySelectorAll('[data-sq-line]')).map((el) => el.textContent)).toEqual(['10s', '5%', '70%'])
    expect(Array.from(group(54).querySelectorAll('[data-sq-line]')).map((el) => el.textContent)).toEqual(['15s', '8%', '60%'])

    const row = modelRow(53)
    expect(row.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.state.substandard·admin.schedQuality.dim.ttfo')
    const [ttfo, error, cache] = ['ttfo', 'error', 'cache'].map((dim) => row.querySelector(`[data-sq-dim="${dim}"]`)!)
    // First output: requests over the line out of n, red for the failing dimension.
    expect(ttfo.textContent).toBe('11/42')
    expect(ttfo.querySelector('span')!.className).toBe('sq-red')
    // Error: the rate with its n, default colour when it passes.
    expect(error.querySelector('span')!.textContent).toBe('2.3%')
    expect(error.querySelector('span')!.className).toBe('')
    // Cache: grey for a short sample, marked as merged evidence.
    expect(cache.querySelector('span')!.className).toBe('sq-text-3')
    expect(cache.querySelector('[data-sq-merged]')).not.toBeNull()
    // Level and recovery time of the substandard row; a dash on a normal row.
    expect(row.textContent).toContain('admin.schedQuality.level:{"n":2}')
    expect(modelRow(54).querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.state.normal')
    expect(modelRow(54).querySelector('[data-sq-dim="ttfo"]')!.textContent).toBe('—')
  })

  it('marks a recovery time renewed from other groups', async () => {
    const d = detail()
    d.groups[0].by_model[0].other_group_ids = [54]
    await openWith(d)
    expect(modelRow(53).querySelector('[data-sq-other]')).not.toBeNull()
  })

  it('reads an exempt substandard row as normal and shows the exemption next to the group name', async () => {
    const d = detail()
    d.groups[0].by_model[0].exempt = true
    d.groups[0].exemption = { group_id: 53, expires_at: null, reason: '' }
    await openWith(d)
    expect(modelRow(53).querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.state.normal')
    expect(group(53).querySelector('[data-sq-override="exempt"]')).not.toBeNull()
    expect(trigger('override:53')!.textContent).toBe('admin.schedQuality.action.undoExempt')
  })

  it('opens the group\'s selection order from its name', async () => {
    await openWith(detail())
    trigger('order:53')!.click()
    expect(useSchedLayers().board.groupId).toBe(53)
  })
})

describe('SchedAccountDrawer — layer behaviour', () => {
  it('scrolls to the group it was opened for', async () => {
    mountDrawer()
    openSchedDrawer({ id: 479, name: 'acc-03' }, { groupId: 54 })
    await flushPromises()
    expect(api.accountDetail).toHaveBeenCalledWith(479, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(group(54).classList.contains('sq-target')).toBe(true)
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
  })

  it('keeps focus in the drawer when a refresh removes the focused control', async () => {
    const wrapper = await openWith(detail())
    const clear = trigger('auto:model_limit:gpt-6-astra')!
    clear.focus()
    await (wrapper.vm as unknown as { refresh: () => Promise<void> }).refresh()
    await flushPromises()
    expect(document.activeElement?.getAttribute('data-sq-trigger')).toBe('auto:model_limit:gpt-6-astra')

    api.accountDetail.mockResolvedValue({ ...detail(), auto_states: [] })
    await (wrapper.vm as unknown as { refresh: () => Promise<void> }).refresh()
    await flushPromises()
    expect(document.activeElement).toBe(panel())
  })

  it('closes on Escape and gives focus back to the control that opened it', async () => {
    mountDrawer()
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    expect(document.activeElement).toBe(panel())
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(document.querySelector('[data-drawer-panel]')).toBeNull()
    expect(document.activeElement).toBe(opener)
  })

  it('hides write actions without accounts:manage, the group link without groups:view and 记录 without usage:view', async () => {
    perms.granted = ['accounts:view']
    await openWith(detail())
    for (const name of ['drawer-exempt', 'drawer-reset', 'drawer-records', 'auto:model_limit:gpt-6-astra', 'override:53', 'order:53']) {
      expect(trigger(name)).toBeNull()
    }
    expect(group(53).querySelector('td')!.textContent!.trim()).toBe('晨星组')
  })
})

describe('SchedAccountDrawer — exemptions', () => {
  it('sets an exemption for one group from its row and cancels it again', async () => {
    await openWith(detail())
    trigger('override:54')!.click()
    await flushPromises()
    const after = detail()
    after.groups[1].exemption = { group_id: 54, expires_at: null, reason: '' }
    api.accountDetail.mockResolvedValue(after)
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    expect(api.setOverride).toHaveBeenCalledWith(479, expect.objectContaining({ group_id: 54, mode: 'exempt' }))
    expect(trigger('override:54')!.textContent).toBe('admin.schedQuality.action.undoExempt')
    trigger('override:54')!.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(479, 54)
  })

  it('cancels the all-groups exemption from a group row it covers and from the header, and shows 失败 by the header', async () => {
    const d = detail()
    d.exemptions = [{ group_id: 0, expires_at: null, reason: '' }]
    d.groups.forEach((g) => { g.exemption = d.exemptions[0] })
    api.clearOverride.mockResolvedValueOnce(undefined).mockRejectedValueOnce({ status: 500 })
    await openWith(d)
    expect(group(53).querySelector('[data-sq-override]')!.textContent!.replace(/\s+/g, ' ').trim())
      .toBe('admin.schedQuality.override.allGroups admin.schedQuality.state.exempted')
    trigger('override:53')!.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenLastCalledWith(479, 0)

    const header = trigger('drawer-exempt')!
    expect(header.textContent).toBe('admin.schedQuality.action.undoExempt')
    header.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenLastCalledWith(479, 0)
    expect(header.previousElementSibling?.textContent).toBe('admin.schedQuality.result.failed')
  })

  it('closes an open exemption popover when accounts:manage is withdrawn', async () => {
    await openWith(detail())
    trigger('drawer-exempt')!.click()
    await flushPromises()
    expect(document.querySelector('[data-sq-popover]')).not.toBeNull()
    perms.granted = ['accounts:view', 'groups:view']
    await flushPromises()
    expect(document.querySelector('[data-sq-popover]')).toBeNull()
    expect(api.setOverride).not.toHaveBeenCalled()
  })
})

describe('SchedAccountDrawer — 恢复 (FINAL §5)', () => {
  const result = { account_id: 479, reset_at: '2026-11-11T12:00:00Z', cleared: ['quality'], kept: [], failed: [] }

  it('offers 恢复 whatever the state, runs it at once and shows its result line', async () => {
    const d = detail()
    d.groups.forEach((g) => g.by_model.forEach((row) => { row.failing = false }))
    d.auto_states = []
    let answer!: (value: unknown) => void
    api.reset.mockReturnValue(new Promise((resolve) => { answer = resolve }))
    let refreshed!: () => void
    const onRecovered = vi.fn(() => new Promise<void>((resolve) => { refreshed = resolve }))
    await openWith(d, { onRecovered })

    trigger('drawer-reset')!.click()
    await flushPromises()
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(api.reset.mock.calls).toEqual([[479]])
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')
    expect(document.querySelector('.modal-content')).toBeNull()

    answer(result)
    await flushPromises()
    expect(onRecovered).toHaveBeenCalledWith(479)
    expect(panel().querySelector('[data-sq-recover-line] [data-sq-cleared]')!.textContent).toBe('admin.schedQuality.kind.quality')
    refreshed()
    await flushPromises()
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('false')
  })

  it('keeps 恢复 locked until its own detail reload is in, and reloads it once', async () => {
    await openWith(detail(), { onRecovered: async () => {} })
    let reloaded!: (value: SchedAccountDetail) => void
    api.accountDetail.mockReturnValue(new Promise((resolve) => { reloaded = resolve }))
    api.reset.mockResolvedValue(result)
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(api.accountDetail).toHaveBeenCalledTimes(2)
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')

    reloaded(detail())
    await flushPromises()
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('false')
    expect(api.accountDetail).toHaveBeenCalledTimes(2)
  })

  it('keeps 恢复 locked while the detail reload that replaced its own is still pending', async () => {
    const d = detail()
    d.groups[0].exemption = { group_id: 53, expires_at: null, reason: '' }
    await openWith(d, { onRecovered: async () => {} })
    const answers: Array<(value: SchedAccountDetail) => void> = []
    let aborted = 0
    api.accountDetail.mockImplementation((_id: number, { signal }: { signal: AbortSignal }) => new Promise((resolve, reject) => {
      answers.push(resolve)
      signal.addEventListener('abort', () => { aborted++; reject(new DOMException('aborted', 'AbortError')) }, { once: true })
    }))
    api.reset.mockResolvedValue(result)
    api.clearOverride.mockResolvedValue(undefined)
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(answers).toHaveLength(1)

    // Cancelling the exemption reloads the detail again: the recovery's reload is aborted and replaced.
    trigger('override:53')!.click()
    await flushPromises()
    expect([answers.length, aborted]).toEqual([2, 1])
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')

    answers[1](detail())
    await flushPromises()
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('false')
  })
})
