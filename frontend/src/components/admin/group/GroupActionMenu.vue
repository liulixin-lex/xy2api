<template>
  <Teleport to="body">
    <div v-if="show && group && anchorRect" ref="rootRef">
      <div class="fixed inset-0" :style="{ zIndex: layer.zIndex.value }" @click="emit('close')"></div>
      <div
        ref="menuRef"
        role="menu"
        :aria-label="group.name"
        class="fixed w-52 overflow-y-auto overscroll-contain rounded-popover border border-line-popover bg-surface-popover py-1 shadow-dropdown"
        :style="[menuStyle, { zIndex: (layer.zIndex.value ?? 9998) + 1 }]"
        data-test="group-action-menu"
        @click.stop
      >
        <button v-if="actions.board" type="button" role="menuitem" :class="ITEM" data-test="menu-board" @click="pick('board')">
          <Icon name="chart" size="sm" :class="ICON" />{{ t('admin.schedQuality.action.board') }}
        </button>
        <button v-if="actions.edit" type="button" role="menuitem" :class="ITEM" data-test="menu-edit" @click="pick('edit')">
          <Icon name="edit" size="sm" :class="ICON" />{{ t('common.edit') }}
        </button>
        <button
          v-if="actions.duplicate"
          type="button"
          role="menuitem"
          :class="[ITEM, 'disabled:cursor-not-allowed disabled:opacity-50']"
          :disabled="duplicating"
          data-test="menu-duplicate"
          @click="pick('duplicate')"
        >
          <Icon name="copy" size="sm" :class="ICON" />{{ duplicating ? t('admin.groups.duplicating') : t('admin.groups.duplicate') }}
        </button>
        <button v-if="actions.composite" type="button" role="menuitem" :class="ITEM" data-test="menu-composite-routes" @click="pick('composite')">
          <Icon name="swap" size="sm" :class="ICON" />{{ t('admin.groups.compositeRoutes.action') }}
        </button>
        <button v-if="actions.rate" type="button" role="menuitem" :class="ITEM" data-test="menu-rate-multipliers" @click="pick('rate')">
          <Icon name="dollar" size="sm" :class="ICON" />{{ t('admin.groups.rateMultipliers') }}
        </button>
        <button v-if="actions.rpm" type="button" role="menuitem" :class="ITEM" data-test="menu-rpm-overrides" @click="pick('rpm')">
          <Icon name="bolt" size="sm" :class="ICON" />{{ t('admin.groups.rpmOverrides') }}
        </button>
        <template v-if="actions.delete">
          <div class="my-1 border-t border-line-popover"></div>
          <button type="button" role="menuitem" :class="[ITEM, DANGER]" data-test="menu-delete" @click="pick('delete')">
            <Icon name="trash" size="sm" class="shrink-0" />{{ t('common.delete') }}
          </button>
        </template>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
/**
 * A group row's actions below 1024px, where the row shows only "⋯" (mobile list plan): the buttons of the wide row in
 * the same order, each with its own permission and disabled state, the menu kept on screen. Its items come from
 * groupRowActions, so the row knows whether "⋯" has anything to offer.
 */
import { computed, ref, watch } from 'vue'
import { useResizeObserver, useWindowSize } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useOverlayLayer } from '@/composables/useOverlayLayer'
import type { GroupRowAction, GroupRowActions } from './groupRowActions'

const props = defineProps<{
  show: boolean
  group: { id: number; name: string } | null
  anchorRect: DOMRect | null
  actions: GroupRowActions
  duplicating?: boolean
}>()
const emit = defineEmits<{ close: []; action: [action: GroupRowAction] }>()
const { t } = useI18n()

const ITEM = 'gmenu-item flex w-full items-center gap-2 px-4 py-2 text-left text-sm text-gray-700 hover:bg-surface-popover-hover dark:text-gray-200'
const DANGER = '!text-status-error-text hover:!bg-surface-popover-danger-hover focus-visible:!bg-surface-popover-danger-hover'
const ICON = 'shrink-0 text-gray-400 dark:text-gray-500'

function pick(action: GroupRowAction) {
  emit('action', action)
  emit('close')
}

const menuRef = ref<HTMLElement | null>(null)
const rootRef = ref<HTMLElement | null>(null)
const { width: viewportWidth, height: viewportHeight } = useWindowSize()
const PAD = 8
/** The page header's bottom: the menu never covers it, and scrolls inside when taller than the room under it. */
const HEADER = 65
const position = ref({ top: HEADER, left: PAD })
const menuStyle = computed(() => ({
  top: `${position.value.top}px`,
  left: `${position.value.left}px`,
  maxWidth: `${Math.max(0, viewportWidth.value - PAD * 2)}px`,
  maxHeight: `${Math.max(0, viewportHeight.value - HEADER - PAD)}px`,
}))
const place = () => {
  if (!menuRef.value || !props.anchorRect) return
  const { width, height } = menuRef.value.getBoundingClientRect()
  const anchor = props.anchorRect
  const gap = 4
  const maxTop = viewportHeight.value - height - PAD
  const top = anchor.bottom + gap <= maxTop ? anchor.bottom + gap : anchor.top - height - gap
  // Fields, not a new object: an unchanged place must not render the menu again (and measure it again).
  position.value.top = Math.max(HEADER, Math.min(top, maxTop))
  position.value.left = Math.max(PAD, Math.min(anchor.right - width, viewportWidth.value - width - PAD))
}
watch([menuRef, () => props.anchorRect, viewportWidth, viewportHeight], place, { flush: 'post' })
useResizeObserver(menuRef, place)

const layer = useOverlayLayer(() => props.show && !!props.group && !!props.anchorRect, {
  root: rootRef,
  modal: false,
  floor: 9998,
  onEscape: () => emit('close'),
})
</script>

<style scoped>
/* Touch: every item 40px high; stacked without gaps they cannot reach out unseen. */
@media (pointer: coarse) {
  .gmenu-item {
    min-height: 40px;
  }
}
</style>
