import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { GroupSchedulingDocument, GroupSchedulingPolicy } from '@/types/scheduling'
import englishScheduling from '@/i18n/locales/en/admin/scheduling'
import chineseScheduling from '@/i18n/locales/zh/admin/scheduling'
const { getGroupPolicy, saveGroupPolicy, explain, list, setSchedulable, getAllIncludingInactive, route, leaveGuards } = vi.hoisted(() => ({
  getGroupPolicy: vi.fn(), saveGroupPolicy: vi.fn(), explain: vi.fn(), list: vi.fn(), setSchedulable: vi.fn(), getAllIncludingInactive: vi.fn(),
  route: { query: {} as Record<string, string> }, leaveGuards: [] as Array<() => unknown>
}))
vi.mock('@/api/admin/scheduling', () => ({ default: { getGroupPolicy, saveGroupPolicy, explain } }))
vi.mock('@/api/admin/accounts', () => ({ default: { list, setSchedulable } }))
vi.mock('@/api/admin/groups', () => ({ default: { getAllIncludingInactive } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('vue-router', () => ({ useRoute: () => route, onBeforeRouteLeave: (guard: () => unknown) => leaveGuards.push(guard) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import SchedulingView from '../SchedulingView.vue'
import { useSchedulingModeStore } from '@/stores/schedulingMode'

const wrappers: ReturnType<typeof mount>[] = []
function document(groupID = 2, version = 7): GroupSchedulingDocument {
  return { group_id: groupID, version, configured: true, default_scope: groupID === 0 ? 'ungrouped' : 'group', policy: {
    group_id: groupID, version, accounts: [{ account_id: 1, priority: 5, traffic_weight: 1 }, { account_id: 2, priority: 1, traffic_weight: 2 }],
    first_output_timeout_ms: 120000, total_wait_timeout_ms: 240000, max_attempts: 3
  } }
}
async function setup() {
  const wrapper = mount(SchedulingView, { global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' }, RouterLink: { template: '<a><slot /></a>' } } } })
  wrappers.push(wrapper); await flushPromises(); return wrapper
}
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
function accountSwitch(wrapper: ReturnType<typeof mount>, id: number) {
  return wrapper.get(`[data-testid="account-row-${id}"] button[role="switch"]`)
}
beforeEach(() => {
  vi.resetAllMocks(); useSchedulingModeStore().document = { mode: 'controlled', version: 1 }; route.query = {}; leaveGuards.length = 0
  getAllIncludingInactive.mockResolvedValue([{ id: 2, name: 'Group A' }, { id: 3, name: 'Group B' }, { id: 4, name: 'Group C' }])
  list.mockResolvedValue({ items: [
    { id: 1, name: 'Primary', status: 'active', schedulable: true, platform: 'openai', group_ids: [2] },
    { id: 2, name: 'Secondary', status: 'active', schedulable: true, platform: 'openai', group_ids: [2] },
    { id: 99, name: 'Outside group', status: 'active', schedulable: true, group_ids: [3] }
  ], total: 3 })
  getGroupPolicy.mockImplementation(async (id: number) => document(id))
  saveGroupPolicy.mockImplementation(async (policy: GroupSchedulingPolicy) => ({ ...document(policy.group_id, 8), policy: { ...policy, version: 8 } }))
  setSchedulable.mockImplementation(async (id: number, schedulable: boolean) => ({ id, schedulable }))
  explain.mockResolvedValue({ policy_version: 7, mode: 'swrr', candidates: [], readonly: true })
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.restoreAllMocks() })

describe('group scheduling editor', () => {
  it('retries a failed initial group list through the existing refresh action', async () => {
    getAllIncludingInactive.mockRejectedValueOnce(new Error('Group list unavailable'))
    const wrapper = await setup()
    expect(wrapper.get('[role="alert"]').text()).toContain('Group list unavailable')
    expect(wrapper.get('[data-testid="reload-policy"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="reload-policy"]').trigger('click'); await flushPromises()
    expect(getAllIncludingInactive).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="priority-1"]').exists()).toBe(true)
  })
  it('renders structured migration warnings as readable messages without exposing wire objects', async () => {
    getGroupPolicy.mockResolvedValue({ ...document(), migration_warnings: [{ code: 'legacy_model_policies_ignored', message: '旧模型配置只保留记录，不自动导入。', models: ['previous-model'] }] })
    const wrapper = await setup()
    expect(wrapper.get('ul li').text()).toBe('旧模型配置只保留记录，不自动导入。')
    expect(wrapper.text()).not.toContain('legacy_model_policies_ignored')
    expect(wrapper.text()).not.toContain('previous-model')
    expect(wrapper.get('[data-testid="save-policy"]').attributes('disabled')).toBeDefined()
  })
  it('loads all group rules without asking for a model, sorts priority and never includes foreign accounts', async () => {
    const wrapper = await setup()
    expect(getGroupPolicy).toHaveBeenCalledWith(2, expect.any(AbortSignal))
    expect(wrapper.findAll('[data-testid^="account-row-"]').map(row => row.attributes('data-testid'))).toEqual(['account-row-2', 'account-row-1'])
    expect(wrapper.text()).not.toContain('Outside group')
    expect(wrapper.find('[data-testid="scope-model"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="policy-mode"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="add-profile"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="first-output-wait"]').element).toHaveProperty('value', '120')
    expect(saveGroupPolicy).not.toHaveBeenCalled()
  })
  it('keeps enabled accounts together, then sorts each state by priority and weight', async () => {
    const payload = document()
    payload.policy.accounts = [
      { account_id: 1, priority: 2, traffic_weight: 100 },
      { account_id: 2, priority: 1, traffic_weight: 5 },
      { account_id: 3, priority: 1, traffic_weight: 10 },
      { account_id: 4, priority: 0, traffic_weight: 1 },
      { account_id: 5, priority: 1, traffic_weight: 5 },
      { account_id: 6, priority: 1, traffic_weight: 10 }
    ]
    getGroupPolicy.mockResolvedValue(payload)
    list.mockResolvedValue({ items: payload.policy.accounts.map(({ account_id }) => ({
      id: account_id, name: `Account ${account_id}`, status: 'active',
      schedulable: account_id <= 3, group_ids: [2]
    })), total: 6 })
    const wrapper = await setup()
    const rowOrder = () => wrapper.findAll('[data-testid^="account-row-"]').map(row => row.attributes('data-testid'))
    expect(rowOrder()).toEqual(['account-row-3', 'account-row-2', 'account-row-1', 'account-row-4', 'account-row-6', 'account-row-5'])

    await accountSwitch(wrapper, 4).trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenCalledWith(4, true)
    expect(rowOrder()).toEqual(['account-row-4', 'account-row-3', 'account-row-2', 'account-row-1', 'account-row-6', 'account-row-5'])
  })
  it('keeps the row in place while editing priority and reorders only after save', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="priority-1"]').setValue(-2)
    expect(wrapper.findAll('[data-testid^="account-row-"]').map(row => row.attributes('data-testid'))).toEqual(['account-row-2', 'account-row-1'])
    await wrapper.get('[data-testid="save-policy-top"]').trigger('click'); await flushPromises()
    expect(wrapper.findAll('[data-testid^="account-row-"]').map(row => row.attributes('data-testid'))).toEqual(['account-row-1', 'account-row-2'])
  })
  it('hides controlled configuration in original mode without loading or changing its policy', async () => {
    useSchedulingModeStore().document = { mode: 'sub2api', version: 2 }
    const wrapper = await setup()
    expect(wrapper.text()).toContain('modeSettings.inactive')
    expect(wrapper.find('[data-testid="save-policy"]').exists()).toBe(false)
    expect(getGroupPolicy).not.toHaveBeenCalled(); expect(saveGroupPolicy).not.toHaveBeenCalled()
  })
  it('renders at most fifty account rows while preserving the complete policy when saving', async () => {
    const payload = document(); payload.policy.accounts = Array.from({ length: 125 }, (_, index) => ({ account_id: index + 1, priority: 1, traffic_weight: 1 }))
    getGroupPolicy.mockResolvedValue(payload)
    const wrapper = await setup()
    expect(wrapper.findAll('[data-testid^="account-row-"]')).toHaveLength(50)
    await wrapper.get('[data-testid="weight-1"]').setValue(7)
    await wrapper.get('[data-testid="save-policy-top"]').trigger('click'); await flushPromises()
    expect(saveGroupPolicy.mock.calls[0][0].accounts).toHaveLength(125)
  })
  it('honors an explicit group link and filters account metadata by that group', async () => {
    route.query.group_id = '3'; await setup()
    expect(getGroupPolicy).toHaveBeenCalledWith(3, expect.any(AbortSignal))
    expect(list).toHaveBeenCalledWith(1, 100, { group: '3', lite: 'true' }, { signal: expect.any(AbortSignal) })
  })
  it('saves account fields and one group policy with CAS, with no model matrix', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="priority-1"]').setValue(-2)
    await wrapper.get('[data-testid="weight-1"]').setValue(8)
    await wrapper.get('[data-testid="first-output-wait"]').setValue(150)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(saveGroupPolicy).toHaveBeenCalledTimes(1)
    expect(saveGroupPolicy.mock.calls[0][0]).toMatchObject({ group_id: 2, accounts: [
      { account_id: 1, priority: -2, traffic_weight: 8 }, { account_id: 2, priority: 1, traffic_weight: 2 }
    ], first_output_timeout_ms: 150000, total_wait_timeout_ms: 300000, max_attempts: 3 })
    expect(saveGroupPolicy.mock.calls[0][0]).not.toHaveProperty('model')
    expect(saveGroupPolicy.mock.calls[0][0]).not.toHaveProperty('profiles')
    expect(saveGroupPolicy.mock.calls[0][1]).toBe(7)
    expect(wrapper.get('[data-testid="save-policy"]').attributes('disabled')).toBeDefined()
  })
  it('applies batch values only to selected visible account IDs', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="account-search"]').setValue('Primary')
    await wrapper.get('[data-testid="select-visible"]').setValue(true)
    await wrapper.get('[data-testid="batch-priority"]').setValue(0)
    await wrapper.get('[data-testid="batch-weight"]').setValue(6)
    await wrapper.get('[data-testid="apply-batch"]').trigger('click')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(saveGroupPolicy.mock.calls[0][0].accounts).toEqual([
      { account_id: 1, priority: 0, traffic_weight: 6 }, { account_id: 2, priority: 1, traffic_weight: 2 }
    ])
  })
  it('keeps a draft after a conflict and requires deliberate reload, never overwriting automatically', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="weight-1"]').setValue(9)
    saveGroupPolicy.mockRejectedValueOnce({ response: { status: 409 } })
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[data-testid="weight-1"]').element).toHaveProperty('value', '9')
    expect(wrapper.get('[role="alert"]').text()).toContain('policyConflict')
    await wrapper.get('form').trigger('submit')
    expect(saveGroupPolicy).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-testid="reload-policy"]').trigger('click')
    expect(getGroupPolicy).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-testid="discard-draft"]').trigger('click'); await flushPromises()
    expect(getGroupPolicy).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-testid="weight-1"]').element).toHaveProperty('value', '1')
  })
  it('asks before discarding on group change and keeps the exact original group until confirmed', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="weight-1"]').setValue(4)
    await wrapper.get('[data-testid="scope-group"]').setValue(3)
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(true)
    expect(getGroupPolicy).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-testid="keep-draft"]').trigger('click')
    expect(wrapper.get('[data-testid="scope-group"]').element).toHaveProperty('value', '2')
    expect(wrapper.get('[data-testid="weight-1"]').element).toHaveProperty('value', '4')
    await wrapper.get('[data-testid="scope-group"]').setValue(3)
    await wrapper.get('[data-testid="discard-draft"]').trigger('click'); await flushPromises()
    expect(getGroupPolicy).toHaveBeenLastCalledWith(3, expect.any(AbortSignal))
    expect(saveGroupPolicy).not.toHaveBeenCalled()
  })
  it('ignores a late policy response from a previously selected group', async () => {
    const wrapper = await setup(); const delayed = deferred<GroupSchedulingDocument>()
    getGroupPolicy.mockImplementation((id: number) => id === 3 ? delayed.promise : Promise.resolve(document(id, 12)))
    await wrapper.get('[data-testid="scope-group"]').setValue(3)
    await wrapper.get('[data-testid="scope-group"]').setValue(4); await flushPromises()
    delayed.resolve({ ...document(3), policy: { ...document(3).policy, accounts: [{ account_id: 99, priority: 0, traffic_weight: 90 }] } }); await flushPromises()
    expect(wrapper.get('[data-testid="scope-group"]').element).toHaveProperty('value', '4')
    expect(wrapper.find('[data-testid="account-row-99"]').exists()).toBe(false)
    await wrapper.get('[data-testid="weight-1"]').setValue(5)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(saveGroupPolicy.mock.calls[0][0].group_id).toBe(4)
    expect(saveGroupPolicy.mock.calls[0][1]).toBe(12)
  })
  it('preserves manual total wait and rejects a total below the per-account limit', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="automatic-total"]').setValue(false)
    await wrapper.get('[data-testid="total-wait"]').setValue(500)
    await wrapper.get('[data-testid="first-output-wait"]').setValue(180)
    expect(wrapper.get('[data-testid="total-wait"]').element).toHaveProperty('value', '500')
    await wrapper.get('[data-testid="total-wait"]').setValue(60)
    await wrapper.get('form').trigger('submit')
    expect(saveGroupPolicy).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('invalidTotalWait')
  })
  it('supports zero weight without any pause modal or troubleshooting entry', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="weight-1"]').setValue(0)
    expect(wrapper.get('[data-testid="weight-1"]').element).toHaveProperty('value', '0')
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('true')
    expect(wrapper.find('[data-testid="routing-diagnostics"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="preview-model"]').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toMatch(/controlTitle|diagnosticsTitle|failureDomains/)
    expect(explain).not.toHaveBeenCalled()
    expect(saveGroupPolicy).not.toHaveBeenCalled()
  })
  it('uses the account scheduling switch without a status description or policy save', async () => {
    list.mockResolvedValue({ items: [{ id: 1, name: 'Disabled', schedulable: false, status: 'active' }], total: 1 })
    const wrapper = await setup()
    const toggle = accountSwitch(wrapper, 1)
    expect(toggle.attributes('type')).toBe('button')
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="account-row-1"]').text()).not.toContain('groupPolicy.disabled')
    expect(wrapper.get('[data-testid="account-row-1"]').text()).not.toContain('groupPolicy.available')
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenCalledWith(1, true)
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(saveGroupPolicy).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="save-policy"]').attributes('disabled')).toBeDefined()
    expect(accountSwitch(wrapper, 2).attributes('disabled')).toBeDefined()
    expect(explain).not.toHaveBeenCalled()
  })
  it('shows the same account multiplier formatting as account management', async () => {
    list.mockResolvedValue({ items: [
      { id: 1, name: 'Primary', status: 'active', schedulable: true, rate_multiplier: 0.035 },
      { id: 2, name: 'Secondary', status: 'active', schedulable: true, rate_multiplier: null }
    ], total: 2 })
    const wrapper = await setup()
    expect(wrapper.get('[data-testid="account-rate-1"]').text()).toBe('0.035x')
    expect(wrapper.get('[data-testid="account-rate-2"]').text()).toBe('1.00x')
    expect(wrapper.findAll('[data-testid^="account-rate-"]')).toHaveLength(2)
  })
  it('locks only the pending account and rejects duplicate clicks', async () => {
    const first = deferred<{ id: number; schedulable: boolean }>()
    const second = deferred<{ id: number; schedulable: boolean }>()
    setSchedulable.mockImplementation((id: number) => id === 1 ? first.promise : second.promise)
    const wrapper = await setup()
    const primary = accountSwitch(wrapper, 1)
    const secondary = accountSwitch(wrapper, 2)
    await primary.trigger('click')
    await primary.trigger('click')
    expect(primary.attributes('aria-busy')).toBe('true')
    expect(primary.attributes('disabled')).toBeDefined()
    expect(secondary.attributes('disabled')).toBeUndefined()
    await secondary.trigger('click')
    expect(setSchedulable.mock.calls).toEqual([[1, false], [2, false]])
    first.resolve({ id: 1, schedulable: false }); await flushPromises()
    expect(primary.attributes('aria-checked')).toBe('false')
    expect(primary.attributes('disabled')).toBeUndefined()
    expect(secondary.attributes('disabled')).toBeDefined()
    second.resolve({ id: 2, schedulable: false }); await flushPromises()
    expect(secondary.attributes('aria-checked')).toBe('false')
    expect(saveGroupPolicy).not.toHaveBeenCalled()
  })
  it('keeps the confirmed switch state after failure and permits retry', async () => {
    setSchedulable.mockRejectedValueOnce(new Error('temporary failure'))
    const wrapper = await setup()
    const toggle = accountSwitch(wrapper, 1)
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(toggle.attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[role="alert"]').text()).toContain('temporary failure')
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenCalledTimes(2)
    expect(toggle.attributes('aria-checked')).toBe('false')
  })
  it('refreshes external account changes and retains a local policy draft', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="weight-1"]').setValue(9)
    list.mockResolvedValue({ items: [
      { id: 1, name: 'Primary', status: 'active', schedulable: false, rate_multiplier: 0.3 },
      { id: 2, name: 'Secondary', status: 'active', schedulable: true, rate_multiplier: 1 }
    ], total: 2 })
    await wrapper.get('[data-testid="reload-policy"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="weight-1"]').element).toHaveProperty('value', '9')
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="account-rate-1"]').text()).toBe('0.30x')
    expect(saveGroupPolicy).not.toHaveBeenCalled()
  })
  it('keeps the policy draft separate from an immediate scheduling toggle', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="weight-1"]').setValue(7)
    await accountSwitch(wrapper, 1).trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenCalledWith(1, false)
    expect(wrapper.get('[data-testid="weight-1"]').element).toHaveProperty('value', '7')
    expect(wrapper.get('[data-testid="save-policy"]').attributes('disabled')).toBeUndefined()
    expect(saveGroupPolicy).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(saveGroupPolicy.mock.calls[0][0].accounts[0]).toMatchObject({ account_id: 1, traffic_weight: 7 })
  })
  it.each(['focus', 'visibilitychange'])('refreshes account state on %s without replacing an unsaved policy', async eventName => {
    vi.spyOn(globalThis.document, 'hidden', 'get').mockReturnValue(false)
    const wrapper = await setup()
    await wrapper.get('[data-testid="weight-1"]').setValue(9)
    await wrapper.get('[data-testid="priority-1"]').setValue(-2)
    list.mockResolvedValueOnce({ items: [
      { id: 1, name: 'Primary', status: 'active', schedulable: false, rate_multiplier: 0.035 },
      { id: 2, name: 'Secondary', status: 'active', schedulable: true, rate_multiplier: 1 }
    ], total: 2 })
    const target = eventName === 'focus' ? window : globalThis.document
    target.dispatchEvent(new Event(eventName)); await flushPromises()
    expect(list).toHaveBeenCalledTimes(2)
    expect(getGroupPolicy).toHaveBeenCalledTimes(1)
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="account-rate-1"]').text()).toBe('0.035x')
    expect(wrapper.get('[data-testid="weight-1"]').element).toHaveProperty('value', '9')
    expect(wrapper.get('[data-testid="priority-1"]').element).toHaveProperty('value', '-2')
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(false)
    expect(saveGroupPolicy).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(saveGroupPolicy.mock.calls[0][0].accounts[0]).toMatchObject({ account_id: 1, priority: -2, traffic_weight: 9 })
    expect(saveGroupPolicy.mock.calls[0][1]).toBe(7)
  })
  it.each(['metadata first', 'toggle first'])('keeps a shared account switch synchronized after changing groups: %s', async completionOrder => {
    const pendingToggle = deferred<{ id: number; schedulable: boolean }>()
    const nextGroupDetails = deferred<unknown>()
    setSchedulable.mockReturnValueOnce(pendingToggle.promise)
    const wrapper = await setup()
    await accountSwitch(wrapper, 1).trigger('click')
    list.mockReturnValueOnce(nextGroupDetails.promise)
    await wrapper.get('[data-testid="scope-group"]').setValue(3); await flushPromises()
    expect(list).toHaveBeenLastCalledWith(1, 100, { group: '3', lite: 'true' }, { signal: expect.any(AbortSignal) })
    const details = { items: [
      { id: 1, name: 'Shared account in Group B', schedulable: true, status: 'active', rate_multiplier: 0.3 },
      { id: 2, name: 'Secondary', schedulable: true, status: 'active', rate_multiplier: 1 }
    ], total: 2 }
    if (completionOrder === 'metadata first') {
      nextGroupDetails.resolve(details); await flushPromises()
      pendingToggle.resolve({ id: 1, schedulable: false }); await flushPromises()
    } else {
      pendingToggle.resolve({ id: 1, schedulable: false }); await flushPromises()
      nextGroupDetails.resolve(details); await flushPromises()
    }
    expect(wrapper.get('[data-testid="scope-group"]').element).toHaveProperty('value', '3')
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('false')
    expect(accountSwitch(wrapper, 1).attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="account-row-1"]').text()).toContain('Shared account in Group B')
    expect(wrapper.get('[data-testid="account-rate-1"]').text()).toBe('0.30x')
    expect(setSchedulable.mock.calls).toEqual([[1, false]])
    expect(saveGroupPolicy).not.toHaveBeenCalled()
  })
  it('does not replace an accepted toggle with an older focus refresh response', async () => {
    vi.spyOn(globalThis.document, 'hidden', 'get').mockReturnValue(false)
    const pendingToggle = deferred<{ id: number; schedulable: boolean }>()
    const staleDetails = deferred<unknown>()
    setSchedulable.mockReturnValueOnce(pendingToggle.promise)
    const wrapper = await setup()
    await accountSwitch(wrapper, 1).trigger('click')
    list.mockReturnValueOnce(staleDetails.promise)
    window.dispatchEvent(new Event('focus')); await flushPromises()
    expect(list).toHaveBeenCalledTimes(2)
    pendingToggle.resolve({ id: 1, schedulable: false }); await flushPromises()
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('false')
    staleDetails.resolve({ items: [
      { id: 1, name: 'Primary', status: 'active', schedulable: true },
      { id: 2, name: 'Secondary', status: 'active', schedulable: true }
    ], total: 2 }); await flushPromises()
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('false')
    expect(accountSwitch(wrapper, 1).attributes('disabled')).toBeUndefined()
    expect(getGroupPolicy).toHaveBeenCalledTimes(1)
  })
  it('keeps unrelated account changes from a focus refresh during a toggle', async () => {
    vi.spyOn(globalThis.document, 'hidden', 'get').mockReturnValue(false)
    const pendingToggle = deferred<{ id: number; schedulable: boolean }>()
    const staleDetails = deferred<unknown>()
    setSchedulable.mockReturnValueOnce(pendingToggle.promise)
    const wrapper = await setup()
    await accountSwitch(wrapper, 1).trigger('click')
    list.mockReturnValueOnce(staleDetails.promise)
    window.dispatchEvent(new Event('focus')); await flushPromises()
    pendingToggle.resolve({ id: 1, schedulable: false }); await flushPromises()
    staleDetails.resolve({ items: [
      { id: 1, name: 'Primary', status: 'active', schedulable: true },
      { id: 2, name: 'Secondary', status: 'active', schedulable: false, rate_multiplier: 0.3 }
    ], total: 2 }); await flushPromises()
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('false')
    expect(accountSwitch(wrapper, 2).attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="account-rate-2"]').text()).toBe('0.30x')
  })
  it('prevents a toggle while an earlier metadata refresh is pending', async () => {
    vi.spyOn(globalThis.document, 'hidden', 'get').mockReturnValue(false)
    const pendingDetails = deferred<unknown>()
    const wrapper = await setup()
    list.mockReturnValueOnce(pendingDetails.promise)
    window.dispatchEvent(new Event('focus')); await flushPromises()
    expect(accountSwitch(wrapper, 1).attributes('disabled')).toBeDefined()
    await accountSwitch(wrapper, 1).trigger('click')
    expect(setSchedulable).not.toHaveBeenCalled()
    pendingDetails.resolve({ items: [
      { id: 1, name: 'Primary', status: 'active', schedulable: false },
      { id: 2, name: 'Secondary', status: 'active', schedulable: true }
    ], total: 2 }); await flushPromises()
    expect(accountSwitch(wrapper, 1).attributes('disabled')).toBeUndefined()
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('false')
    await accountSwitch(wrapper, 1).trigger('click'); await flushPromises()
    expect(setSchedulable.mock.calls).toEqual([[1, true]])
    expect(accountSwitch(wrapper, 1).attributes('aria-checked')).toBe('true')
  })
  it('keeps missing metadata disabled until a later refresh reads the account', async () => {
    vi.spyOn(globalThis.document, 'hidden', 'get').mockReturnValue(false)
    list.mockResolvedValueOnce({ items: [{ id: 1, name: 'Primary', status: 'active', schedulable: true }], total: 1 })
    const wrapper = await setup()
    expect(accountSwitch(wrapper, 2).attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="account-rate-2"]').text()).toBe('-')
    await accountSwitch(wrapper, 2).trigger('click')
    expect(setSchedulable).not.toHaveBeenCalled()
    window.dispatchEvent(new Event('focus')); await flushPromises()
    expect(accountSwitch(wrapper, 2).attributes('disabled')).toBeUndefined()
    expect(accountSwitch(wrapper, 2).attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="account-rate-2"]').text()).toBe('1.00x')
    await accountSwitch(wrapper, 2).trigger('click'); await flushPromises()
    expect(setSchedulable.mock.calls).toEqual([[2, false]])
  })
  it('blocks leaving with dirty values until the administrator chooses', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="weight-1"]').setValue(6)
    const result = leaveGuards[0]() as Promise<boolean>
    await flushPromises(); await wrapper.get('[data-testid="keep-draft"]').trigger('click')
    await expect(result).resolves.toBe(false)
  })
  it('supports explicit ungrouped scope and the server-defined simple-mode all-account scope', async () => {
    route.query.group_id = '0'
    getGroupPolicy.mockResolvedValue({ ...document(0), default_scope: 'all_accounts' })
    const wrapper = await setup()
    expect(wrapper.get('[data-testid="scope-group"] option[value="0"]').text()).toContain('allAccounts')
    expect(list).toHaveBeenCalledWith(1, 100, { group: undefined, lite: 'true' }, { signal: expect.any(AbortSignal) })
  })
  it('refuses a server response for another group and leaves nothing editable', async () => {
    getGroupPolicy.mockResolvedValue(document(77))
    const wrapper = await setup()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('invalidScope')
  })
  it('has matching Chinese and English keys with no same-tier attempt cap in new copy', () => {
    expect(Object.keys(chineseScheduling.scheduling.groupPolicy).sort()).toEqual(Object.keys(englishScheduling.scheduling.groupPolicy).sort())
    expect(chineseScheduling.scheduling.groupPolicy.accountHint).toContain('试完同级可用账号')
    expect(chineseScheduling.scheduling.groupPolicy.waitHint).not.toContain('同级最多')
  })
})
