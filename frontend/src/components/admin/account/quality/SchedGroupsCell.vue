<template>
  <span v-if="loading && !summary" class="skeleton inline-block h-3.5 w-10 align-middle" />
  <span v-else-if="!capsules.length" class="sq-text-3">—</span>
  <span v-else class="flex flex-nowrap items-center gap-1.5 max-md:flex-wrap max-md:justify-end" data-sq-groups>
    <button
      v-for="item in capsules.slice(0, MAX_SHOWN)"
      :key="item.key"
      type="button"
      :class="['sq-capsule sq-focus', item.exempt ? 'sq-capsule--manual' : 'sq-capsule--cool', { 'sq-capsule--all': item.groupId === 0 }]"
      :data-sq-capsule="item.key"
      @click.stop="emit('open', { groupId: item.groupId || null, model: item.model })"
    >
      <span class="sq-capsule__name">{{ item.name }}</span> <span class="sq-capsule__state">{{ item.state }}</span>
    </button>
    <button
      v-if="capsules.length > MAX_SHOWN"
      type="button"
      class="sq-plain sq-focus sq-text-3 text-xs"
      data-sq-more
      @click.stop="emit('open', { groupId: null, model: null })"
    >{{ t('admin.schedQuality.more', { n: capsules.length - MAX_SHOWN }) }}</button>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SchedAccountSummary } from '@/types/schedQuality'
import { fmtAt } from '@/utils/schedQualityFormat'

// Groups column of the account list (FINAL §2, §7): one red capsule per substandard group and model,
// `name 不达标·首字 · 12:30` with the first triggered dimension (contract §2.5; the numbers are red for all), then one
// capsule per exemption. The model joins the name when the account is
// substandard on more than one model. At most 3, then +N; a click opens the drawer at that group.
const props = defineProps<{ summary?: SchedAccountSummary; loading?: boolean; groupName: (id: number) => string }>()
const emit = defineEmits<{ open: [target: { groupId: number | null; model: string | null }] }>()
const { t } = useI18n()
const MAX_SHOWN = 3

interface Capsule { key: string; groupId: number; model: string | null; name: string; state: string; exempt: boolean }

const capsules = computed<Capsule[]>(() => {
  const s = props.summary
  if (!s) return []
  const now = Date.now()
  const live = s.substandard.filter((item) => !item.exempt)
  const manyModels = new Set(live.map((item) => item.model)).size > 1
  const items: Capsule[] = live
    .slice()
    .sort((a, b) => Date.parse(a.until) - Date.parse(b.until))
    .map((item) => ({
      key: `${item.group_id}:${item.model}`,
      groupId: item.group_id,
      model: item.model,
      name: manyModels ? `${props.groupName(item.group_id)} ${item.model}` : props.groupName(item.group_id),
      state: `${t('admin.schedQuality.state.substandard')}·${t(`admin.schedQuality.dim.${item.dims[0]}`)} · ${fmtAt(item.until, now)}`,
      exempt: false
    }))
  for (const exemption of s.exemptions) {
    items.push({
      key: `exempt:${exemption.group_id}`,
      groupId: exemption.group_id,
      model: null,
      name: exemption.group_id === 0 ? t('admin.schedQuality.override.allGroups') : props.groupName(exemption.group_id),
      state: t('admin.schedQuality.state.exempted'),
      exempt: true
    })
  }
  return items
})
</script>
