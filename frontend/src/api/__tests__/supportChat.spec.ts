import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import {
  fetchAdminChatImage,
  fetchMyChatImage,
  getAdminChatSummary,
  listAdminChatMessages,
  listMyChatMessages,
  newSupportChatClientId,
  sendAdminChatMessage,
  sendMyChatMessage,
  setAdminChatBlocked,
  setAdminChatStatus,
} from '../supportChat'

afterEach(() => vi.restoreAllMocks())

describe('support chat api', () => {
  it('sends text as JSON and images as multipart with a long timeout', async () => {
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { message: { id: 1 }, duplicate: false } })

    await sendMyChatMessage({ body: 'hi', client_msg_id: 'abcdefgh' })
    expect(post).toHaveBeenLastCalledWith('/support-chat/messages', { body: 'hi', client_msg_id: 'abcdefgh' })

    const image = new Blob(['x'], { type: 'image/webp' })
    await sendAdminChatMessage(7, { body: '', client_msg_id: 'abcdefgh', image })
    const [url, form, config] = post.mock.calls[1] as [string, FormData, Record<string, unknown>]
    expect(url).toBe('/admin/support-chats/7/messages')
    expect(form).toBeInstanceOf(FormData)
    expect(form.get('client_msg_id')).toBe('abcdefgh')
    expect(form.get('body')).toBe('')
    expect((form.get('image') as File).name).toBe('image.webp')
    expect(config).toEqual({ headers: { 'Content-Type': 'multipart/form-data' }, timeout: 120000 })
  })

  it('pages with after_id/before_id and reads images as blobs', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: {} })
    await listMyChatMessages({ after_id: 5, limit: 50 })
    expect(get).toHaveBeenLastCalledWith('/support-chat/messages', { params: { after_id: 5, limit: 50 } })
    await listAdminChatMessages(3, { before_id: 9 })
    expect(get).toHaveBeenLastCalledWith('/admin/support-chats/3/messages', { params: { before_id: 9 } })
    await fetchMyChatImage(11)
    expect(get).toHaveBeenLastCalledWith('/support-chat/images/11', { responseType: 'blob' })
    await fetchAdminChatImage(12)
    expect(get).toHaveBeenLastCalledWith('/admin/support-chats/images/12', { responseType: 'blob' })
    await getAdminChatSummary()
    expect(get).toHaveBeenLastCalledWith('/admin/support-chats/summary')
  })

  it('sends the last seen user message when resolving and the reason only when blocking', async () => {
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: {} })
    await setAdminChatStatus(4, 'resolved', 31)
    expect(post).toHaveBeenLastCalledWith('/admin/support-chats/4/status', { status: 'resolved', last_seen_message_id: 31 })
    await setAdminChatBlocked(4, true, 'spam')
    expect(post).toHaveBeenLastCalledWith('/admin/support-chats/4/block', { blocked: true, reason: 'spam' })
    await setAdminChatBlocked(4, false)
    expect(post).toHaveBeenLastCalledWith('/admin/support-chats/4/block', { blocked: false })
  })

  it('makes client message ids the server accepts', () => {
    const ids = new Set(Array.from({ length: 20 }, () => newSupportChatClientId()))
    expect(ids.size).toBe(20)
    for (const id of ids) expect(id).toMatch(/^[A-Za-z0-9_-]{8,64}$/)
  })
})
