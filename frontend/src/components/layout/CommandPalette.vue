<template>
  <Teleport to="body">
    <Transition name="modal" @after-leave="restoreFocus">
      <div
        v-if="open"
        class="modal-overlay items-start pt-[12vh]"
        data-test="command-palette"
        @mousedown.self="closePalette"
      >
        <div
          class="modal-content max-w-xl overflow-hidden"
          role="dialog"
          aria-modal="true"
          :aria-label="t('commandPalette.title')"
        >
          <div class="flex items-center gap-3 border-b border-gray-200 px-4 dark:border-dark-700">
            <Icon name="search" size="md" class="flex-shrink-0 text-gray-400 dark:text-dark-400" />
            <input
              ref="inputRef"
              v-model="query"
              type="text"
              role="combobox"
              aria-autocomplete="list"
              aria-expanded="true"
              :aria-controls="listboxId"
              :aria-activedescendant="activeOptionId"
              :placeholder="t('commandPalette.placeholder')"
              class="h-12 w-full bg-transparent text-sm text-gray-900 outline-none placeholder:text-gray-400 dark:text-white dark:placeholder:text-dark-400"
              autocomplete="off"
              spellcheck="false"
              data-test="command-palette-input"
              @keydown="onKeydown"
            />
            <kbd class="hidden flex-shrink-0 rounded border border-gray-200 px-1.5 py-0.5 text-[11px] font-medium text-gray-500 dark:border-dark-600 dark:text-dark-400 sm:inline">Esc</kbd>
          </div>

          <ul
            v-if="results.length"
            :id="listboxId"
            ref="listRef"
            role="listbox"
            :aria-label="t('commandPalette.results')"
            class="max-h-[min(60vh,420px)] overflow-y-auto p-2"
          >
            <li
              v-for="(entry, index) in results"
              :id="`${listboxId}-${index}`"
              :key="entry.path"
              role="option"
              :aria-selected="index === activeIndex"
              class="flex cursor-pointer items-center justify-between gap-3 rounded-lg px-3 py-2 text-sm transition-colors duration-100"
              :class="
                index === activeIndex
                  ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                  : 'text-gray-700 dark:text-gray-200'
              "
              data-test="command-palette-option"
              @mousemove="activeIndex = index"
              @click="go(entry)"
            >
              <span class="min-w-0 truncate">
                <span v-if="entry.section" class="text-gray-400 dark:text-dark-400">{{ entry.section }} / </span>{{ entry.label }}
              </span>
              <span v-if="entry.path === currentPath" class="flex-shrink-0 text-xs text-gray-400 dark:text-dark-400">
                {{ t('commandPalette.current') }}
              </span>
            </li>
          </ul>
          <p v-else :id="listboxId" role="status" class="px-4 py-8 text-center text-sm text-gray-500 dark:text-dark-400">
            {{ t('commandPalette.empty') }}
          </p>

          <div class="hidden items-center gap-4 border-t border-gray-200 px-4 py-2 text-xs text-gray-500 dark:border-dark-700 dark:text-dark-400 sm:flex">
            <span>↑ ↓ {{ t('commandPalette.hintMove') }}</span>
            <span>Enter {{ t('commandPalette.hintOpen') }}</span>
            <span>Esc {{ t('commandPalette.hintClose') }}</span>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import { searchEntries, useCommandPalette, type CommandEntry } from '@/composables/useCommandPalette'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { open, entries, closePalette, togglePalette } = useCommandPalette()

const listboxId = 'command-palette-listbox'
const query = ref('')
const activeIndex = ref(0)
const inputRef = ref<HTMLInputElement | null>(null)
const listRef = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null

const currentPath = computed(() => route.path)
const results = computed(() => searchEntries(entries.value, query.value))
const activeOptionId = computed(() => (results.value.length ? `${listboxId}-${activeIndex.value}` : undefined))

watch(query, () => {
  activeIndex.value = 0
})

watch(open, async (isOpen) => {
  if (!isOpen) return
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  query.value = ''
  activeIndex.value = 0
  await nextTick()
  inputRef.value?.focus()
})

watch(activeIndex, async (index) => {
  await nextTick()
  const option = listRef.value?.querySelector<HTMLElement>(`#${listboxId}-${index}`)
  option?.scrollIntoView?.({ block: 'nearest' })
})

function restoreFocus() {
  if (previousFocus?.isConnected) previousFocus.focus()
  previousFocus = null
}

function go(entry: CommandEntry) {
  closePalette()
  if (entry.path !== route.path) void router.push(entry.path)
}

function move(delta: number) {
  const count = results.value.length
  if (!count) return
  activeIndex.value = (activeIndex.value + delta + count) % count
}

function onKeydown(event: KeyboardEvent) {
  if (event.isComposing) return
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault()
      move(1)
      break
    case 'ArrowUp':
      event.preventDefault()
      move(-1)
      break
    case 'Home':
    case 'End':
      // 输入框里有文字时保留光标跳转的原生行为
      if (query.value) return
      event.preventDefault()
      activeIndex.value = event.key === 'Home' ? 0 : Math.max(0, results.value.length - 1)
      break
    case 'Enter': {
      event.preventDefault()
      const entry = results.value[activeIndex.value]
      if (entry) go(entry)
      break
    }
    case 'Escape':
      event.preventDefault()
      event.stopPropagation()
      closePalette()
      break
    case 'Tab':
      // 面板里只有一个可聚焦元素，Tab 用来在结果间移动
      event.preventDefault()
      move(event.shiftKey ? -1 : 1)
      break
  }
}

// 其他弹窗打开时不抢快捷键，避免叠在确认框、编辑框上面
function otherDialogOpen() {
  return Array.from(document.querySelectorAll('[aria-modal="true"]')).some(
    (el) => !el.closest('[data-test="command-palette"]')
  )
}

function onGlobalKeydown(event: KeyboardEvent) {
  if (event.key.toLowerCase() !== 'k' || !(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey) return
  if (!open.value && otherDialogOpen()) return
  event.preventDefault()
  togglePalette()
}

onMounted(() => {
  window.addEventListener('keydown', onGlobalKeydown)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onGlobalKeydown)
  // 布局随路由重建；打开状态是模块级的，卸载时收起，免得新页面带着未聚焦的面板出现
  closePalette()
})
</script>
