import { isConfigured } from './schema'
import type { AvailablePolicy, SchemaField } from './types'

/** Fields whose stored value differs from the default: what the key would do once it applies. */
export function configuredFields(entry: AvailablePolicy): SchemaField[] {
  return entry.schema.filter((f) => isConfigured(entry, entry.stored.spec, f))
}
