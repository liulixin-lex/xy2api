import { describe, expect, it } from 'vitest'
import {
  FCO_COMPAT_EXTRA_KEY,
  FCO_COMPAT_MAX_ADMIN_NAMES,
  applyFCOCompatAccountMode,
  extraForFCOCompatAccount,
  fcoCompatAdminNameProblem,
  isFCOCompatReservedName,
  readFCOCompatAccountMode
} from '../fcoCompat'

// The cases mirror backend/internal/resilience/settings_fco_compat_test.go.
describe('fco compat helpers', () => {
  it('accepts the same names as the server', () => {
    for (const name of ['a', 'Z', '0', '_', 'Browser_Info', 'browser_info2', 'codex_apps_selected_page_part', 'codex_apps_open_page_v2', 'A'.repeat(128)]) {
      expect(fcoCompatAdminNameProblem(name, []), name).toBeNull()
    }
  })

  it('rejects malformed names without trimming', () => {
    for (const name of ['', ' leading', 'trailing ', 'with space', 'dash-name', 'dot.name', '名字', 'tab\tname', 'line\n', 'a'.repeat(129)]) {
      expect(fcoCompatAdminNameProblem(name, []), JSON.stringify(name)).toBe('invalid')
    }
  })

  it('rejects the built-in names and the whole page-part family', () => {
    for (const name of [
      'browser_info', 'goal_objective', 'untrusted_context', 'ide_context', 'untrusted_content',
      'proposed_plan', 'terminal_info', 'browser_tabs', 'codex_apps_open_page',
      'codex_apps_selected_page_part_', 'codex_apps_selected_page_part_1_of_2', 'codex_apps_selected_page_part_x'
    ]) {
      expect(isFCOCompatReservedName(name), name).toBe(true)
      expect(fcoCompatAdminNameProblem(name, []), name).toBe('reserved')
    }
  })

  it('rejects duplicates and names past the cap', () => {
    expect(fcoCompatAdminNameProblem('tool', ['tool'])).toBe('duplicate')
    expect(fcoCompatAdminNameProblem('Tool', ['tool'])).toBeNull()
    const full = Array.from({ length: FCO_COMPAT_MAX_ADMIN_NAMES }, (_, i) => `name_${i}`)
    expect(fcoCompatAdminNameProblem('one_more', full)).toBe('full')
    expect(fcoCompatAdminNameProblem('one_more', full.slice(1))).toBeNull()
  })

  it('reads and writes the account override; inherit removes the key', () => {
    expect(readFCOCompatAccountMode(undefined)).toBe('inherit')
    expect(readFCOCompatAccountMode({ [FCO_COMPAT_EXTRA_KEY]: null })).toBe('inherit')
    expect(readFCOCompatAccountMode({ [FCO_COMPAT_EXTRA_KEY]: 'Proactive' })).toBe('inherit')
    expect(readFCOCompatAccountMode({ [FCO_COMPAT_EXTRA_KEY]: 'proactive' })).toBe('proactive')

    const extra: Record<string, unknown> = { other: true }
    applyFCOCompatAccountMode(extra, 'off')
    expect(extra).toEqual({ other: true, [FCO_COMPAT_EXTRA_KEY]: 'off' })
    applyFCOCompatAccountMode(extra, 'inherit')
    expect(extra).toEqual({ other: true })
  })

  it('drops the override only from accounts that cannot carry it', () => {
    const extra = { [FCO_COMPAT_EXTRA_KEY]: 'proactive', other: 1 }
    expect(extraForFCOCompatAccount(extra, 'openai', 'apikey')).toBe(extra)
    for (const [platform, type] of [['openai', 'oauth'], ['anthropic', 'apikey'], [undefined, undefined]]) {
      expect(extraForFCOCompatAccount(extra, platform, type)).toEqual({ other: 1 })
    }
    expect(extra).toEqual({ [FCO_COMPAT_EXTRA_KEY]: 'proactive', other: 1 })
    const without = { other: 1 }
    expect(extraForFCOCompatAccount(without, 'anthropic', 'apikey')).toBe(without)
    expect(extraForFCOCompatAccount(undefined, 'anthropic', 'apikey')).toBeUndefined()
  })
})
