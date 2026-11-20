import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../AppHeader.vue'), 'utf8')

// Mobile list plan: below 1024px the header shows the page's name right of the menu button, on one line that gives way
// (an ellipsis) before the buttons on the right do; the description stays a wide-screen line.
describe('AppHeader page title on narrow screens', () => {
  it('shows the title at every width, one line cut with an ellipsis; the description from 1024px only', () => {
    const title = source.match(/<div class="min-w-0">\s*<h1 class="([^"]*)" data-test="page-title">\s*\{\{ pageTitle \}\}/)
    expect(title).not.toBeNull()
    expect(title![1].split(' ')).toEqual(expect.arrayContaining(['truncate', 'text-base', 'lg:text-lg']))
    expect(source).toMatch(/<p v-if="pageDescription" class="hidden truncate [^"]*lg:block">/)
  })

  it('lets the title side shrink while the buttons on the right keep their size', () => {
    expect(source).toContain('<div class="flex min-w-0 flex-1 items-center gap-2 sm:gap-4">')
    expect(source).toContain('<div class="header-tools flex shrink-0 items-center gap-1 sm:gap-3">')
  })

  // Opus review, suggestion 8: on the user side at 768–1023px every item on the right squeezed the title to 0px.
  // Astra re-review, suggestion: at 1280 beside the open navigation the English title still got 12px; the links and
  // the frozen amount now fold below 1440px.
  it('folds the right side\'s labels below 1440px and the name below 1280px so the title keeps its room', () => {
    expect(source).toContain(`<span class="hidden min-[1440px]:inline">{{ t('nav.docs') }}</span>`)
    expect(source).toContain(`<span class="hidden min-[1440px]:inline">{{ t('nav.modelPlaza') }}</span>`)
    expect(source).toMatch(/<div class="hidden min-w-0 max-w-\[10rem\] text-left xl:block">\s*<div class="truncate/)
    expect(source).toMatch(/v-if="frozenBalance > 0"\s*class="hidden [^"]*min-\[1440px\]:inline"/)
  })

  // Astra second re-review #4: a link folded to its icon keeps its name; the folded frozen amount stays readable and,
  // in the user menu, reachable by a tap.
  it('names the folded links and keeps the folded frozen amount reachable', () => {
    expect(source).toMatch(/v-if="docUrl"[\s\S]*?:title="t\('nav\.docs'\)"\s*:aria-label="t\('nav\.docs'\)"/)
    expect(source).toMatch(/:title="t\('nav\.modelPlaza'\)"\s*:aria-label="t\('nav\.modelPlaza'\)"/)
    expect(source).toContain('<span v-if="frozenBalance > 0" class="sr-only min-[1440px]:hidden">{{ balanceFrozenLabel }}</span>')
    expect(source).toContain('<div class="border-b border-gray-100 px-4 py-2 dark:border-dark-700 min-[1440px]:hidden">')
  })

  // Astra third re-review #1: with the balance in it, the menu ran past a phone on its side and Logout was out of reach.
  it('keeps the user menu within the visible height, scrolling on its own', () => {
    expect(source).toContain('class="dropdown user-menu right-0 mt-2 w-56"')
    expect(source).toMatch(/\.user-menu \{\s*max-height: calc\(100vh - 72px\);\s*max-height: calc\(100dvh - 72px\);\s*overflow-y: auto;\s*overscroll-behavior: contain;/)
  })

  // Astra post-acceptance review #1: at 360px with 40px buttons the menu button and the search overlapped, and the
  // title had no room from 390px down. Below 768px the secondary entries sit in the user menu.
  it('folds the search, the model plaza and the subscriptions into the user menu below 768px, the docs below 640px', () => {
    expect(source).toMatch(/class="header-hit hidden [^"]* md:flex"\s*:title="`\$\{t\('commandPalette\.open'\)\}/)
    expect(source).toMatch(/:aria-label="t\('nav\.modelPlaza'\)"\s*class="header-hit hidden [^"]* md:flex"/)
    expect(source).toContain('<SubscriptionProgressMini v-if="user && subscriptionFeatureEnabled" class="hidden md:block" />')
    expect(source).toContain('class="dropdown-item w-full md:hidden" data-test="menu-search" @click="closeDropdown(); openPalette()"')
    expect(source).toMatch(/v-if="modelPlazaEnabled"[^>]*class="dropdown-item md:hidden" data-test="menu-model-plaza"/)
    expect(source).toMatch(/v-if="subscriptionFeatureEnabled && hasActiveSubscriptions" to="\/subscriptions" class="dropdown-item md:hidden"/)
    expect(source).toMatch(/v-if="docUrl"[^>]*class="dropdown-item sm:hidden" data-test="menu-docs"/)
  })
})
