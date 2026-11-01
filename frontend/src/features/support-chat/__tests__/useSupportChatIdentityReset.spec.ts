import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, reactive } from 'vue'
import { useSupportChatStore } from '@/stores/supportChat'
import { supportChatIdentityKey, useSupportChatIdentityReset } from '../useSupportChatIdentityReset'

const state = vi.hoisted(() => ({
  auth: null as null | { isAuthenticated: boolean; canAccessAdmin: boolean; user: { id: number; role: string; balance?: number } | null },
  app: null as null | { cachedPublicSettings: Record<string, unknown> | null },
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => state.app }))

function mountReset() {
  return mount(defineComponent({ setup() { useSupportChatIdentityReset(); return () => h('div') } }))
}

beforeEach(() => {
  state.auth = reactive({ isAuthenticated: true, canAccessAdmin: false, user: { id: 5, role: 'user', balance: 1 } })
  state.app = reactive({ cachedPublicSettings: { support_chat_enabled: true, support_ticket_enabled: true } })
})

describe('support chat identity reset', () => {
  it('builds a primitive key from id, role and the chat switch', () => {
    expect(supportChatIdentityKey(5, 'user', true)).toBe('5|user|true')
    expect(supportChatIdentityKey(undefined, undefined, undefined)).toBe('||')
  })

  it('does not reset when the user or settings object is replaced with the same values', async () => {
    const reset = vi.spyOn(useSupportChatStore(), 'reset')
    const wrapper = mountReset()
    // refreshUser() every 60s: a new object, same id and role (other fields may differ).
    state.auth!.user = { id: 5, role: 'user', balance: 2 }
    await nextTick()
    // fetchPublicSettings(true): a new object, chat switch unchanged.
    state.app!.cachedPublicSettings = { support_chat_enabled: true, support_ticket_enabled: false }
    await nextTick()
    expect(reset).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('resets once for each real change of id, role or the chat switch', async () => {
    const reset = vi.spyOn(useSupportChatStore(), 'reset')
    const wrapper = mountReset()
    state.auth!.user = { id: 6, role: 'user' }
    await nextTick()
    expect(reset).toHaveBeenCalledTimes(1)
    state.auth!.user = { id: 6, role: 'operator' }
    await nextTick()
    expect(reset).toHaveBeenCalledTimes(2)
    state.app!.cachedPublicSettings = { support_chat_enabled: false }
    await nextTick()
    expect(reset).toHaveBeenCalledTimes(3)
    state.auth!.user = null // logout
    await nextTick()
    expect(reset).toHaveBeenCalledTimes(4)
    wrapper.unmount()
  })
})
