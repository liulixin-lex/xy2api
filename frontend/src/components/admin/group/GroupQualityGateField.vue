<template>
  <div ref="root" class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-400" data-testid="group-quality-gate">
    <div class="mb-3 flex items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.groupQualityGate.title') }}</h4>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groupQualityGate.hint') }}</p>
      </div>
      <Toggle
        data-testid="quality-gate-toggle"
        :aria-label="t('admin.groupQualityGate.enable')"
        :model-value="form.enabled"
        :disabled="loadState !== 'ready'"
        class="disabled:cursor-not-allowed disabled:opacity-50"
        @update:model-value="setEnabled"
      />
    </div>

    <p v-if="loadState === 'loading'" class="text-xs text-gray-500 dark:text-gray-400">
      {{ t('admin.groupQualityGate.loading') }}
    </p>
    <p v-else-if="loadState === 'failed'" role="alert" class="text-xs text-red-600 dark:text-red-400">
      {{ t('admin.groupQualityGate.loadFailed', { message: loadError }) }}
      <button type="button" class="ml-1 font-medium underline underline-offset-2 hover:text-red-700 dark:hover:text-red-300" @click="load">
        {{ t('admin.groupQualityGate.retry') }}
      </button>
    </p>
    <div v-else-if="form.enabled" ref="fields" data-testid="quality-gate-fields">
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <div v-for="field in FIELDS" :key="field.key">
          <label :for="`${uid}-${field.key}`" class="input-label">{{ t(field.label) }}</label>
          <input
            :id="`${uid}-${field.key}`"
            v-model.number="form[field.key]"
            type="number"
            step="any"
            :min="field.min"
            :max="field.max"
            class="input"
            :class="{ 'input-error': visibleErrors[field.key] }"
            :aria-invalid="visibleErrors[field.key] ? 'true' : undefined"
            :aria-describedby="`${uid}-${field.key}-note`"
            :data-testid="`quality-gate-${field.key}`"
            @blur="touched.add(field.key)"
          />
          <p v-if="visibleErrors[field.key]" :id="`${uid}-${field.key}-note`" class="input-error-text" role="alert">
            {{ t(`admin.groupQualityGate.${visibleErrors[field.key]}`) }}
          </p>
          <p v-else :id="`${uid}-${field.key}-note`" class="input-hint">{{ t(field.hint) }}</p>
        </div>
      </div>

      <fieldset class="mt-3">
        <legend class="input-label">{{ t('admin.groupQualityGate.pinned') }}</legend>
        <p v-if="membersState === 'loading'" class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.groupQualityGate.membersLoading') }}
        </p>
        <p v-else-if="membersState === 'failed'" role="alert" class="text-sm text-red-600 dark:text-red-400">
          {{ t('admin.groupQualityGate.membersFailed', { message: membersError }) }}
          <button type="button" class="ml-1 font-medium underline underline-offset-2 hover:text-red-700 dark:hover:text-red-300" @click="loadMembers">
            {{ t('admin.groupQualityGate.retry') }}
          </button>
        </p>
        <p v-else-if="!members.length" class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.groupQualityGate.membersEmpty') }}
        </p>
        <div v-else class="flex max-h-40 flex-wrap gap-2 overflow-y-auto" data-testid="quality-gate-members">
          <label
            v-for="account in members"
            :key="account.id"
            :class="[
              'inline-flex max-w-full cursor-pointer items-center gap-1.5 rounded-lg border px-2.5 py-1 text-sm transition-colors',
              form.pinned_account_ids.includes(account.id)
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-400 dark:bg-primary-900/20 dark:text-primary-300'
                : 'border-gray-200 text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700'
            ]"
          >
            <input
              v-model="form.pinned_account_ids"
              type="checkbox"
              :value="account.id"
              class="h-3.5 w-3.5 shrink-0 rounded border-gray-300 text-primary-500 focus:ring-primary-500 dark:border-dark-500"
            />
            <span class="truncate">{{ account.name }}</span>
            <span class="shrink-0 text-xs text-gray-500 dark:text-gray-400">#{{ account.id }}</span>
          </label>
        </div>
        <p class="input-hint">{{ t('admin.groupQualityGate.pinnedHint') }}</p>
      </fieldset>
    </div>
    <p v-else class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groupQualityGate.disabledHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import Toggle from '@/components/common/Toggle.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useStateMotion } from '@/composables/useStateMotion'
import type { GroupQualityGatePolicy } from '@/types/groupQualityGate'
import {
  qualityGateFormToPolicy,
  qualityGatePoliciesEqual,
  qualityGatePolicyToForm,
  qualityGateSaveFailureKey,
  validateQualityGateForm,
  type QualityGateField,
  type QualityGateFormState
} from '@/views/admin/groupsQualityGate'

// 分组编辑框里的“质量门槛”区块（只对 OpenAI 分组显示）。门槛策略与分组分开保存：
// 父组件先存分组，再调用 save()；失败只提示“分组已保存，门槛未保存”，不抛错。
// 原生 min/max 只放外边界、step 为 any，浏览器不会拦下 7.5 秒这类合法值，具体规则由 validateQualityGateForm 判定。
const FIELDS: { key: QualityGateField; label: string; hint: string; min: string; max: string }[] = [
  { key: 'max_error_rate_percent', label: 'admin.groupQualityGate.maxErrorRate', hint: 'admin.groupQualityGate.maxErrorRateHint', min: '0', max: '100' },
  { key: 'max_warm_ttft_p90_seconds', label: 'admin.groupQualityGate.maxWarmTtft', hint: 'admin.groupQualityGate.maxWarmTtftHint', min: '1', max: '600' },
  { key: 'fast_warm_ttft_p90_seconds', label: 'admin.groupQualityGate.fastWarmTtft', hint: 'admin.groupQualityGate.fastWarmTtftHint', min: '1', max: '600' }
]
const MEMBER_PAGE_SIZE = 1000

const props = defineProps<{ groupId: number }>()
const emit = defineEmits<{
  saved: [groupId: number, version: number, policy: GroupQualityGatePolicy]
}>()

const { t } = useI18n()
const appStore = useAppStore()
const uid = useId()

const root = ref<HTMLElement>()
const fields = ref<HTMLElement>()
const playMotion = useStateMotion(root)

const loadState = ref<'loading' | 'ready' | 'failed'>('loading')
const loadError = ref('')
let version = 0
let loaded = qualityGatePolicyToForm(null)
const form = reactive<QualityGateFormState>(qualityGatePolicyToForm(null))

const members = ref<{ id: number; name: string }[]>([])
const membersState = ref<'idle' | 'loading' | 'ready' | 'failed'>('idle')
const membersError = ref('')

const touched = reactive(new Set<QualityGateField>())
const submitted = ref(false)
const errors = computed(() => validateQualityGateForm(form))
const visibleErrors = computed(() => {
  const shown: Partial<Record<QualityGateField, string>> = {}
  for (const [key, value] of Object.entries(errors.value) as [QualityGateField, string][]) {
    if (submitted.value || touched.has(key)) shown[key] = value
  }
  return shown
})

const resetForm = (next: QualityGateFormState) => Object.assign(form, next, { pinned_account_ids: [...next.pinned_account_ids] })

async function load() {
  loadState.value = 'loading'
  try {
    const policies = await adminAPI.groupQualityGate.getPolicies()
    version = policies.version
    loaded = qualityGatePolicyToForm(policies.groups[String(props.groupId)])
    resetForm(loaded)
    loadState.value = 'ready'
  } catch (error) {
    loadError.value = extractApiErrorMessage(error, t('common.unknownError'))
    loadState.value = 'failed'
  }
}

async function loadMembers() {
  membersState.value = 'loading'
  try {
    const page = await adminAPI.accounts.list(1, MEMBER_PAGE_SIZE, { group: String(props.groupId), platform: 'openai', lite: '1' })
    members.value = page.items.map((account) => ({ id: account.id, name: account.name }))
    membersState.value = 'ready'
  } catch (error) {
    membersError.value = extractApiErrorMessage(error, t('common.unknownError'))
    membersState.value = 'failed'
  }
}

// 固定账号只列本组成员，门槛打开后才需要。
watch(() => form.enabled, (enabled) => {
  if (enabled && membersState.value === 'idle') void loadMembers()
})

// 关闭只改开关：数值与固定账号回到打开对话框时读到的配置，免得把关着的无效值写回去。
async function setEnabled(enabled: boolean) {
  if (!enabled) {
    resetForm({ ...loaded, enabled: false })
    touched.clear()
    return
  }
  form.enabled = true
  await nextTick()
  const el = fields.value
  if (el) playMotion(() => gsap.from(el, { autoAlpha: 0, y: -4, duration: 0.18, ease: 'expo.out', clearProps: 'opacity,visibility,transform' }))
}

onMounted(() => {
  void load()
})

/** 保存分组前调用；没读到配置时不拦截（也不会保存）。 */
function validate(): boolean {
  submitted.value = true
  return loadState.value !== 'ready' || Object.keys(errors.value).length === 0
}

/** 分组保存成功后调用；没有改动不发请求。失败时提示并返回 false。 */
async function save(groupId: number): Promise<boolean> {
  if (loadState.value !== 'ready') return true
  const memberIds = membersState.value === 'ready' ? new Set(members.value.map((account) => account.id)) : undefined
  const policy = qualityGateFormToPolicy(form, memberIds)
  if (qualityGatePoliciesEqual(policy, qualityGateFormToPolicy(loaded, memberIds))) return true
  try {
    const result = await adminAPI.groupQualityGate.updatePolicy(groupId, { expected_version: version, policy })
    version = result.version
    loaded = qualityGatePolicyToForm(result.policy)
    emit('saved', groupId, result.version, result.policy)
    return true
  } catch (error) {
    appStore.showWarning(t(`admin.groupQualityGate.${qualityGateSaveFailureKey(error)}`, {
      message: extractApiErrorMessage(error, t('common.unknownError'))
    }))
    return false
  }
}

defineExpose({ validate, save })
</script>
