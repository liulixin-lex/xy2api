import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import SchedEvidence from '../SchedEvidence.vue'
import { basis, cacheEvidence, errorEvidence, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import type { BackendBasis } from '@/api/admin/schedQualityTypes'

// With the real Chinese messages: a decision's evidence is when it was taken and what it read against its line, then
// the units counted; no labels on the reading, no sentences.
function text(record: BackendBasis) {
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh } as never })
  const wrapper = mount(SchedEvidence, { props: { basis: record, tone: 'rear' }, global: { plugins: [i18n] } })
  const lines = wrapper.findAll('.ev__row').map((row) => row.findAll('dt, dd').map((cell) => cell.text()).join(' '))
  wrapper.unmount()
  return lines
}

const local = (h: number, m: number) => new Date(2026, 10, 25, h, m).toISOString()

describe('SchedEvidence figures (real Chinese messages)', () => {
  it.each([
    ['ttfo with its tolerance', basis('ttfo', { ttft: ttfoEvidence({ limit_s: 8, n: 40, k: 9, tolerance: 0.2 }), samples_local: 11, samples_total: 30 }), ['22.5 / 20', '样本 30 本组 11']],
    ['ttfo from before the tolerance was kept', basis('ttfo', { ttft: ttfoEvidence({ n: 40, k: 9, z: null, prior_mu: 0.146 }), samples_local: 0, samples_total: 30 }), ['22.5 / —', '样本 30 本组 0']],
    ['errors', basis('error', { error: errorEvidence({ failed: 6, padded: 80, limit: 0.05 }), samples_local: null, samples_total: 80 }), ['7.5 / 5', '样本 80']],
    ['cache with its period', basis('cache', { cut: local(13, 5), cache: cacheEvidence({ ratio: 0.781, min_ratio: 0.85, window_from: local(12, 45), window_to: local(13, 15) }), samples_local: 3, samples_total: null }), ['13:05–13:15 78.1 / 85', '本组 3']]
  ] as const)('%s', (_, record, expected) => {
    expect(text(record)).toEqual(expected)
  })
})
