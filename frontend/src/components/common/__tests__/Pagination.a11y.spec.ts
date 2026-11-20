import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import Pagination from '../Pagination.vue'

// The page-size picker reads its name in the page's language at every width (Astra post-acceptance review,
// suggestion 4): the wide one read "Select option".

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

describe('Pagination names', () => {
  it('names both page-size pickers', () => {
    const wrapper = mount(Pagination, {
      props: { total: 100, page: 1, pageSize: 20 },
      global: { stubs: { Icon: true, Select: { props: ['ariaLabel'], template: '<button class="select-trigger" :aria-label="ariaLabel" />' } } }
    })
    expect(wrapper.get('[data-test="pagination-compact"] .select-trigger').attributes('aria-label')).toBe('pagination.perPage')
    expect(wrapper.get('.pager-wide .select-trigger').attributes('aria-label')).toBe('pagination.perPage')
  })
})
