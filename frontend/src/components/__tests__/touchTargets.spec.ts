import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Touch sizes (mobile list plan, item 17; first-step browser check): on a coarse pointer the header's controls, the
// shared pagination and the accounts toolbar's "more actions" are at least 40px to hit and 8px apart; a mouse keeps
// them as they are. jsdom lays nothing out, so this checks that each control carries its hook and that the touch rule
// sizes it; the sizes themselves were measured in Chrome (delivery note, "第一步验收后修改").

const here = dirname(fileURLToPath(import.meta.url))
const read = (path: string) => readFileSync(resolve(here, '..', path), 'utf8')

/** The bodies of a file's `@media <query> { ... }` blocks, joined. */
function mediaBlocks(source: string, query: string) {
  const bodies: string[] = []
  let at = source.indexOf(`@media ${query} {`)
  while (at !== -1) {
    let depth = 0
    let end = at
    for (let i = source.indexOf('{', at); i < source.length; i++) {
      if (source[i] === '{') depth++
      else if (source[i] === '}' && --depth === 0) {
        end = i
        break
      }
    }
    bodies.push(source.slice(at, end + 1))
    at = source.indexOf(`@media ${query} {`, end)
  }
  return bodies.join('\n')
}
/** The declarations of `selector` inside the touch rules. */
function touchRule(source: string, selector: string, query = '(pointer: coarse)') {
  const block = mediaBlocks(source, query)
  const start = block.indexOf(`\n  ${selector} {`)
  expect(start, `${selector} in ${query}`).toBeGreaterThan(-1)
  return block.slice(start, block.indexOf('}', start))
}
const px = (rule: string, property: string) => Number(new RegExp(`(?:^|[\\s;{])${property}:\\s*(\\d+)px`).exec(rule)?.[1] ?? NaN)

describe('touch targets', () => {
  it('header: its own links and the search are 40px, 8px apart', () => {
    const source = read('layout/AppHeader.vue')
    expect(source).toContain('class="header-tools flex shrink-0 items-center gap-1 sm:gap-3"')
    expect(source.match(/class="header-hit /g)).toHaveLength(3) // search, docs, model plaza
    expect(source).toMatch(/class="header-hit [^"]*"\s*:title="`\$\{t\('commandPalette\.open'\)\}/)
    expect(px(touchRule(source, '.header-tools'), 'gap')).toBe(8)
    const hit = touchRule(source, '.header-hit')
    expect(px(hit, 'min-width')).toBeGreaterThanOrEqual(40)
    expect(px(hit, 'min-height')).toBeGreaterThanOrEqual(40)
  })

  it("header: the user menu's items, the ones moved in from the header among them, are 40px high and 8px apart", () => {
    const source = read('layout/AppHeader.vue')
    expect(source.match(/class="user-menu__list /g)).toHaveLength(3) // the main list, the tour, logout
    const list = touchRule(source, '.user-menu__list')
    expect(list).toContain('flex-direction: column;')
    expect(px(list, 'gap')).toBe(8)
    expect(px(touchRule(source, '.user-menu__list > .dropdown-item'), 'min-height')).toBe(40)
  })

  it('header: the bell, the language and the subscription badge are 40px', () => {
    const bell = read('common/AnnouncementBell.vue')
    expect(bell).toMatch(/@click="openModal"\s*class="bell-btn /)
    expect(px(touchRule(bell, '.bell-btn'), 'width')).toBe(40)
    expect(px(touchRule(bell, '.bell-btn'), 'height')).toBe(40)

    const locale = read('common/LocaleSwitcher.vue')
    expect(locale).toMatch(/@click="toggleDropdown"[\s\S]*?class="locale-btn /)
    expect(px(touchRule(locale, '.locale-btn'), 'min-width')).toBe(40)
    expect(px(touchRule(locale, '.locale-btn'), 'min-height')).toBe(40)

    const mini = read('common/SubscriptionProgressMini.vue')
    expect(mini).toMatch(/@click="toggleTooltip"\s*class="sub-mini-btn /)
    expect(px(touchRule(mini, '.sub-mini-btn'), 'min-height')).toBe(40)
  })

  it('accounts toolbar: "more actions" is a real 40px target at every width, like its neighbours', () => {
    const source = read('admin/account/table/AccountToolsMenu.vue')
    const rule = touchRule(source, '.tools-btn', '(any-pointer: coarse)')
    expect(px(rule, 'min-width')).toBe(40)
    expect(px(rule, 'min-height')).toBe(40)
    expect(source).not.toContain('.tools-btn::after')
  })

  it('pagination: page buttons 40px and 8px apart, the page size 40px high, a long run wraps', () => {
    const source = read('common/Pagination.vue')
    expect(source.match(/pager-btn relative inline-flex/g)).toHaveLength(3) // previous, the pages, next
    expect(px(touchRule(source, '.pager-nav'), 'gap')).toBe(8)
    const button = touchRule(source, '.pager-nav > .pager-btn:not([hidden])')
    expect(px(button, 'min-width')).toBe(40)
    expect(px(button, 'min-height')).toBe(40)
    expect(button).toContain('margin: 0;')
    expect(px(touchRule(source, '.page-size-select :deep(.select-trigger)'), 'min-height')).toBe(40)
    expect(touchRule(source, '.pager-wide')).toContain('flex-wrap: wrap;')
    expect(px(touchRule(source, 'input.pager-jump'), 'font-size')).toBe(16)
  })

  it('dialogs: the close button is 40px', () => {
    const source = read('common/BaseDialog.vue')
    expect(source).toContain('class="dialog-close ')
    const rule = touchRule(source, '.dialog-close')
    expect(px(rule, 'min-width')).toBe(40)
    expect(px(rule, 'min-height')).toBe(40)
  })

  // Astra review (users page): the row menu, the usage sort, the bulk menu, the filter and column settings, the
  // column sheet and the sheet's text field.
  it("users page: its menus' items 40px and 8px apart, the sheet's text field 40px at 16px", () => {
    const source = read('../views/admin/UsersView.vue')
    expect(source.match(/class="users-menu /g)).toHaveLength(5)
    const menu = touchRule(source, '.users-menu')
    expect(menu).toContain('flex-direction: column;')
    expect(px(menu, 'gap')).toBe(8)
    expect(px(touchRule(source, '.users-menu > button'), 'min-height')).toBe(40)
    const input = touchRule(source, '.users-sheet__input')
    expect(px(input, 'min-height')).toBe(40)
    expect(px(input, 'font-size')).toBe(16)
    expect(source).toMatch(/\.users-colsheet \{\s*display: grid;\s*gap: 8px;/)
  })

  // Step 2 acceptance: on a phone a switch scrolled half under the frozen "⋯" column touched it, a tap beside "⋯"
  // reaching the switch. The column starts with an 8px opaque strip that takes the tap and carries the edge line.
  it('table: an 8px tap-catching strip before the frozen right column on a phone with touch', () => {
    const source = read('common/DataTable.vue')
    const block = mediaBlocks(source, '(max-width: 767px) and (pointer: coarse)')
    expect(block).toMatch(/\.dt-layout-table :deep\(\.sticky-col-right\)::before \{\s*position: absolute;\s*top: 0;\s*bottom: 0;\s*left: -8px;\s*width: 8px;\s*background: inherit;/)
    expect(block).toMatch(/\.is-scrollable :deep\(\.sticky-col-right\)::before \{\s*box-shadow: inset 1px 0 0 var\(--color-line-strong\);/)
    // Keyboard focus keeps clear of the strip too.
    expect(source).toContain("right: Math.round(rightCell ? width(rightCell) + stripWidth : 0)")
  })

  // Follow-ups after #96, package A item 3: touch targets outside the tables.
  it('date range picker: the trigger, the presets, the date fields and "apply" are 40px high, presets 8px apart', () => {
    const source = read('common/DateRangePicker.vue')
    expect(px(touchRule(source, '.date-picker-trigger'), 'min-height')).toBe(40)
    expect(px(touchRule(source, '.date-picker-presets'), 'gap')).toBe(8)
    expect(px(touchRule(source, '.date-picker-preset'), 'min-height')).toBe(40)
    expect(px(touchRule(source, '.date-picker-apply'), 'min-height')).toBe(40)
    const input = touchRule(source, '.date-picker-input')
    expect(px(input, 'min-height')).toBe(40)
    expect(px(input, 'font-size')).toBe(16)
  })

  it('sidebar: submenu links are 40px high and 8px from every neighbour', () => {
    const source = read('layout/AppSidebar.vue')
    expect(source).toContain('class="sidebar-sublist mb-1 ')
    expect(source).toContain('class="sidebar-link sidebar-sublink ')
    const list = touchRule(source, '.sidebar-sublist')
    expect(list).toContain('flex-direction: column;')
    expect(px(list, 'gap')).toBe(8)
    // The group button above and the next item below sit 4px (mb-1) outside the list.
    expect(source).toMatch(/class="sidebar-link mb-1 w-full"/)
    expect(px(list, 'padding-top')).toBe(4)
    expect(px(list, 'padding-bottom')).toBe(4)
    const link = touchRule(source, '.sidebar-sublink')
    expect(px(link, 'min-height')).toBe(40)
    expect(link).toContain('margin-bottom: 0;')
  })

  it('usage charts: every toggle button is 40px high on touch, its neighbours 8px apart', () => {
    for (const name of ['ModelDistributionChart', 'GroupDistributionChart', 'EndpointDistributionChart']) {
      const source = read(`charts/${name}.vue`)
      const buttons = source.match(/class="rounded-md px-2\.5 py-1 text-xs font-medium transition-colors[^"]*"/g) ?? []
      expect(buttons.length, name).toBeGreaterThan(0)
      for (const button of buttons) expect(button, name).toContain('[@media(pointer:coarse)]:min-h-10')
      const groups = source.match(/class="(?:ml-auto )?inline-flex rounded-lg [^"]*"/g) ?? []
      expect(groups.length, name).toBeGreaterThan(0)
      for (const group of groups) expect(group, name).toContain('[@media(pointer:coarse)]:gap-2')
      // Follow-ups review (Astra): at 360 and 320 the English 40px buttons ran off the screen beside the title; the
      // header wraps, the buttons going under the title when they do not fit beside it.
      expect(source, name).toContain('<div class="mb-4 flex flex-wrap items-center justify-between gap-3">')
    }
  })
})
