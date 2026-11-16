import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { GroupProfitControlPreview as Preview } from '@/types/groupProfitControl'
import GroupProfitControlPreview from '../GroupProfitControlPreview.vue'

const api = vi.hoisted(() => ({ getProfitControlPreview: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: api } }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const preview = (over: Partial<Preview> = {}): Preview => ({
  members: 8,
  schedulable: 6,
  passing: 3,
  ceiling: 0.8,
  floor: 0,
  below_floor: 0,
  pinned: 0,
  range_empty: false,
  pinned_loss: [],
  ...over
})

type Props = { marginPercent?: number | string | null; profitEnabled?: boolean; floor?: number | string | null }
const mountPreview = (props: Props = {}) =>
  mount(GroupProfitControlPreview, { props: { groupId: 53, marginPercent: 20, profitEnabled: true, floor: '', ...props } })
const line = (w: ReturnType<typeof mount>) => w.get('[data-testid="profit-control-preview-line"]')
const blockingReason = (w: ReturnType<typeof mount>) => (w.vm as unknown as { blockingReason: () => Promise<string | null> }).blockingReason()

beforeEach(() => {
  api.getProfitControlPreview.mockReset().mockResolvedValue(preview())
})
afterEach(() => { vi.useRealTimers() })

describe('GroupProfitControlPreview', () => {
  it('asks with the unsaved floor (0 for an empty box) and asks again after the typing pause', async () => {
    vi.useFakeTimers()
    const w = mountPreview()
    await flushPromises()
    expect(api.getProfitControlPreview).toHaveBeenLastCalledWith(53, 0.2, 0, expect.anything())

    await w.setProps({ floor: 0.35 })
    expect(api.getProfitControlPreview).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(400)
    expect(api.getProfitControlPreview).toHaveBeenLastCalledWith(53, 0.2, 0.35, expect.anything())
  })

  it('does not ask while the floor is invalid', async () => {
    const w = mountPreview({ floor: 0.12345 })
    await flushPromises()
    expect(api.getProfitControlPreview).not.toHaveBeenCalled()
    expect(w.find('[data-testid="profit-control-preview-floor-invalid"]').exists()).toBe(true)
  })

  it('marks an empty range as danger and asks for confirmation before saving', async () => {
    api.getProfitControlPreview.mockResolvedValue(preview({ floor: 0.9, range_empty: true, passing: 0 }))
    const w = mountPreview({ floor: 0.9 })
    await flushPromises()
    expect(line(w).attributes('data-level')).toBe('danger')
    expect(line(w).text()).toContain('admin.groups.profitControl.floorRangeEmpty')
    expect(await blockingReason(w)).toBe('range_empty')
  })

  it('shows below-floor and pinned counts, and names pinned accounts above the ceiling', async () => {
    api.getProfitControlPreview.mockResolvedValue(preview({
      floor: 0.3,
      below_floor: 2,
      pinned: 2,
      pinned_loss: [{ account_id: 7, name: 'acc-07', rate: 1.2 }]
    }))
    const w = mountPreview({ floor: 0.3 })
    await flushPromises()
    expect(w.get('[data-testid="profit-control-preview-below-floor"]').text()).toContain('"n":2')
    expect(w.get('[data-testid="profit-control-preview-pinned"]').text()).toContain('"n":2')
    expect(w.get('[data-testid="profit-control-preview-pinned-loss"]').text()).toContain('acc-07')
    expect(await blockingReason(w)).toBeNull()
  })

  it('with profit control off previews only the floor: margin 0, no ceiling line, no pinned loss', async () => {
    api.getProfitControlPreview.mockResolvedValue(preview({
      floor: 0.3,
      passing: 0,
      below_floor: 6,
      pinned_loss: [{ account_id: 7, name: 'acc-07', rate: 1.2 }]
    }))
    const w = mountPreview({ profitEnabled: false, marginPercent: -5, floor: 0.3 })
    await flushPromises()
    expect(api.getProfitControlPreview).toHaveBeenLastCalledWith(53, 0, 0.3, expect.anything())
    expect(line(w).text()).toContain('admin.groups.profitControl.floorPreviewOnly')
    expect(line(w).attributes('data-level')).toBe('danger')
    expect(w.find('[data-testid="profit-control-preview-pinned-loss"]').exists()).toBe(false)
    expect(await blockingReason(w)).toBe('none_usable')
  })
})
