import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import SchedEvidence from '../SchedEvidence.vue'
import { errorEvidence, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'

// Browser check #8 with the real Chinese messages: the unit follows a known total only ("全部 30 条"); an unknown side
// is "未知" with no unit, and a zero is a plain zero with one unit (never "全部 未知 条" or "全部 0 个 个").
function samplesText(dim: 'ttfo' | 'error', samples: { samples_local: number | null; samples_total: number | null }) {
  const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh } as never })
  const evidence = dim === 'ttfo' ? ttfoEvidence() : errorEvidence()
  const wrapper = mount(SchedEvidence, { props: { dim, evidence, samples }, global: { plugins: [i18n] } })
  const text = wrapper.get('[data-qp-evidence-samples]').text()
  wrapper.unmount()
  return text
}

describe('SchedEvidence sample counts (real Chinese messages)', () => {
  it.each([
    ['ttfo', { samples_local: null, samples_total: null }, '样本 本组 未知 / 全部 未知'],
    ['ttfo', { samples_local: 11, samples_total: 30 }, '样本 本组 11 / 全部 30 条'],
    ['ttfo', { samples_local: 0, samples_total: 0 }, '样本 本组 0 / 全部 0 条'],
    ['error', { samples_local: 0, samples_total: 12 }, '明确结果 本组 0 / 全部 12 个'],
    ['error', { samples_local: 3, samples_total: null }, '明确结果 本组 3 / 全部 未知']
  ] as const)('%s %j reads %s', (dim, samples, expected) => {
    expect(samplesText(dim, samples)).toBe(expected)
  })
})
