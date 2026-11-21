import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import SchedEvidence from '../SchedEvidence.vue'
import { basis, cacheEvidence, errorEvidence, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import type { BackendBasis } from '@/api/admin/schedQualityTypes'

// With the real Chinese messages: a decision's evidence is two short lines of label and figure, no sentences.
function text(record: BackendBasis) {
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh } as never })
  const wrapper = mount(SchedEvidence, { props: { basis: record, tone: 'rear' }, global: { plugins: [i18n] } })
  const lines = wrapper.findAll('.ev__row').map((row) => row.findAll('dt, dd').map((cell) => cell.text()).join(' '))
  wrapper.unmount()
  return lines
}

describe('SchedEvidence figures (real Chinese messages)', () => {
  it.each([
    ['ttfo against the bar', basis('ttfo', { ttft: ttfoEvidence({ limit_s: 8, n: 40, k: 9, z: 0.103 }), samples_local: 11, samples_total: 30 }), ['超 8 秒 22.5% 判差线 10.3%', '样本 30 本组 11']],
    ['ttfo without a bar', basis('ttfo', { ttft: ttfoEvidence({ n: 40, k: 9, z: null, prior_mu: 0.146 }), samples_local: 0, samples_total: 30 }), ['超 10 秒 22.5% 同组 14.6%', '样本 30 本组 0']],
    ['errors', basis('error', { error: errorEvidence({ failed: 6, padded: 80, limit: 0.05 }), samples_local: null, samples_total: 80 }), ['报错 7.5% 上限 5%', '样本 80']],
    ['cache', basis('cache', { cache: cacheEvidence({ ratio: 0.781, min_ratio: 0.85 }), samples_local: 3, samples_total: null }), ['命中 78.1% 下限 85%', '本组 3']]
  ] as const)('%s', (_, record, expected) => {
    expect(text(record)).toEqual(expected)
  })
})
