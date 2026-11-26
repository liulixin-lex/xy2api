import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import type { BackendTTFTRule } from '@/api/admin/schedQualityTypes'
import SchedTTFTRuleField from '../SchedTTFTRuleField.vue'
import { ttftRuleChange, ttftRuleInputValid } from '../schedTTFTRule'

const api = vi.hoisted(() => ({ getTTFTRule: vi.fn(), updateTTFTRule: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const toast = vi.hoisted(() => ({ showInfo: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
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

type Field = { save: () => Promise<boolean>; valid: boolean; unsaved: boolean }
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
  toast.showInfo.mockReset()
})

describe('ttft rule helpers', () => {
  it('accepts only 50 < confidence < 100 and 0 < tolerance < 100 (percent), never empty or non-numbers', () => {
    expect(['50', '50.01', '99.99', '100', ''].map((v) => ttftRuleInputValid('confidence', v))).toEqual([false, true, true, false, false])
    expect(['0', '0.1', '99.9', '100', 'x'].map((v) => ttftRuleInputValid('tolerance', v))).toEqual([false, true, true, false, false])
  })

  it('calls any value up a relaxation, any down a tightening, both mixed', () => {
    const saved = { confidence: 0.8, tolerance: 0.2, peer_margin: 0.05 }
    expect(ttftRuleChange(saved, { ...saved })).toBe('none')
    expect(ttftRuleChange(saved, { ...saved, confidence: 0.9 })).toBe('relax')
    expect(ttftRuleChange(saved, { ...saved, confidence: 0.9, tolerance: 0.25 })).toBe('relax')
    expect(ttftRuleChange(saved, { ...saved, tolerance: 0.15 })).toBe('tighten')
    expect(ttftRuleChange(saved, { ...saved, confidence: 0.9, tolerance: 0.15 })).toBe('mixed')
  })
})

describe('SchedTTFTRuleField', () => {
  // Plan item 18: the rules in force do not compare with peers, so the margin over them is not shown.
  it('shows confidence and tolerance as percent and their defaults, no margin over the peers, and saves nothing when untouched', async () => {
    const w = await mountField()
    expect(w.findAll('[data-sq-ttft-field]').map((el) => el.attributes('data-sq-ttft-field'))).toEqual(['confidence', 'tolerance'])
    expect(['confidence', 'tolerance'].map((field) => (input(w, field).element as HTMLInputElement).value)).toEqual(['80', '20'])
    expect(row(w, 'tolerance').text()).toContain('admin.schedQuality.ttftRule.unit.percent')
    expect(w.text()).not.toContain('peer_margin')
    expect(w.get('[data-sq-ttft-defaults]').text()).toBe('admin.schedQuality.ttftRule.defaults:{"confidence":"80","tolerance":"20"}')
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
  })

  // Rule 4.0 was replayed on its defaults only: the API gives each range as one value. The value itself gets no note;
  // any other names that one value, never a span like "20%–20%".
  it('names the one replayed value when the range is a single value, and notes nothing at it', async () => {
    api.getTTFTRule.mockResolvedValueOnce(rule({ replayed: { confidence: [0.8, 0.8], tolerance: [0.2, 0.2], peer_margin: [0.05, 0.15] } }))
    const w = await mountField()
    expect(w.find('[data-sq-ttft-outside]').exists()).toBe(false)
    await input(w, 'tolerance').setValue('25')
    expect(row(w, 'tolerance').get('[data-sq-ttft-outside]').text()).toBe('admin.schedQuality.ttftRule.outsidePoint:{"value":"20"}')
    await input(w, 'confidence').setValue('90')
    expect(row(w, 'confidence').get('[data-sq-ttft-outside]').text()).toBe('admin.schedQuality.ttftRule.outsidePoint:{"value":"80"}')
    await input(w, 'tolerance').setValue('20')
    expect(row(w, 'tolerance').find('[data-sq-ttft-outside]').exists()).toBe(false)
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

  it('saves a pure tightening at once, as ratios with the version, and the margin over the peers as loaded', async () => {
    api.getTTFTRule.mockResolvedValueOnce(rule({ peer_margin: 0.07 }))
    const w = await mountField()
    await input(w, 'tolerance').setValue('15')
    expect(await vm(w).save()).toBe(true)
    expect(w.find('[data-test="confirm"]').exists()).toBe(false)
    expect(api.updateTTFTRule).toHaveBeenCalledWith({ expected_version: 4, confidence: 0.8, tolerance: 0.15, peer_margin: 0.07 })
  })

  // Astra review of fe-min, suggestion 3: an answer without the margin never makes the save write 0 (the previous rules
  // read it after a rollback): it sends the answer's default, and with no default either it leaves the key out.
  it('sends the answer\'s default margin when the loaded rule lacks it, and no margin when the default is missing too', async () => {
    const { peer_margin: _margin, ...withoutMargin } = rule({ defaults: { confidence: 0.8, tolerance: 0.2, peer_margin: 0.06 } })
    api.getTTFTRule.mockResolvedValueOnce(withoutMargin)
    const w = await mountField()
    await input(w, 'tolerance').setValue('15')
    expect(await vm(w).save()).toBe(true)
    expect(api.updateTTFTRule).toHaveBeenLastCalledWith({ expected_version: 4, confidence: 0.8, tolerance: 0.15, peer_margin: 0.06 })

    const { defaults: _defaults, ...bare } = withoutMargin
    api.getTTFTRule.mockResolvedValueOnce(bare)
    const v = await mountField()
    await input(v, 'tolerance').setValue('15')
    await vm(v).save()
    expect(api.updateTTFTRule.mock.lastCall![0]).toEqual({ expected_version: 4, confidence: 0.8, tolerance: 0.15, peer_margin: undefined })
    expect(JSON.parse(JSON.stringify(api.updateTTFTRule.mock.lastCall![0]))).not.toHaveProperty('peer_margin')
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

  it('marks the values a declined relaxation left unsaved, until they are put back or saved', async () => {
    const w = await mountField()
    const decline = async () => {
      const saving = vm(w).save()
      await flushPromises()
      await w.get('[data-test="no"]').trigger('click')
      expect(await saving).toBe(false)
      await flushPromises()
    }
    await input(w, 'confidence').setValue('90')
    await decline()
    expect(vm(w).unsaved).toBe(true)
    expect(w.get('[data-sq-ttft-unsaved]').text()).toBe('admin.schedQuality.ttftRule.unsaved')
    expect(input(w, 'confidence').attributes('data-sq-ttft-unsaved-input')).toBeDefined()
    expect(input(w, 'confidence').classes()).toContain('border-amber-500')
    expect(input(w, 'tolerance').attributes('data-sq-ttft-unsaved-input')).toBeUndefined()

    // Put back to the saved value: nothing left unsaved.
    await input(w, 'confidence').setValue('80')
    expect(vm(w).unsaved).toBe(false)
    expect(w.find('[data-sq-ttft-unsaved]').exists()).toBe(false)

    // Declined again, then confirmed: saved, the mark goes.
    await input(w, 'confidence').setValue('90')
    await decline()
    expect(w.find('[data-sq-ttft-unsaved]').exists()).toBe(true)
    const confirmed = vm(w).save()
    await flushPromises()
    await w.get('[data-test="ok"]').trigger('click')
    expect(await confirmed).toBe(true)
    await flushPromises()
    expect(w.find('[data-sq-ttft-unsaved]').exists()).toBe(false)
    expect(input(w, 'confidence').attributes('data-sq-ttft-unsaved-input')).toBeUndefined()
  })

  // I2: the answer is filled back in, so nothing can be typed while the PUT is on its way.
  it('locks the inputs while a save is on its way and opens them again when it ends', async () => {
    let answer!: (value: unknown) => void
    api.updateTTFTRule.mockReturnValueOnce(new Promise((resolve) => { answer = resolve }))
    const w = await mountField()
    await input(w, 'tolerance').setValue('15')
    const saving = vm(w).save()
    await flushPromises()
    expect(api.updateTTFTRule).toHaveBeenCalledWith({ expected_version: 4, confidence: 0.8, tolerance: 0.15, peer_margin: 0.05 })
    expect(['confidence', 'tolerance'].map((field) => (input(w, field).element as HTMLInputElement).disabled)).toEqual([true, true])
    answer({ ...rule({ tolerance: 0.15 }), version: 5, change: 'tighten', cleared: 0 })
    expect(await saving).toBe(true)
    await flushPromises()
    expect((input(w, 'tolerance').element as HTMLInputElement).disabled).toBe(false)
    expect((input(w, 'tolerance').element as HTMLInputElement).value).toBe('15')
  })

  it('keeps the loaded defaults and replayed ranges when the save answer leaves them out, and says what a relaxation cleared', async () => {
    const w = await mountField()
    await input(w, 'confidence').setValue('90')
    api.updateTTFTRule.mockResolvedValueOnce({ version: 5, confidence: 0.9, tolerance: 0.2, peer_margin: 0.1, change: 'relax', cleared: 12 })
    const saving = vm(w).save()
    await flushPromises()
    await w.get('[data-test="ok"]').trigger('click')
    expect(await saving).toBe(true)
    await flushPromises()
    expect((input(w, 'confidence').element as HTMLInputElement).value).toBe('90')
    expect(w.get('[data-sq-ttft-defaults]').text()).toContain('"confidence":"80"')
    expect(row(w, 'confidence').find('[data-sq-ttft-outside]').exists()).toBe(false)
    expect(toast.showInfo).toHaveBeenCalledWith('admin.schedQuality.ttftRule.savedRelaxed:{"n":12}', 6000)

    // The next save goes with the new version and the margin the answer carried; a tightening says nothing more.
    await input(w, 'tolerance').setValue('15')
    expect(await vm(w).save()).toBe(true)
    expect(api.updateTTFTRule).toHaveBeenLastCalledWith({ expected_version: 5, confidence: 0.9, tolerance: 0.15, peer_margin: 0.1 })
    expect(toast.showInfo).toHaveBeenCalledTimes(1)
  })

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
