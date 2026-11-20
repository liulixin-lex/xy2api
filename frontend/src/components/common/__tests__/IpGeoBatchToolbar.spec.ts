// Browser acceptance (step 1), item 2: on touch the batch button is 40px high and its row loses its padding, so the
// row keeps its 40px.
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import IpGeoBatchToolbar from '../IpGeoBatchToolbar.vue'

vi.mock('@/utils/ipGeoLookup', () => ({ getEntry: vi.fn(() => ({ status: 'idle' })), fetchBatch: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('IpGeoBatchToolbar on touch', () => {
  it('is 40px high on a coarse pointer, the row without padding there', () => {
    const wrapper = mount(IpGeoBatchToolbar, { props: { ips: ['203.0.113.7'] } })
    expect(wrapper.classes()).toEqual(expect.arrayContaining(['py-2', '[@media(pointer:coarse)]:py-0']))
    expect(wrapper.get('[data-testid="ip-geo-batch"]').classes()).toContain('[@media(pointer:coarse)]:min-h-10')
  })
})
