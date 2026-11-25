import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiClient } from '../client'
import { previewGroupPolicy } from '../admin/schedQuality'

afterEach(() => vi.restoreAllMocks())

describe('quality scheduling API', () => {
  // Plan item 19: the form reads the relaxed lines and each model's reach; the rest of the answer is not copied.
  it('posts the unsaved policy to the preview route and keeps only the relaxed lines and the reach, each a list', async () => {
    const reach = [{ model: 'gpt-a', cache: { ok: 2, n: 3 }, first_output: { ok: 1, n: 3 } }]
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { relaxed_lines: ['cache'], reach, lines: {}, changed: [] } })
    const update = { expected_version: 63, policy: { enabled: true, ttft_seconds: 8, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 900 as const } }
    expect(await previewGroupPolicy(53, update)).toEqual({ relaxed_lines: ['cache'], reach })
    expect(post).toHaveBeenCalledWith('/admin/sched-quality/groups/53/policy/preview', update, { signal: undefined })
    post.mockResolvedValue({ data: { relaxed_lines: null } })
    expect(await previewGroupPolicy(53, update)).toEqual({ relaxed_lines: [], reach: [] })
  })
})
