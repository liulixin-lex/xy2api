import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import * as api from './api'
import { campaignErrorMessage } from './campaignFormat'
import type { Campaign } from './types'

/**
 * Pause / resume / cancel shared by the campaign list rows and the detail
 * dialog. Pause and resume apply at once; cancel asks for confirmation first.
 */
export function useCampaignActions(onChanged: (campaign: Campaign) => void) {
  const { t } = useI18n()
  const appStore = useAppStore()
  const busyId = ref<number | null>(null)
  const cancelTarget = ref<Campaign | null>(null)

  async function act(campaign: Campaign, call: (id: number) => Promise<Campaign>, successKey: string) {
    if (busyId.value !== null) return
    busyId.value = campaign.id
    try {
      const updated = await call(campaign.id)
      appStore.showSuccess(t(successKey))
      onChanged(updated)
    } catch (err) {
      appStore.showError(campaignErrorMessage(err, t))
    } finally {
      busyId.value = null
    }
  }

  const pause = (campaign: Campaign) => act(campaign, api.pauseCampaign, 'admin.emailCampaigns.actions.paused')
  const resume = (campaign: Campaign) => act(campaign, api.resumeCampaign, 'admin.emailCampaigns.actions.resumed')

  function requestCancel(campaign: Campaign) {
    cancelTarget.value = campaign
  }

  async function confirmCancel() {
    const target = cancelTarget.value
    cancelTarget.value = null
    if (target) await act(target, api.cancelCampaign, 'admin.emailCampaigns.actions.canceled')
  }

  return { busyId, cancelTarget, pause, resume, requestCancel, confirmCancel }
}
