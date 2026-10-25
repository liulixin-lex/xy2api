import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

const here = dirname(fileURLToPath(import.meta.url))
const read = (path: string) => readFileSync(resolve(here, path), 'utf8')

describe('Cache observability integration surface', () => {
  it('registers an admin-only route', () => {
    const router = read('../../../router/index.ts')
    const start = router.indexOf("path: '/admin/cache'")
    expect(start).toBeGreaterThan(0)
    const route = router.slice(start, router.indexOf('}', router.indexOf('meta:', start)))
    expect(route).toContain('requiresAuth: true')
    expect(route).toContain('requiresAdmin: true')
  })

  it('adds a top-level admin menu entry right after Ops', () => {
    const sidebar = read('../../../components/layout/AppSidebar.vue')
    const ops = sidebar.indexOf("path: '/admin/ops'")
    const cache = sidebar.indexOf("path: '/admin/cache'")
    const users = sidebar.indexOf("path: '/admin/users'")
    expect(ops).toBeGreaterThan(0)
    expect(cache).toBeGreaterThan(ops)
    expect(cache).toBeLessThan(users)
    expect(zh.nav.cacheObservability).toBeTruthy()
    expect(en.nav.cacheObservability).toBeTruthy()
  })

  it('shows the daily-check tab only when the chart endpoints exist and keeps mock data out of production', () => {
    const view = read('../CacheObservabilityView.vue')
    expect(view).toContain('<CacheSettingsPanel />')
    expect(view).toContain('v-if="charts.available.value"')
    expect(view).not.toMatch(/mock/i)
    const source = read('../charts/dataSource.ts')
    expect(source).toContain('if (!env.DEV) return')
    expect(source).toContain("if (import.meta.env.DEV && source === 'mock')")
    expect(read('../settings/api.ts')).toContain("const base = '/admin/cache/policies'")
  })
})
