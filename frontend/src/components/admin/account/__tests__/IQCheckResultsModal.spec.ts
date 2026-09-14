import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import IQCheckResultsModal from '../IQCheckResultsModal.vue'
import type { Account } from '@/types'
const history = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getIQCheckResults: history } } }))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (s: string) => s, te: () => true }) }))
describe('IQ results', () => {
  it('shows the same extracted answer for JSON and prose while preserving folded originals', async () => {
    history.mockResolvedValue([
      { id: 2, normalized_answer: '21', answer: '{"answer":21}', model: 'custom', effort: 'ultra', protocol: 'responses', output_mode: 'strict', status: 'smart', finished_at: 'now', started_at: 'now', latency_ms: 12, grader_version: 'candy-grader-v2', format_compliant: true },
      { id: 1, normalized_answer: '21', answer: '最终答案是21。解释如下。', model: 'custom', effort: 'upstream_default', status: 'smart', finished_at: 'before', started_at: 'before', latency_ms: 11, grader_version: 'candy-grader-v2', format_compliant: false }
    ])
    const wrapper = mount(IQCheckResultsModal, { props: { show: true, account: { id: 8, name: 'fixture' } as Account }, global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } } })
    await flushPromises()
    expect(wrapper.findAll('p.font-semibold').map(p => p.text())).toEqual(['21', '21'])
    expect(wrapper.findAll('details')).toHaveLength(2)
    expect(wrapper.findAll('details').every(d => d.attributes('open') === undefined)).toBe(true)
    expect(wrapper.text()).toContain('iqFormatMismatch')
    expect(wrapper.findAll('pre').map(p => p.text())).toEqual(['{"answer":21}', '最终答案是21。解释如下。'])
  })
  it('distinguishes unfinished and legacy attempts', async () => {
    history.mockResolvedValue([{ id: 1, model: 'legacy', effort: 'low', answer: '29', status: 'unknown', finished_at: null, started_at: 'now', latency_ms: 0 }])
    const wrapper = mount(IQCheckResultsModal, { props: { show: true, account: { id: 8 } as Account }, global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } } })
    await flushPromises()
    expect(wrapper.text()).toContain('iqRunning')
    expect(wrapper.text()).toContain('iqLegacyResult')
    expect(wrapper.get('pre').text()).toBe('29')
  })
})
