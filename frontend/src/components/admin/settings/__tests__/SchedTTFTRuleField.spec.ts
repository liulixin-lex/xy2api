import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import type { BackendTTFTRule } from '@/api/admin/schedQualityTypes'
import SchedTTFTRuleField from '../SchedTTFTRuleField.vue'
import { ttftRuleChange, ttftRuleInputValid } from '../schedTTFTRule'

const api = vi.hoisted(() => ({ getTTFTRule: vi.fn(), updateTTFTRule: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const rule = (patch: Partial<BackendTTFTRule> = {}): BackendTTFTRule => ({
  version: 4,
  confidence: 0.8,
  tolerance: 0.2,
  peer_margin: 0.05,
  defaults: { confidence: 0.8, tolerance: 0.2, peer_margin: 0.05 },
  replayed: { confidence: [0.8, 0.95], tolerance: [0.2, 0.25], peer_margin: [0.05, 0.15] },
  ...patch
})

// The page's confirm dialog, reduced to its two buttons.
const ConfirmStub = defineComponent({
  props: { show: Boolean, title: String, message: String },
  emits: ['confirm', 'cancel'],
  setup: (props, { emit }) => () => props.show
    ? h('div', { 'data-test': 'confirm' }, [
        h('p', props.message),
        h('button', { 'data-test': 'ok', onClick: () => emit('confirm') }),
        h('button', { 'data-test': 'no', onClick: () => emit('cancel') })
      ])
    : null
})

type Field = { save: () => Promise<boolean>; valid: boolean }
async function mountField() {
  const w = mount(SchedTTFTRuleField, { global: { stubs: { ConfirmDialog: ConfirmStub } } })
  await flushPromises()
  return w
}
const input = (w: ReturnType<typeof mount>, field: string) => w.get(`[data-sq-ttft-input="${field}"]`)
const row = (w: ReturnType<typeof mount>, field: string) => w.get(`[data-sq-ttft-field="${field}"]`)
const vm = (w: ReturnType<typeof mount>) => w.vm as unknown as Field

beforeEach(() => {
  api.getTTFTRule.mockReset().mockResolvedValue(rule())
  api.updateTTFTRule.mockReset().mockImplementation(async (update) => ({ ...rule(update), version: 5, change: 'tighten', cleared: 0 }))
})

describe('ttft rule helpers', () => {
  it('accepts only 50 < confidence < 100, 0 < tolerance < 100 and 0 ≤ margin < 100 (percent), never empty or non-numbers', () => {
    expect(['50', '50.01', '99.99', '100', ''].map((v) => ttftRuleInputValid('confidence', v))).toEqual([false, true, true, false, false])
    expect(['0', '0.1', '99.9', '100', 'x'].map((v) => ttftRuleInputValid('tolerance', v))).toEqual([false, true, true, false, false])
    expect(['-1', '0', '99', '100', ' '].map((v) => ttftRuleInputValid('peer_margin', v))).toEqual([false, true, true, false, false])
  })

  it('calls any value up a relaxation, any down a tightening, both mixed', () => {
    const saved = { confidence: 0.8, tolerance: 0.2, peer_margin: 0.05 }
    expect(ttftRuleChange(saved, { ...saved })).toBe('none')
    expect(ttftRuleChange(saved, { ...saved, peer_margin: 0.1 })).toBe('relax')
    expect(ttftRuleChange(saved, { ...saved, confidence: 0.9, tolerance: 0.25 })).toBe('relax')
    expect(ttftRuleChange(saved, { ...saved, tolerance: 0.15 })).toBe('tighten')
    expect(ttftRuleChange(saved, { ...saved, confidence: 0.9, tolerance: 0.15 })).toBe('mixed')
  })
})

describe('SchedTTFTRuleField', () => {
  it('shows the three values as percent and points, the defaults, and saves nothing when untouched', async () => {
    const w = await mountField()
    expect(['confidence', 'tolerance', 'peer_margin'].map((field) => (input(w, field).element as HTMLInputElement).value)).toEqual(['80', '20', '5'])
    expect(row(w, 'peer_margin').text()).toContain('admin.schedQuality.ttftRule.unit.points')
    expect(row(w, 'confidence').text()).toContain('admin.schedQuality.ttftRule.unit.percent')
    expect(w.get('[data-sq-ttft-defaults]').text()).toBe('admin.schedQuality.ttftRule.defaults:{"confidence":"80","tolerance":"20","peer_margin":"5"}')
    expect(await vm(w).save()).toBe(true)
    expect(api.updateTTFTRule).not.toHaveBeenCalled()
  })

  it('frames an illegal value red with its range and saves nothing; a legal value outside the replayed range gets a note', async () => {
    const w = await mountField()
    await input(w, 'confidence').setValue('50')
    expect(input(w, 'confidence').attributes('aria-invalid')).toBe('true')
    expect(row(w, 'confidence').get('[data-sq-ttft-invalid]').text()).toBe('admin.schedQuality.ttftRule.legal.confidence')
    expect(vm(w).valid).toBe(false)
    expect(await vm(w).save()).toBe(false)
    expect(api.updateTTFTRule).not.toHaveBeenCalled()

    await input(w, 'confidence').setValue('97')
    expect(vm(w).valid).toBe(true)
    expect(row(w, 'confidence').find('[data-sq-ttft-invalid]').exists()).toBe(false)
    expect(row(w, 'confidence').get('[data-sq-ttft-outside]').text()).toBe('admin.schedQuality.ttftRule.outside:{"range":"admin.schedQuality.ttftRule.range.percent:{\\"min\\":\\"80\\",\\"max\\":\\"95\\"}"}')
    await input(w, 'confidence').setValue('95')
    expect(row(w, 'confidence').find('[data-sq-ttft-outside]').exists()).toBe(false)
    await input(w, 'peer_margin').setValue('0')
    expect(vm(w).valid).toBe(true)
    expect(row(w, 'peer_margin').get('[data-sq-ttft-outside]').text()).toContain('range.points')
  })

  it('never holds up the settings page when the rule did not load: valid, nothing to save, a retry', async () => {
    api.getTTFTRule.mockRejectedValueOnce(new Error('down'))
    const w = await mountField()
    expect(w.find('[data-sq-ttft-input="confidence"]').exists()).toBe(false)
    expect(vm(w).valid).toBe(true)
    expect(await vm(w).save()).toBe(true)
    expect(api.updateTTFTRule).not.toHaveBeenCalled()
    await w.get('button').trigger('click')
    await flushPromises()
    expect((input(w, 'confidence').element as HTMLInputElement).value).toBe('80')
  })

  it('saves a pure tightening at once, as ratios with the version', async () => {
    const w = await mountField()
    await input(w, 'tolerance').setValue('15')
    expect(await vm(w).save()).toBe(true)
    expect(w.find('[data-test="confirm"]').exists()).toBe(false)
    expect(api.updateTTFTRule).toHaveBeenCalledWith({ expected_version: 4, confidence: 0.8, tolerance: 0.15, peer_margin: 0.05 })
  })

  for (const [kind, edit] of [['relaxation', { confidence: '90' }], ['mixed change', { confidence: '90', tolerance: '15' }]] as const) {
    it(`asks before a ${kind}: declining saves nothing, confirming saves`, async () => {
      const w = await mountField()
      for (const [field, value] of Object.entries(edit)) await input(w, field).setValue(value)

      const declined = vm(w).save()
      await flushPromises()
      expect(w.get('[data-test="confirm"]').text()).toBe('admin.schedQuality.ttftRule.confirmMessage')
      await w.get('[data-test="no"]').trigger('click')
      expect(await declined).toBe(false)
      expect(api.updateTTFTRule).not.toHaveBeenCalled()

      const confirmed = vm(w).save()
      await flushPromises()
      await w.get('[data-test="ok"]').trigger('click')
      expect(await confirmed).toBe(true)
      expect(api.updateTTFTRule).toHaveBeenCalledTimes(1)
      expect(api.updateTTFTRule.mock.calls[0][0]).toMatchObject({ expected_version: 4, confidence: 0.9 })
      expect(w.find('[data-test="confirm"]').exists()).toBe(false)
    })
  }

  it('shows 失败 + 重新载入 on a version conflict and the backend\'s message on a rejected value', async () => {
    const w = await mountField()
    await input(w, 'tolerance').setValue('15')
    api.updateTTFTRule.mockRejectedValueOnce({ status: 409 })
    expect(await vm(w).save()).toBe(false)
    expect(w.find('[data-sq-ttft-reload]').exists()).toBe(true)

    api.getTTFTRule.mockResolvedValueOnce(rule({ version: 6, tolerance: 0.22 }))
    await w.get('[data-sq-ttft-reload]').trigger('click')
    await flushPromises()
    expect(w.find('[data-sq-ttft-failed]').exists()).toBe(false)
    expect((input(w, 'tolerance').element as HTMLInputElement).value).toBe('22')

    await input(w, 'tolerance').setValue('15')
    api.updateTTFTRule.mockRejectedValueOnce({ status: 400, message: 'tolerance must be below 1' })
    expect(await vm(w).save()).toBe(false)
    expect(w.get('[data-sq-ttft-failed]').text()).toContain('tolerance must be below 1')
    expect(w.find('[data-sq-ttft-reload]').exists()).toBe(false)
  })
})
