import { Comment, Fragment, defineComponent, h, isVNode, toDisplayString, type PropType, type Slots, type VNode, type VNodeArrayChildren } from 'vue'
import type { Column } from './types'

// 与 Vue renderSlot 的回退规则一致：插槽只渲染出注释/空片段时显示默认内容。
const hasRenderableContent = (nodes: VNodeArrayChildren): boolean =>
  nodes.some((node) => {
    if (!isVNode(node)) return node != null && node !== false && node !== ''
    if (node.type === Comment) return false
    if (node.type === Fragment && Array.isArray(node.children)) return hasRenderableContent(node.children)
    return true
  })

/**
 * 桌面表格的单行。拆成独立组件后，DataTable 因虚拟滚动重渲染时，
 * props 未变的行直接跳过，只有新进入窗口的行才执行单元格插槽；
 * 插槽内读取的响应式数据由本行自己的渲染副作用追踪，数据变化时仍按行更新。
 */
export default defineComponent({
  name: 'DataTableRow',
  props: {
    row: { type: null as unknown as PropType<any>, required: true },
    index: { type: Number, required: true },
    columns: { type: Array as PropType<Column[]>, required: true },
    stickyClasses: { type: Array as PropType<string[]>, required: true },
    paddingClass: { type: String, required: true },
    selectable: { type: Boolean, default: false },
    selected: { type: Boolean, default: false },
    selectionDisabled: { type: Boolean, default: false },
    selectionLabel: { type: String, default: '' },
    clickable: { type: Boolean, default: false },
    expanded: { type: Boolean, default: false },
    cellSlots: { type: Object as PropType<Slots>, required: true }
  },
  emits: {
    toggle: (_row: any, _index: number, _checked: boolean) => true,
    rowClick: (_row: any) => true
  },
  setup(props, { emit }) {
    const renderCell = (column: Column) => {
      const value = props.row[column.key]
      const slot = props.cellSlots[`cell-${column.key}`]
      if (slot) {
        const nodes = slot({ row: props.row, value, expanded: props.expanded })
        if (hasRenderableContent(nodes)) return nodes
      }
      return toDisplayString(column.formatter ? column.formatter(value, props.row) : value)
    }

    return () => {
      const cells: VNode[] = []
      if (props.selectable) {
        cells.push(
          h('td', { class: 'w-11 min-w-11 px-3 py-4 text-center' }, [
            h('input', {
              type: 'checkbox',
              class:
                'h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800',
              checked: props.selected,
              disabled: props.selectionDisabled,
              'aria-label': props.selectionLabel,
              'data-test': 'select-row',
              onClick: (event: Event) => event.stopPropagation(),
              onChange: (event: Event) =>
                emit('toggle', props.row, props.index, (event.target as HTMLInputElement).checked)
            })
          ])
        )
      }
      props.columns.forEach((column, colIndex) => {
        cells.push(
          h(
            'td',
            {
              key: column.key,
              class: [
                'whitespace-nowrap py-4 text-sm text-gray-900 dark:text-gray-100',
                props.paddingClass,
                props.stickyClasses[colIndex],
                column.class
              ]
            },
            renderCell(column)
          )
        )
      })
      return h(
        'tr',
        {
          class: [
            'table-row-border hover:bg-gray-50 dark:hover:bg-dark-800',
            {
              'cursor-pointer': props.clickable,
              'bg-primary-50/40 dark:bg-primary-900/10': props.selectable && props.selected
            }
          ],
          onClick: () => {
            if (props.clickable) emit('rowClick', props.row)
          }
        },
        cells
      )
    }
  }
})
