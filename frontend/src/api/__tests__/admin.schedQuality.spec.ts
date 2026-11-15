import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiClient } from '../client'
import { previewGroupPolicy } from '../admin/schedQuality'

afterEach(() => vi.restoreAllMocks())

describe('quality scheduling API', () => {
  it('posts the unsaved policy to the preview route and always returns a list of changed accounts', async () => {
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { now: { normal: 2, below: 0 }, after: { normal: 1, below: 1 }, changed: null } })
    const update = { expected_version: 63, policy: { enabled: true, ttft_seconds: 8, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 900 as const } }
    expect(await previewGroupPolicy(53, update)).toEqual({ now: { normal: 2, below: 0 }, after: { normal: 1, below: 1 }, changed: [] })
    expect(post).toHaveBeenCalledWith('/admin/sched-quality/groups/53/policy/preview', update, { signal: undefined })
  })
})
