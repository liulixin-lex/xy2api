import { defineComponent, h, type Component } from 'vue'
import { useLiveTable, type LiveTable } from '@/composables/useLiveTable'
import { useSchedLayerTasks } from '@/composables/useSchedLayerData'

/**
 * `layer` inside a page whose live table fetches the quality panel's and the group board's data, wired as the accounts
 * and groups pages wire it. Props given to the host go on to `layer`.
 */
export function withSchedHost(layer: Component, options: { onTable?: (table: LiveTable<{ id: number }>) => void } = {}) {
  return defineComponent({
    inheritAttrs: false,
    setup(_, { attrs }) {
      const sched = useSchedLayerTasks()
      const table = useLiveTable<{ id: number }>({
        getId: (row) => row.id,
        fetchPage: async () => ({ items: [], total: 0 }),
        membershipIntervalMs: 0,
        tasks: sched.tasks
      })
      sched.bind(table)
      options.onTable?.(table)
      void table.reload()
      return () => h(layer, attrs)
    }
  })
}
