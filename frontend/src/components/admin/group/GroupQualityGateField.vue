<template>
  <div ref="root" class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-400" data-testid="group-quality-gate">
    <div class="mb-3 flex items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.groupQualityGate.title') }}</h4>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groupQualityGate.hint') }}</p>
      </div>
      <Toggle
        v-if="canViewAccounts"
        data-testid="quality-gate-toggle"
        :aria-label="t('admin.groupQualityGate.enable')"
        :model-value="form.enabled"
        :disabled="loadState !== 'ready'"
        class="disabled:cursor-not-allowed disabled:opacity-50"
        @update:model-value="setEnabled"
      />
    </div>

    <p v-if="!canViewAccounts" data-testid="quality-gate-no-access" class="text-xs text-gray-500 dark:text-gray-400">
      {{ t('admin.groupQualityGate.needsAccountView') }}
    </p>
    <p v-else-if="loadState === 'loading'" class="text-xs text-gray-500 dark:text-gray-400">
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
            :step="field.step"
            :min="field.min"
            :max="field.max"
            class="input"
            :class="{ 'input-error': visibleErrors[field.key] || rejected?.field === field.key }"
            :aria-invalid="visibleErrors[field.key] || rejected?.field === field.key ? 'true' : undefined"
            :aria-describedby="`${uid}-${field.key}-note`"
            :data-testid="`quality-gate-${field.key}`"
            @blur="touched.add(field.key)"
          />
          <p v-if="visibleErrors[field.key]" :id="`${uid}-${field.key}-note`" class="input-error-text" role="alert">
            {{ t(`admin.groupQualityGate.${visibleErrors[field.key]}`) }}
          </p>
          <p v-else-if="rejected?.field === field.key" :id="`${uid}-${field.key}-note`" class="input-error-text" role="alert">
            {{ rejected.message }}
          </p>
          <p v-else :id="`${uid}-${field.key}-note`" class="input-hint">{{ t(field.hint) }}</p>
        </div>
      </div>

      <div class="mt-3 flex items-start justify-between gap-3 rounded-lg bg-gray-50 px-3 py-2.5 dark:bg-dark-700/60">
        <div class="min-w-0">
          <p class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.groupQualityGate.strict') }}</p>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groupQualityGate.strictHint') }}</p>
        </div>
        <Toggle v-model="form.strict" data-testid="quality-gate-strict" :aria-label="t('admin.groupQualityGate.strict')" />
      </div>
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
import { useAdminPermission } from '@/composables/useAdminPermission'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useStateMotion } from '@/composables/useStateMotion'
import type { GroupQualityGatePolicy } from '@/types/groupQualityGate'
import {
  QUALITY_GATE_LIMITS,
  qualityGateFormToV3,
  qualityGatePolicyToForm,
  qualityGatePolicyWithV3,
  qualityGateRejectedField,
  qualityGateSaveFailureKey,
  qualityGateV3Equal,
  validateQualityGateForm,
  type QualityGateField,
  type QualityGateFormState
} from '@/views/admin/groupsQualityGate'

// 分组编辑框里的“质量门槛”区块（只对 OpenAI 分组显示）。门槛策略与分组分开保存：
// 父组件先存分组，再调用 save()；失败只提示“分组已保存，门槛未保存”，不抛错，表单保留给用户修正后再存。
// 读策略需要账号查看权限，没有时不发请求，只显示一行说明。
// 只编辑 v3 子对象；v2 字段（含旧的熔断线、快档线与固定账号）不展示，保存时按读到的原样回传。
const { ttftSeconds, errorRate, cacheKeepRate } = QUALITY_GATE_LIMITS
const FIELDS: { key: QualityGateField; label: string; hint: string; min: number; max: number; step: string }[] = [
  { key: 'max_ttft_p90_seconds', label: 'admin.groupQualityGate.maxTtft', hint: 'admin.groupQualityGate.maxTtftHint', min: ttftSeconds.min, max: ttftSeconds.max, step: '1' },
  { key: 'max_error_rate_percent', label: 'admin.groupQualityGate.maxErrorRate', hint: 'admin.groupQualityGate.maxErrorRateHint', min: errorRate.min * 100, max: errorRate.max * 100, step: 'any' },
  { key: 'min_cache_keep_rate_percent', label: 'admin.groupQualityGate.minCacheKeep', hint: 'admin.groupQualityGate.minCacheKeepHint', min: cacheKeepRate.min * 100, max: cacheKeepRate.max * 100, step: 'any' }
]

const props = defineProps<{ groupId: number }>()
const emit = defineEmits<{
  saved: [groupId: number, version: number, policy: GroupQualityGatePolicy]
}>()

const { t } = useI18n()
const appStore = useAppStore()
const { canView: canViewAccounts } = useAdminPermission('accounts')
const uid = useId()

const root = ref<HTMLElement>()
const fields = ref<HTMLElement>()
const playMotion = useStateMotion(root)

const loadState = ref<'loading' | 'ready' | 'failed'>('loading')
const loadError = ref('')
let version = 0
let stored: GroupQualityGatePolicy | undefined
let loaded = qualityGatePolicyToForm(null)
const form = reactive<QualityGateFormState>(qualityGatePolicyToForm(null))

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

// 上次保存被后端 400 拒绝的字段与原因，改动表单后清除。
const rejected = ref<{ field: QualityGateField; message: string } | null>(null)
watch(form, () => { rejected.value = null }, { deep: true })

const accept = (policy: GroupQualityGatePolicy | undefined) => {
  stored = policy
  loaded = qualityGatePolicyToForm(policy)
}

async function readPolicy() {
  const policies = await adminAPI.groupQualityGate.getPolicies()
  version = policies.version
  accept(policies.groups[String(props.groupId)])
}

async function load() {
  loadState.value = 'loading'
  try {
    await readPolicy()
    Object.assign(form, loaded)
    loadState.value = 'ready'
  } catch (error) {
    loadError.value = extractApiErrorMessage(error, t('common.unknownError'))
    loadState.value = 'failed'
  }
}

// 关闭只改开关：数值回到打开对话框时读到的配置，免得把关着的无效值写回去。
async function setEnabled(enabled: boolean) {
  if (!enabled) {
    Object.assign(form, loaded, { enabled: false })
    touched.clear()
    return
  }
  form.enabled = true
  await nextTick()
  const el = fields.value
  if (el) playMotion(() => gsap.from(el, { autoAlpha: 0, y: -4, duration: 0.18, ease: 'expo.out', clearProps: 'opacity,visibility,transform' }))
}

onMounted(() => {
  if (canViewAccounts.value) void load()
})

/** 保存分组前调用；没读到配置时不拦截（也不会保存）。 */
function validate(): boolean {
  submitted.value = true
  return loadState.value !== 'ready' || Object.keys(errors.value).length === 0
}

/**
 * 分组保存成功后调用；没有改动不发请求。失败时提示并返回 false，表单保留：
 * 409 重新读取版本（输入不动），用户确认后再存；400 标出 metadata.field 对应的字段。
 */
async function save(groupId: number): Promise<boolean> {
  if (loadState.value !== 'ready') return true
  if (qualityGateV3Equal(qualityGateFormToV3(form), qualityGateFormToV3(loaded))) return true
  try {
    const result = await adminAPI.groupQualityGate.updatePolicy(groupId, {
      expected_version: version,
      policy: qualityGatePolicyWithV3(stored, form)
    })
    version = result.version
    accept(result.policy)
    emit('saved', groupId, result.version, result.policy)
    return true
  } catch (error) {
    let failure = error
    if (qualityGateSaveFailureKey(error) === 'groupSavedGateConflict') {
      await readPolicy().catch((readError) => { failure = readError })
    }
    const message = extractApiErrorMessage(failure, t('common.unknownError'))
    const field = qualityGateRejectedField(failure)
    rejected.value = field ? { field, message } : null
    appStore.showWarning(t(`admin.groupQualityGate.${qualityGateSaveFailureKey(failure)}`, { message }))
    return false
  }
}

defineExpose({ validate, save })
</script>
