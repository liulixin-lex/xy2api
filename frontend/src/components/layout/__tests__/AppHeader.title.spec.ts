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
    expect(source).toContain('<div class="flex shrink-0 items-center gap-1 sm:gap-3">')
  })

  // Opus review, suggestion 8: on the user side at 768–1023px every item on the right squeezed the title to 0px.
  it('folds the right side\'s labels below 1280px so the title keeps its room', () => {
    expect(source).toContain(`<span class="hidden xl:inline">{{ t('nav.docs') }}</span>`)
    expect(source).toContain(`<span class="hidden xl:inline">{{ t('nav.modelPlaza') }}</span>`)
    expect(source).toMatch(/<div class="hidden min-w-0 max-w-\[10rem\] text-left xl:block">\s*<div class="truncate/)
    expect(source).toMatch(/v-if="frozenBalance > 0"\s*class="hidden [^"]*xl:inline"/)
  })
})
