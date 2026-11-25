import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiClient } from '../client'
import { previewGroupPolicy } from '../admin/schedQuality'

afterEach(() => vi.restoreAllMocks())

describe('quality scheduling API', () => {
  it('posts the unsaved policy to the preview route and always returns lists for the relaxed lines and changed keys', async () => {
    const lines = { ttfo: { now: { pass: 2, unknown: 0, fail: 0 }, after: { pass: 1, unknown: 0, fail: 1 } } }
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { relaxed_lines: null, lines, changed: null } })
    const update = { expected_version: 63, policy: { enabled: true, ttft_seconds: 8, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 900 as const } }
    expect(await previewGroupPolicy(53, update)).toEqual({ relaxed_lines: [], lines, changed: [], reach: [] })
    expect(post).toHaveBeenCalledWith('/admin/sched-quality/groups/53/policy/preview', update, { signal: undefined })
  })

  // Plan item 19: the per-model reach comes through as the backend sends it.
  it('passes on each model\'s reach', async () => {
    const reach = [{ model: 'gpt-a', cache: { ok: 2, n: 3 }, first_output: { ok: 1, n: 3 } }]
    vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { relaxed_lines: [], lines: {}, changed: [], reach } })
    const update = { expected_version: 63, policy: { enabled: true, ttft_seconds: 8, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 900 as const } }
    expect((await previewGroupPolicy(53, update)).reach).toEqual(reach)
  })
})
