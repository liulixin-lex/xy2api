import { ref, shallowRef, watchEffect, onScopeDispose } from 'vue'

export interface CommandEntry {
  /** 跳转地址，同时作为唯一键 */
  path: string
  label: string
  /** 所属分组名（如“渠道管理”），用于展示和搜索 */
  section?: string
}

interface NavSource {
  path: string
  label: string
  expandOnly?: boolean
  children?: NavSource[]
}

// 模块级状态：顶栏按钮和全局快捷键共用同一个开关；
// 页面清单由侧栏注册，复用侧栏已经算好的权限、功能开关与简洁模式过滤。
const open = ref(false)
const entries = shallowRef<CommandEntry[]>([])

export function flattenNav(items: NavSource[], section?: string): CommandEntry[] {
  const out: CommandEntry[] = []
  const seen = new Set<string>()
  const visit = (list: NavSource[], parent?: string) => {
    for (const item of list) {
      if (item.children?.length) {
        if (!item.expandOnly && !seen.has(item.path)) {
          seen.add(item.path)
          out.push({ path: item.path, label: item.label, section: parent })
        }
        visit(item.children, item.label)
        continue
      }
      if (seen.has(item.path)) continue
      seen.add(item.path)
      out.push({ path: item.path, label: item.label, section: parent })
    }
  }
  visit(items, section)
  return out
}

/** 侧栏调用：注册当前用户可见的页面。侧栏卸载时清空，避免残留上一身份的菜单。 */
export function registerCommandEntries(source: () => CommandEntry[]) {
  watchEffect(() => {
    entries.value = source()
  })
  onScopeDispose(() => {
    entries.value = []
  })
}

const normalize = (value: string) => value.toLowerCase().replace(/\s+/g, '')

/** 按顺序包含所有字符即可命中；前缀 > 连续包含 > 分散命中，同分按原顺序。 */
export function scoreEntry(entry: CommandEntry, query: string): number {
  const q = normalize(query)
  if (!q) return 1
  const label = normalize(entry.label)
  const haystacks = [label, normalize(entry.section ?? '') + label, entry.path.toLowerCase()]
  let best = 0
  for (const text of haystacks) {
    if (!text) continue
    if (text.startsWith(q)) best = Math.max(best, text === label ? 4 : 3)
    else if (text.includes(q)) best = Math.max(best, 2)
    else {
      let i = 0
      for (const ch of text) if (ch === q[i]) i++
      if (i === q.length) best = Math.max(best, 1)
    }
  }
  return best
}

export function searchEntries(list: CommandEntry[], query: string, limit = 50): CommandEntry[] {
  return list
    .map((entry, index) => ({ entry, index, score: scoreEntry(entry, query) }))
    .filter((item) => item.score > 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
    .slice(0, limit)
    .map((item) => item.entry)
}

export function useCommandPalette() {
  return {
    open,
    entries,
    openPalette: () => {
      open.value = true
    },
    closePalette: () => {
      open.value = false
    },
    togglePalette: () => {
      open.value = !open.value
    }
  }
}
