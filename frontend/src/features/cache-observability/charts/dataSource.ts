import { liveCachePanelClient } from './api'
import type { CachePanelClient } from './types'

export type ChartsDataSource = 'live' | 'mock'

interface SourceEnv {
  DEV: boolean
  VITE_CACHE_PANEL_DATA_SOURCE?: string
}

/**
 * Production builds always read the live endpoints (CACHE_PANEL_API §5, §7.7).
 * Development builds may preview synthetic data with
 * VITE_CACHE_PANEL_DATA_SOURCE=mock or `?source=mock`.
 */
export function resolveDataSource(env: SourceEnv = import.meta.env, search: string = window.location.search): ChartsDataSource {
  if (!env.DEV) return 'live'
  if (new URLSearchParams(search).get('source') === 'mock') return 'mock'
  return env.VITE_CACHE_PANEL_DATA_SOURCE === 'mock' ? 'mock' : 'live'
}

export async function createChartsClient(source: ChartsDataSource): Promise<CachePanelClient> {
  // The mock module is only reachable from development builds, so it is tree-shaken out of production bundles.
  if (import.meta.env.DEV && source === 'mock') {
    const { createMockCachePanelClient } = await import('./mock')
    return createMockCachePanelClient()
  }
  return liveCachePanelClient
}
