import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import ShareFactorMarks from '../ShareFactorMarks.vue'
import { shareMember } from '@/stores/__tests__/shareSchedulingFixtures'

vi.mock('vue-i18n', async importOriginal => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params && Object.keys(params).length ? `${key}:${JSON.stringify(params)}` : key),
    te: () => true
  })
}))

const now = Date.parse('2026-10-20T12:00:00Z')
const downweighted = shareMember(5, {
  health_factor: 0.2,
  effective_weight: 2,
  health: { window_minutes: 30, attempts: 456, soft_failures: 92, rate: 0.2, tier_baseline_rate: 0, insufficient: false }
})

function mountMarks(member = downweighted) {
  return mount(ShareFactorMarks, { props: { member, now }, attachTo: document.body })
}

describe('ShareFactorMarks accessibility', () => {
  it('makes each mark a button that keyboards reach', () => {
    const w = mountMarks()
    const mark = w.get('[data-testid="share-factor-health-5"]')
    expect(mark.element.tagName).toBe('BUTTON')
    expect(mark.attributes('type')).toBe('button')
    w.unmount()
  })

  it('describes the breakdown to screen readers without hovering', () => {
    const w = mountMarks()
    const mark = w.get('[data-testid="share-factor-health-5"]')
    const describedBy = mark.attributes('aria-describedby')
    expect(describedBy).toBeTruthy()
    const description = document.getElementById(describedBy!)
    expect(description?.textContent).toContain('admin.shareScheduling.traffic.factors.breakdown')
    expect(description?.textContent).toContain('admin.shareScheduling.traffic.factors.healthReason')
    w.unmount()
  })

  it('expands the breakdown in place when the mark is activated', async () => {
    const w = mountMarks()
    const mark = w.get('[data-testid="share-factor-health-5"]')
    expect(mark.attributes('aria-expanded')).toBe('false')
    expect(w.find('[data-testid="share-factor-expanded-5"]').exists()).toBe(false)
    await mark.trigger('click')
    expect(mark.attributes('aria-expanded')).toBe('true')
    const panel = w.get('[data-testid="share-factor-expanded-5"]')
    expect(mark.attributes('aria-controls')).toBe(panel.attributes('id'))
    expect(panel.text()).toContain('"weight":"2"')
    await mark.trigger('click')
    expect(w.find('[data-testid="share-factor-expanded-5"]').exists()).toBe(false)
    w.unmount()
  })

  it('gives every instance on a page its own ids', () => {
    // The traffic table and the switch confirmation can show the same account at once.
    const Page = defineComponent({
      components: { ShareFactorMarks },
      setup: () => ({ member: downweighted, now }),
      template: '<div><ShareFactorMarks :member="member" :now="now" /><ShareFactorMarks :member="member" :now="now" /></div>'
    })
    const w = mount(Page, { attachTo: document.body })
    const ids = w.findAll('[data-testid="share-factor-health-5"]').map(mark => mark.attributes('aria-describedby'))
    expect(ids).toHaveLength(2)
    expect(ids[0]).not.toBe(ids[1])
    w.unmount()
  })
})
