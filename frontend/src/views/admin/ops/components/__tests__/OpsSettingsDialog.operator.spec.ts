import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { MeAccess } from '@/types/adminAccess'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)

import OpsSettingsDialog from '../OpsSettingsDialog.vue'
const { opsAPI, showError } = vi.hoisted(() => ({
  opsAPI: {
    getAlertRuntimeSettings: vi.fn(), getEmailNotificationConfig: vi.fn(),
    getAdvancedSettings: vi.fn(), getMetricThresholds: vi.fn(),
    updateAlertRuntimeSettings: vi.fn(), updateEmailNotificationConfig: vi.fn(),
    updateAdvancedSettings: vi.fn(), updateMetricThresholds: vi.fn(),
  }, showError: vi.fn(),
}))
vi.mock('@/api/admin/ops', () => ({ opsAPI }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))

function seedOperator() {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { ops: 'manage' },
    flags: [],
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
}

async function openDialog() {
  const wrapper = shallowMount(OpsSettingsDialog, {
    props: { show: false },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('ops settings for an operator', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    opsAPI.getAlertRuntimeSettings.mockResolvedValue({ evaluation_interval_seconds: 30 })
    opsAPI.getEmailNotificationConfig.mockResolvedValue({
      alert: { enabled: true, recipients: [], min_severity: 'warning' },
      report: {
        enabled: false,
        recipients: [],
        daily_summary_enabled: false,
        daily_summary_schedule: '0 9 * * *',
        weekly_summary_enabled: false,
        weekly_summary_schedule: '0 9 * * 1'
      }
    })
    opsAPI.getAdvancedSettings.mockResolvedValue({
      data_retention: {
        cleanup_enabled: false,
        cleanup_schedule: '0 3 * * *',
        error_log_retention_days: 7,
        minute_metrics_retention_days: 7,
        hourly_metrics_retention_days: 7
      },
      aggregation: { aggregation_enabled: true },
      openai_account_quota_auto_pause: { default_threshold_5h: 0, default_threshold_7d: 0 },
      ignore_count_tokens_errors: false,
      ignore_context_canceled: false,
      ignore_no_available_accounts: false,
      ignore_insufficient_balance_errors: false,
      auto_refresh_enabled: false,
      auto_refresh_interval_seconds: 30,
      display_alert_events: true,
      display_openai_token_stats: true
    })
    opsAPI.getMetricThresholds.mockResolvedValue({ sla_percent_min: 98 })
    opsAPI.updateAlertRuntimeSettings.mockResolvedValue({})
    opsAPI.updateMetricThresholds.mockResolvedValue({})
    seedOperator()
  })

  it('saves only the alert runtime and thresholds for an operator', async () => {
    const wrapper = await openDialog()
    expect(wrapper.get('[data-test="ops-email-settings"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="ops-advanced-settings"]').attributes('disabled')).toBeDefined()

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(opsAPI.updateAlertRuntimeSettings).toHaveBeenCalledWith(expect.objectContaining({ evaluation_interval_seconds: 30 }))
    expect(opsAPI.updateMetricThresholds).toHaveBeenCalledWith(expect.objectContaining({ sla_percent_min: 98 }))
    expect(opsAPI.updateEmailNotificationConfig).not.toHaveBeenCalled()
    expect(opsAPI.updateAdvancedSettings).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })
})
