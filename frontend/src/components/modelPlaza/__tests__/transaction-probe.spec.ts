import { it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PlazaGroupSection from '../PlazaGroupSection.vue'
import type { ModelPlazaGroup } from '@/api/modelPlaza'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: null }) }))

it('observes the same exclusive group with badge visibility disabled', () => {
  const group = {
    id: 71, name: 'transaction-group', description: '', platform: 'openai',
    subscription_type: 'standard', rate_multiplier: 1, is_exclusive: true,
    show_exclusive_badge: false, models: [], long_context_pricing_enabled: true
  } as unknown as ModelPlazaGroup
  const wrapper = mount(PlazaGroupSection, {
    props: { group }, global: { stubs: { GroupBadge: true, Icon: true, PlazaModelPricingTable: true } }
  })
  expect(wrapper.findComponent({ name: 'GroupBadge' }).exists()).toBe(true)
  console.log(JSON.stringify({
    input: { is_exclusive: true, show_exclusive_badge: false },
    group_rendered: true,
    exclusive_badge_visible: wrapper.text().includes('modelPlaza.badges.exclusive'),
    exclusive_permission: group.is_exclusive
  }))
  wrapper.unmount()
})
