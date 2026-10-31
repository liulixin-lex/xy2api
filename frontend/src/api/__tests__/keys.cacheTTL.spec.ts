import { beforeEach, describe, expect, it, vi } from 'vitest'
import { create, update, list } from '../keys'

const { post, put, get } = vi.hoisted(() => ({ post: vi.fn(), put: vi.fn(), get: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { post, put, get } }))

describe('API key Claude cache TTL contract', () => {
  beforeEach(() => {
    post.mockReset().mockResolvedValue({ data: {} })
    put.mockReset().mockResolvedValue({ data: {} })
    get.mockReset()
  })

  it('omits inherited create preference and sends explicit modes without changing positional callers', async () => {
    await create('Default', 2)
    await create('Inherited', 2, undefined, undefined, undefined, undefined, undefined, undefined, 'inherit')
    await create('Explicit', 2, undefined, undefined, undefined, undefined, undefined, undefined, 'off')
    expect(post.mock.calls.map(([, body]) => body)).toEqual([
      { name: 'Default', group_id: 2 }, { name: 'Inherited', group_id: 2 },
      { name: 'Explicit', group_id: 2, anthropic_cache_ttl_mode: 'off' }
    ])
  })

  it('preserves patch omission and returns the list default metadata', async () => {
    await update(2, { name: 'Rename' })
    await update(2, { anthropic_cache_ttl_mode: 'inherit' })
    expect(put.mock.calls).toEqual([
      ['/keys/2', { name: 'Rename' }], ['/keys/2', { anthropic_cache_ttl_mode: 'inherit' }]
    ])
    const response = { items: [], total: 0, pages: 0, meta: { anthropic_cache_ttl_default: '1h' } }
    get.mockResolvedValue({ data: response })
    expect(await list()).toEqual(response)
  })
})
