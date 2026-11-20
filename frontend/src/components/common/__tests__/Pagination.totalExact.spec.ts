import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import Pagination from '../Pagination.vue'
import en from '@/i18n/locales/en/dashboard'
import zh from '@/i18n/locales/zh/dashboard'

// `totalExact` (mobile list plan, usage records): a list that counts only as far as it needs passes `total` as "the end
// of this page, plus one" while there is a next page. The range and the narrow line then leave the total out; the page
// buttons and "next" still go by it.

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key) })
}))
enableAutoUnmount(afterEach)

const mountPagination = (props: Record<string, unknown>) =>
  mount(Pagination, { props: { page: 1, pageSize: 20, showPageSizeSelector: false, ...props }, global: { stubs: { Icon: true } } })
const pageButtons = (wrapper: ReturnType<typeof mountPagination>) =>
  wrapper.findAll('nav[aria-label="Pagination"] button').map((button) => button.text())

describe('Pagination totalExact', () => {
  it('reads the exact total by default, as before', () => {
    const wrapper = mountPagination({ total: 74 })
    expect(wrapper.get('[data-test="pagination-range"]').text()).toBe('pagination.range {"from":"1","to":"20","total":"74"}')
    expect(wrapper.get('[data-test="pagination-page"]').text()).toBe('pagination.pageOf {"page":1,"total":4}')
  })

  it('leaves the total out when it is not exact, and still offers the next page', async () => {
    const wrapper = mountPagination({ total: 21, totalExact: false })
    expect(wrapper.get('[data-test="pagination-range"]').text()).toBe('pagination.rangeOpen {"from":"1","to":"20"}')
    expect(wrapper.get('[data-test="pagination-page"]').text()).toBe('pagination.pageOnly {"page":1}')
    expect(pageButtons(wrapper)).toEqual(['', '1', '2', ''])
    const next = wrapper.get('[data-test="pagination-compact"] button[aria-label="pagination.next"]')
    expect(next.attributes('disabled')).toBeUndefined()
    await next.trigger('click')
    expect(wrapper.emitted('update:page')).toEqual([[2]])

    // The last page: the total is exact there (no next page), and "next" is off.
    await wrapper.setProps({ page: 2, total: 33 })
    expect(wrapper.get('[data-test="pagination-range"]').text()).toBe('pagination.rangeOpen {"from":"21","to":"33"}')
    expect(wrapper.get('[data-test="pagination-compact"] button[aria-label="pagination.next"]').attributes('disabled')).toBeDefined()
  })

  it('has the copy in both languages', () => {
    expect(zh.pagination.rangeOpen).toBe('第 {from}–{to} 条')
    expect(zh.pagination.pageOnly).toBe('第 {page} 页')
    expect(en.pagination.rangeOpen).toBe('{from}–{to}')
    expect(en.pagination.pageOnly).toBe('Page {page}')
  })
})
