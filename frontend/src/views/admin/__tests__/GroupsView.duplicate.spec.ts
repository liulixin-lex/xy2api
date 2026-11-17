import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'
import { adminAPI } from '@/api/admin'

const {
  listGroups,
  duplicateGroup,
  updateGroup,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  showSuccess,
  showError
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  duplicateGroup: vi.fn(),
  updateGroup: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

const authState = vi.hoisted(() => ({ isSimpleMode: false }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      duplicate: duplicateGroup,
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      getAll: vi.fn(),
      getProfitControlPreview: vi.fn(),
      create: vi.fn(),
      update: updateGroup,
      delete: vi.fn(),
      updateSortOrder: vi.fn()
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn()
    }
  }
}))

vi.mock('@/api/admin/groupCategories', () => ({
  default: { list: vi.fn(async () => []), save: vi.fn(), setGroupCategories: vi.fn() }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const sourceGroup: AdminGroup = {
  id: 42,
  name: 'Primary',
  description: null,
  platform: 'openai',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  peak_rate_enabled: false,
  peak_start: '',
  peak_end: '',
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  messages_dispatch_model_config: undefined,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  model_allowlist: undefined,
  sort_order: 10
}

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>'
})

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>'
})

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false }
  },
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>'
})

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true
      }
    }
  })
}

describe('GroupsView duplicate action', () => {
  beforeEach(() => {
    authState.isSimpleMode = false
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [
      listGroups,
      duplicateGroup,
      updateGroup,
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      showSuccess,
      showError
    ]) {
      fn.mockReset()
    }

    listGroups.mockResolvedValue({
      items: [sourceGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    duplicateGroup.mockResolvedValue({
      ...sourceGroup,
      id: 43,
      name: 'Primary (Copy)',
      status: 'inactive'
    })
    getModelAllowlistCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getLiveCapability.mockResolvedValue({ supported: false })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('duplicates the selected group, reports success, and refreshes the list', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(duplicateGroup).toHaveBeenCalledTimes(1)
    expect(duplicateGroup).toHaveBeenCalledWith(42)
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.duplicateSuccess')
    expect(listGroups).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('hides advanced group actions in simple mode', async () => {
    authState.isSimpleMode = true
    const compositeGroup = { ...sourceGroup, platform: 'composite' }
    listGroups.mockResolvedValueOnce({ items: [compositeGroup], total: 1, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="group-duplicate"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="group-composite-routes"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="group-rate-multipliers"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="group-rpm-overrides"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('ignores repeated clicks while the duplicate request is in flight', async () => {
    let resolveDuplicate!: (value: AdminGroup) => void
    duplicateGroup.mockImplementationOnce(
      () => new Promise<AdminGroup>((resolve) => { resolveDuplicate = resolve })
    )
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.get('[data-testid="group-duplicate"]')
    void button.trigger('click')
    void button.trigger('click')
    await wrapper.vm.$nextTick()

    expect(duplicateGroup).toHaveBeenCalledTimes(1)
    expect(button.attributes('disabled')).toBeDefined()
    expect(button.attributes('title')).toBe('admin.groups.duplicating')

    resolveDuplicate({ ...sourceGroup, id: 43, name: 'Primary (Copy)', status: 'inactive' })
    await flushPromises()
    expect(wrapper.get('[data-testid="group-duplicate"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('shows the API error and restores the action when duplication fails', async () => {
    duplicateGroup.mockRejectedValueOnce(new Error('duplicate failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('duplicate failed')
    expect(wrapper.get('[data-testid="group-duplicate"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('does not report a successful duplicate as failed when the refresh fails', async () => {
    listGroups
      .mockResolvedValueOnce({
        items: [sourceGroup],
        total: 1,
        page: 1,
        page_size: 20,
        pages: 1
      })
      .mockRejectedValueOnce(new Error('refresh failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith('admin.groups.duplicateSuccess')
    expect(showError).toHaveBeenCalledWith('admin.groups.failedToLoad')
    expect(showError).not.toHaveBeenCalledWith('admin.groups.duplicateFailed')
    wrapper.unmount()
  })

  it('shows the standardized API message when updating a group fails', async () => {
    updateGroup.mockRejectedValueOnce({
      status: 409,
      code: 409,
      message: 'group name already exists',
      reason: 'GROUP_EXISTS'
    })
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find((button) => button.text() === 'common.edit')
    expect(editButton).toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('group name already exists')
    wrapper.unmount()
  })

  it('offers same-platform standard groups as availability fallback and saves the choice', async () => {
    const SelectStub = defineComponent({
      props: { modelValue: { type: null, default: null }, options: { type: Array, default: () => [] } },
      emits: ['update:modelValue'],
      template: '<div><button v-for="o in options" :key="String(o.value)" type="button" data-option :data-value="String(o.value)" @click="$emit(\'update:modelValue\', o.value)">{{ o.label }}</button></div>'
    })
    const groups = [
      { ...sourceGroup, availability_fallback_group_id: 52 },
      { ...sourceGroup, id: 50, name: 'Backup' },
      { ...sourceGroup, id: 51, name: 'Claude', platform: 'anthropic' as const },
      { ...sourceGroup, id: 52, name: 'Old backup' },
      { ...sourceGroup, id: 53, name: 'Monthly', subscription_type: 'subscription' as const }
    ]
    listGroups.mockResolvedValue({ items: groups, total: groups.length, page: 1, page_size: 20, pages: 1 })
    updateGroup.mockResolvedValue(groups[0])
    const wrapper = mount(GroupsView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub, TablePageLayout: TablePageLayoutStub, DataTable: DataTableStub, Pagination: true,
          BaseDialog: BaseDialogStub, ConfirmDialog: true, EmptyState: true, Select: SelectStub, PlatformIcon: true,
          Icon: true, GroupCapacityBadge: true, GroupRateMultipliersModal: true, GroupRPMOverridesModal: true, VueDraggable: true
        }
      }
    })
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()

    const field = wrapper.get('[data-testid="availability-fallback-field"]')
    expect(field.findAll('[data-option]').map(o => o.attributes('data-value'))).toEqual(['null', '50', '52'])
    await field.get('[data-value="50"]').trigger('click')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({ availability_fallback_group_id: 50 }))

    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    wrapper.get('[data-testid="availability-fallback-field"]').findComponent(SelectStub).vm.$emit('update:modelValue', null)
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenLastCalledWith(42, expect.objectContaining({ availability_fallback_group_id: 0 }))

    // Switching to a subscription group hides the field and clears it, so the
    // save is not rejected for a value the administrator can no longer see.
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    const vm = wrapper.vm as unknown as { editForm: { subscription_type: string; availability_fallback_group_id: number | null } }
    expect(vm.editForm.availability_fallback_group_id).toBe(52)
    vm.editForm.subscription_type = 'subscription'
    await flushPromises()
    expect(wrapper.find('[data-testid="availability-fallback-field"]').exists()).toBe(false)
    expect(vm.editForm.availability_fallback_group_id).toBeNull()
    wrapper.unmount()
  })

  it('loads, edits, and saves custom reasoning multipliers for group pricing', async () => {
    const group = {
      ...sourceGroup,
      model_pricing: [{
        platform: 'openai', models: ['example-model'], billing_mode: 'token',
        input_price: 3e-6, output_price: 15e-6, cache_write_price: null, cache_read_price: null,
        image_input_price: null, image_output_price: null, per_request_price: null,
        reasoning_effort_multipliers: { high: 1.5, max: 3 }, intervals: [], time_pricing: null,
      }],
    }
    listGroups.mockResolvedValue({ items: [group], total: 1, page: 1, page_size: 20, pages: 1 })
    updateGroup.mockResolvedValue(group)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()

    expect(wrapper.get<HTMLInputElement>('[data-reasoning-effort="high"]').element.value).toBe('1.5')
    await wrapper.get('[data-reasoning-effort="high"]').setValue('0.5')
    await wrapper.get('[data-reasoning-effort="max"]').setValue('')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({
      model_pricing: [expect.objectContaining({ reasoning_effort_multipliers: { high: 0.5 } })],
    }))
    wrapper.unmount()
  })

  it('blocks saving an invalid group reasoning multiplier and allows clearing it', async () => {
    const group = {
      ...sourceGroup,
      model_pricing: [{
        platform: 'openai', models: ['example-model'], billing_mode: 'token',
        input_price: 3e-6, output_price: 15e-6, cache_write_price: null, cache_read_price: null,
        image_input_price: null, image_output_price: null, per_request_price: null,
        reasoning_effort_multipliers: { high: 1.5 }, intervals: [], time_pricing: null,
      }],
    }
    listGroups.mockResolvedValue({ items: [group], total: 1, page: 1, page_size: 20, pages: 1 })
    updateGroup.mockResolvedValue(group)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()

    await wrapper.get('[data-reasoning-effort="high"]').setValue('0')
    await wrapper.get('#edit-group-form').trigger('submit')
    expect(updateGroup).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith(expect.stringContaining('reasoningEffortMultiplierPositive'))

    await wrapper.get('[data-testid="reasoning-effort-multipliers"] button').trigger('click')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({
      model_pricing: [expect.objectContaining({ reasoning_effort_multipliers: null })],
    }))
    wrapper.unmount()
  })

  it('leaves a bad rate floor to the page check: no native step or min constraint blocks the save first', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    const floor = wrapper.get('[data-testid="edit-min-upstream-rate"]')

    for (const [value, key] of [['1.12345', 'floorPrecisionError'], ['-1', 'floorRangeError']]) {
      showError.mockClear()
      await floor.setValue(value)
      // The browser would show its own bubble and never run the page's save for an input it finds invalid.
      expect((floor.element as HTMLInputElement).checkValidity()).toBe(true)
      expect(floor.attributes('aria-invalid')).toBe('true')
      await wrapper.get('#edit-group-form').trigger('submit')
      expect(showError).toHaveBeenCalledWith(`admin.groups.profitControl.${key}`)
    }
    expect(updateGroup).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // B1: what is saved and the preview that decides whether to confirm must be the same values, so the profit block
  // cannot change while a save is on its way.
  it('freezes the profit switch, margin and rate floor while the group is being saved', async () => {
    let finish!: (value: unknown) => void
    updateGroup.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
    // Floor 0.5 under the 0.8 ceiling (base 1, margin 20%) with both members inside: the save goes on to the PUT.
    vi.mocked(adminAPI.groups.getProfitControlPreview).mockResolvedValue({
      members: 2, schedulable: 2, passing: 2, ceiling: 0.8, floor: 0.5, below_floor: 0, pinned: 0, range_empty: false, pinned_loss: []
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    const floor = wrapper.get('[data-testid="edit-min-upstream-rate"]')
    const profitSwitch = floor.element.closest('.border-t')!.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    await wrapper.findAll('input[type="checkbox"]').find((input) => input.element === profitSwitch)!.setValue(true)
    await flushPromises()
    await floor.setValue('0.5')
    const disabled = (selector: string) => (wrapper.get(selector).element as HTMLInputElement).disabled
    expect([profitSwitch.disabled, disabled('#edit-profit-min-margin'), disabled('[data-testid="edit-min-upstream-rate"]')]).toEqual([false, false, false])
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect([profitSwitch.disabled, disabled('#edit-profit-min-margin'), disabled('[data-testid="edit-min-upstream-rate"]')]).toEqual([true, true, true])
    finish(sourceGroup)
    await flushPromises()
    wrapper.unmount()
  })

  // The "no usable account" confirmation judges exactly what is submitted (requestData), whatever the preview area or
  // the child component holds; a preview it cannot get for those values is never taken as "no confirmation needed".
  describe('confirming a save that may leave the group without usable accounts', () => {
    const preview = (patch: Record<string, unknown> = {}) =>
      ({ members: 2, schedulable: 2, passing: 2, ceiling: 0.8, floor: 0, below_floor: 0, pinned: 0, range_empty: false, pinned_loss: [], ...patch })
    const previewApi = () => vi.mocked(adminAPI.groups.getProfitControlPreview)
    const shownConfirm = (w: ReturnType<typeof mountView>) =>
      w.findAllComponents({ name: 'ConfirmDialog' }).find((dialog) => dialog.props('show') === true)
    async function openEdit(group: AdminGroup = sourceGroup) {
      listGroups.mockResolvedValue({ items: [group], total: 1, page: 1, page_size: 20, pages: 1 })
      const w = mountView()
      await flushPromises()
      await w.findAll('button').find((button) => button.text() === 'common.edit')!.trigger('click')
      await flushPromises()
      return w
    }
    beforeEach(() => {
      previewApi().mockReset().mockResolvedValue(preview())
      updateGroup.mockResolvedValue(sourceGroup)
    })

    it('judges the value submitted in the same turn as the change, and asks before saving it', async () => {
      previewApi().mockImplementation(async (_id: number, _margin: number, floor: number) =>
        preview(floor >= 2 ? { floor, below_floor: 2 } : { floor }))
      const w = await openEdit()
      const floor = w.get('[data-testid="edit-min-upstream-rate"]').element as HTMLInputElement
      floor.value = '0.1'
      floor.dispatchEvent(new Event('input'))
      await flushPromises()
      // Typed and submitted before anything re-renders: the payload and the check both read 2.
      floor.value = '2'
      floor.dispatchEvent(new Event('input'))
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(previewApi()).toHaveBeenLastCalledWith(42, 0, 2, expect.anything())
      expect(shownConfirm(w)!.props('title')).toBe('admin.groups.profitControl.floorNoneUsableConfirmTitle')
      expect(updateGroup).not.toHaveBeenCalled()
      shownConfirm(w)!.vm.$emit('confirm')
      await flushPromises()
      expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({ min_upstream_rate: 2 }))
      w.unmount()
    })

    it.each([
      ['failed', new Error('Network Error')],
      ['timed out', Object.assign(new Error('timeout of 30000ms exceeded'), { code: 'ECONNABORTED' })],
      ['cancelled', Object.assign(new Error('canceled'), { name: 'CanceledError', code: 'ERR_CANCELED' })]
    ])('asks with the general confirmation when the preview for the submitted values %s', async (_case, error) => {
      const w = await openEdit()
      previewApi().mockRejectedValue(error)
      await w.get('[data-testid="edit-min-upstream-rate"]').setValue('2')
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(shownConfirm(w)!.props('title')).toBe('admin.groups.profitControl.unverifiedConfirmTitle')
      shownConfirm(w)!.vm.$emit('cancel')
      await flushPromises()
      expect(updateGroup).not.toHaveBeenCalled()

      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      shownConfirm(w)!.vm.$emit('confirm')
      await flushPromises()
      expect(updateGroup).toHaveBeenCalledTimes(1)
      w.unmount()
    })

    it('asks with the multiplier note, without a preview, when the multiplier changes too', async () => {
      const w = await openEdit({ ...sourceGroup, profit_control_enabled: true, profit_min_margin: 0.2, profit_safety_buffer: 0 } as AdminGroup)
      await flushPromises()
      const shown = previewApi().mock.calls.length
      Object.assign((w.vm as unknown as { editForm: Record<string, unknown> }).editForm, { rate_multiplier: 0.5, min_upstream_rate: 0.6 })
      await flushPromises()
      expect(w.get('[data-testid="profit-control-preview-saved-rate"]').text()).toBe('admin.groups.profitControl.previewSavedRate')
      const asked = previewApi().mock.calls.length
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(shownConfirm(w)!.props('title')).toBe('admin.groups.profitControl.rateChangedConfirmTitle')
      expect(previewApi().mock.calls.length).toBe(asked)
      expect(asked).toBeGreaterThanOrEqual(shown)
      shownConfirm(w)!.vm.$emit('cancel')
      await flushPromises()
      expect(updateGroup).not.toHaveBeenCalled()
      w.unmount()
    })

    it('neither asks nor previews again when the switch, margin, floor and multiplier are unchanged', async () => {
      previewApi().mockResolvedValue(preview({ passing: 0 }))
      const w = await openEdit({ ...sourceGroup, profit_control_enabled: true, profit_min_margin: 0.2, profit_safety_buffer: 0 } as AdminGroup)
      await flushPromises()
      const shown = previewApi().mock.calls.length
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(shownConfirm(w)).toBeUndefined()
      expect(previewApi().mock.calls.length).toBe(shown)
      expect(updateGroup).toHaveBeenCalledTimes(1)
      w.unmount()
    })

    // A subscription group in its peak hours: what it charges is base × peak multiplier, and the preview uses the saved one.
    const peaked = {
      ...sourceGroup, subscription_type: 'subscription', peak_rate_enabled: true, peak_start: '00:00', peak_end: '23:59', peak_rate_multiplier: 1,
      profit_control_enabled: true, profit_min_margin: 0.2, profit_safety_buffer: 0, min_upstream_rate: 0.6
    } as AdminGroup
    const editForm = (w: ReturnType<typeof mountView>) => (w.vm as unknown as { editForm: Record<string, unknown> }).editForm

    it.each([
      ['the peak multiplier', { peak_rate_multiplier: 0.5 }],
      ['the peak hours', { peak_end: '12:00' }],
      ['the peak switch', { peak_rate_enabled: false }],
      ['the subscription type', { subscription_type: 'standard' }]
    ])('asks with the multiplier confirmation when only %s changes; cancel saves nothing, confirm saves once', async (_case, patch) => {
      const w = await openEdit(peaked)
      const shown = previewApi().mock.calls.length
      Object.assign(editForm(w), patch)
      await flushPromises()
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(shownConfirm(w)!.props('title')).toBe('admin.groups.profitControl.rateChangedConfirmTitle')
      shownConfirm(w)!.vm.$emit('cancel')
      await flushPromises()
      expect(updateGroup).not.toHaveBeenCalled()
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      shownConfirm(w)!.vm.$emit('confirm')
      await flushPromises()
      expect(updateGroup).toHaveBeenCalledTimes(1)
      expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining(patch))
      // No preview is asked to judge the save: it could only answer with the saved multiplier.
      expect(previewApi().mock.calls.length).toBe(shown)
      w.unmount()
    })

    it('neither asks nor previews for a change that does not touch the multiplier range', async () => {
      const w = await openEdit(peaked)
      const shown = previewApi().mock.calls.length
      Object.assign(editForm(w), { description: 'only the words change', rpm_limit: 30 })
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(shownConfirm(w)).toBeUndefined()
      expect(previewApi().mock.calls.length).toBe(shown)
      expect(updateGroup).toHaveBeenCalledTimes(1)
      w.unmount()
    })

    it('ignores a second save while the first one is still on its way, so it saves once', async () => {
      let finish!: (value: unknown) => void
      updateGroup.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
      const w = await openEdit()
      await w.get('[data-testid="edit-min-upstream-rate"]').setValue('0.5')
      const save = (w.vm as unknown as { handleUpdateGroup: () => Promise<void> }).handleUpdateGroup
      void save()
      await flushPromises()
      expect(updateGroup).toHaveBeenCalledTimes(1)
      // Still saving (the PUT has not answered): a second save would check and PUT again in parallel.
      void save()
      await flushPromises()
      expect(updateGroup).toHaveBeenCalledTimes(1)
      finish(sourceGroup)
      await flushPromises()
      w.unmount()
    })

    it('does not save when the page is left right after the confirmation, nor while it is open', async () => {
      const w = await openEdit()
      previewApi().mockRejectedValue(new Error('Network Error'))
      await w.get('[data-testid="edit-min-upstream-rate"]').setValue('2')
      await w.get('#edit-group-form').trigger('submit')
      await flushPromises()
      shownConfirm(w)!.vm.$emit('confirm')
      w.unmount()
      await flushPromises()
      expect(updateGroup).not.toHaveBeenCalled()

      // Left while the confirmation is open: it ends as a cancel and the save finishes without a PUT.
      const open = await openEdit()
      await open.get('[data-testid="edit-min-upstream-rate"]').setValue('2')
      const saving = (open.vm as unknown as { handleUpdateGroup: () => Promise<void> }).handleUpdateGroup()
      await flushPromises()
      expect(shownConfirm(open)).toBeDefined()
      open.unmount()
      const ended = await Promise.race([saving.then(() => 'ended'), new Promise((resolve) => setTimeout(() => resolve('still waiting'), 50))])
      expect(ended).toBe('ended')
      expect(updateGroup).not.toHaveBeenCalled()
    })

    it('freezes the profit switch, margin and floor while a new group is being created', async () => {
      let finish!: (value: unknown) => void
      vi.mocked(adminAPI.groups.create).mockReturnValueOnce(new Promise((resolve) => { finish = resolve }) as never)
      const w = mountView()
      await flushPromises()
      const vm = w.vm as unknown as { showCreateModal: boolean; createForm: Record<string, unknown> }
      vm.showCreateModal = true
      Object.assign(vm.createForm, { name: 'new-group', platform: 'openai', profit_control_enabled: true, profit_min_margin_percent: 20, min_upstream_rate: 2 })
      await flushPromises()
      const inputs = () => [
        w.get('#create-profit-min-margin').element.closest('.border-t')!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.disabled,
        (w.get('#create-profit-min-margin').element as HTMLInputElement).disabled,
        (w.get('#create-min-upstream-rate').element as HTMLInputElement).disabled
      ]
      expect(inputs()).toEqual([false, false, false])
      await w.get('#create-group-form').trigger('submit')
      await flushPromises()
      expect(adminAPI.groups.create).toHaveBeenCalledTimes(1)
      expect(inputs()).toEqual([true, true, true])
      finish({ ...sourceGroup, id: 99 })
      await flushPromises()
      w.unmount()
    })
  })

  it('updates manifest controls immediately and submits the displayed selection', async () => {
    vi.useFakeTimers()
    vi.mocked(adminAPI.accounts.list).mockResolvedValue({
      items: [{ id: 5, name: 'Manifest account' }]
    } as never)
    updateGroup.mockResolvedValue(sourceGroup)
    const wrapper = mountView()
    try {
      await flushPromises()
      const editButton = wrapper.findAll('button').find((button) => button.text() === 'common.edit')!
      await editButton.trigger('click')
      await flushPromises()

      const toggle = wrapper.get('[data-testid="codex-manifest-toggle"]')
      await toggle.trigger('click')
      expect(toggle.attributes('aria-checked')).toBe('true')
      const search = wrapper.get('[data-testid="codex-manifest-search"]')
      await search.trigger('focus')
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()
      expect(adminAPI.accounts.list).toHaveBeenCalledWith(
        1, 20, { search: '', platform: 'openai', group: '42' }, expect.anything()
      )
      await wrapper.get('[data-testid="codex-manifest-dropdown"] button').trigger('click')
      expect(wrapper.get('[data-testid="codex-manifest-selected-tags"]').text()).toContain('Manifest account')

      await wrapper.get('[aria-label="remove account 5"]').trigger('click')
      expect(wrapper.find('[data-testid="codex-manifest-selected-tags"]').exists()).toBe(false)
      await wrapper.get('#edit-group-form').trigger('submit')
      expect(updateGroup).not.toHaveBeenCalled()
      expect(wrapper.find('[data-testid="codex-manifest-validation-error"]').exists()).toBe(true)

      await search.trigger('focus')
      await wrapper.get('[data-testid="codex-manifest-dropdown"] button').trigger('click')
      expect(wrapper.get('[data-testid="codex-manifest-selected-tags"]').text()).toContain('Manifest account')
      const fallback = wrapper.get('[data-testid="codex-manifest-fallback-toggle"]')
      await fallback.trigger('click')
      expect(fallback.attributes('aria-checked')).toBe('true')
      await fallback.trigger('click')
      expect(fallback.attributes('aria-checked')).toBe('false')
      await fallback.trigger('click')

      await toggle.trigger('click')
      expect(wrapper.find('[data-testid="codex-manifest-search"]').exists()).toBe(false)
      await toggle.trigger('click')
      expect(wrapper.get('[data-testid="codex-manifest-selected-tags"]').text()).toContain('Manifest account')
      await wrapper.get('#edit-group-form').trigger('submit')
      await flushPromises()
      expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({
        codex_models_manifest_config: {
          enabled: true, account_ids: [5], fallback_to_scheduler: true
        }
      }))

      // Reopening reads the saved group afresh, without retaining the prior draft.
      await editButton.trigger('click')
      await flushPromises()
      expect(wrapper.get('[data-testid="codex-manifest-toggle"]').attributes('aria-checked')).toBe('false')
      expect(wrapper.find('[data-testid="codex-manifest-search"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
      vi.useRealTimers()
    }
  })

})
