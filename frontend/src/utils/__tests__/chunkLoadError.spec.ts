import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isChunkLoadError, recoverFromChunkLoadError } from '../chunkLoadError'

describe('isChunkLoadError', () => {
  it.each([
    'Failed to fetch dynamically imported module: https://x/assets/a.js',
    'error loading dynamically imported module',
    'Importing a module script failed.',
    'Unable to preload CSS for /assets/a.css',
    'Loading chunk 12 failed.'
  ])('recognizes %s', (message) => {
    expect(isChunkLoadError(new Error(message))).toBe(true)
  })

  it('recognizes ChunkLoadError by name and ignores other errors', () => {
    const named = new Error('x')
    named.name = 'ChunkLoadError'
    expect(isChunkLoadError(named)).toBe(true)
    expect(isChunkLoadError(new Error('Request failed with status code 500'))).toBe(false)
    expect(isChunkLoadError(null)).toBe(false)
    expect(isChunkLoadError('Failed to fetch dynamically imported module')).toBe(false)
  })
})

describe('recoverFromChunkLoadError', () => {
  const assign = vi.fn()
  const reload = vi.fn()

  beforeEach(() => {
    sessionStorage.clear()
    vi.stubGlobal('location', { ...window.location, pathname: '/admin/dashboard', search: '', hash: '', assign, reload })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    assign.mockReset()
    reload.mockReset()
  })

  it('opens the target page once and then respects the cooldown', () => {
    expect(recoverFromChunkLoadError('/admin/accounts')).toBe(true)
    expect(assign).toHaveBeenCalledWith('/admin/accounts')
    expect(recoverFromChunkLoadError('/admin/accounts')).toBe(false)
    expect(assign).toHaveBeenCalledTimes(1)
  })

  it('reloads the current page when no other target is given', () => {
    expect(recoverFromChunkLoadError()).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(assign).not.toHaveBeenCalled()
  })

  it('retries after the cooldown has passed', () => {
    sessionStorage.setItem('chunk_reload_attempted', String(Date.now() - 11_000))
    expect(recoverFromChunkLoadError()).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
  })
})
