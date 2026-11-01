/**
 * Support chat ("在线客服") — one long-lived conversation per user with support staff.
 * Every call answers SUPPORT_CHAT_DISABLED (403) unless both the ticket switch and
 * the chat switch are on. Types follow the backend contract; keep them here (not in
 * types/index.ts) so the chat stays one self-contained module.
 */

import { apiClient } from './client'
import type { PaginatedResponse } from '@/types'

export type SupportChatAuthorRole = 'user' | 'admin' | 'system' | 'bot'
export type SupportChatStatus = 'open' | 'resolved'
export type SupportChatAdminFilter = 'waiting' | 'open' | 'resolved' | 'all'

export const SUPPORT_CHAT_BODY_MAX = 5000
export const SUPPORT_CHAT_WELCOME_MAX = 300
export const SUPPORT_CHAT_BLOCK_REASON_MAX = 200
export const SUPPORT_CHAT_IMAGE_MAX_BYTES = 1572864
export const SUPPORT_CHAT_RETENTION_DEFAULT_DAYS = 90
export const SUPPORT_CHAT_RETENTION_MIN_DAYS = 7
export const SUPPORT_CHAT_RETENTION_MAX_DAYS = 3650
export const SUPPORT_CHAT_PAGE_SIZE = 30
export const SUPPORT_CHAT_PAGE_SIZE_MAX = 50

export interface SupportChatImageRef {
  id: number
  content_type: string
  width: number
  height: number
  byte_size: number
  purged: boolean
}

export interface SupportChatMessage {
  id: number
  author_role: SupportChatAuthorRole
  /** '' when deleted. */
  body: string
  /** null when deleted or text-only. */
  image: SupportChatImageRef | null
  deleted: boolean
  /** Deleted by its own side: a user recall, or staff removing a staff message. */
  recalled?: boolean
  client_msg_id?: string
  created_at: string
  /** Admin views only, staff messages only. */
  author_name?: string
  /** Staff messages only: the staff account id, the only staff detail users see. */
  staff_id?: number
}

export interface SupportChatThreadPage {
  /** The chat this page belongs to (the requested chat on the admin side; 0 when the user has no chat). */
  chat_id?: number
  /** Always ascending by id. */
  items: SupportChatMessage[]
  /** after_id: newer remain; before_id or none: older remain. */
  has_more: boolean
  status: SupportChatStatus
  blocked: boolean
  /** The caller side's read cursor. */
  read_message_id: number
  /** The other side's read cursor (for 已读 receipts). */
  peer_read_message_id: number
  content_version: number
  /** User side only (0 for admin). */
  unread_count: number
}

export interface SupportChatThreadQuery {
  after_id?: number
  before_id?: number
  limit?: number
}

export interface SupportChatUserChat {
  id: number
  status: SupportChatStatus
  blocked: boolean
  waiting: boolean
  last_message_id: number
  read_message_id: number
  peer_read_message_id: number
  content_version: number
}

export interface SupportChatUserSummary {
  chat: SupportChatUserChat | null
  unread_count: number
  ticket_unread_count: number
  welcome: string
}

export interface SupportChatSendRequest {
  body: string
  client_msg_id: string
  /** Already compressed by compressImage(). */
  image?: Blob | null
}

export interface SupportChatSendResult {
  message: SupportChatMessage
  duplicate: boolean
  /** The chat the message went into (user side). */
  chat_id?: number
}

export interface SupportChatUserCard {
  id: number
  email: string
  username: string
  status: string
  deleted: boolean
  /** Stripped for operators. */
  balance?: number
  /** Stripped for operators. */
  created_at?: string
}

export interface SupportChatAdminItem {
  id: number
  user_id: number
  status: SupportChatStatus
  waiting_since: string | null
  last_message_at: string
  last_message_role: SupportChatAuthorRole
  last_message_id: number
  /** Up to 80 runes of the last message body; '' for image-only, deleted or none. */
  preview: string
  preview_kind: 'text' | 'image' | 'deleted' | 'recalled' | 'none'
  unread: boolean
  message_count: number
  blocked: boolean
  blocked_reason: string
  resolved_at: string | null
  revision: number
  content_version: number
  /** Staff cursor. */
  read_message_id: number
  /** User cursor. */
  peer_read_message_id: number
  user: SupportChatUserCard
}

/** Counts only; polled by the staff alerts and never audited. */
export interface SupportChatAdminSummary {
  chat_waiting: number
  ticket_pending: number
  latest_user_message_id: number
  /** SUM(revision) over all chats; changes whenever any chat row changes. */
  version: number
}

const CLIENT_MSG_ID_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'

/** Idempotency key for one message; retries reuse it. Matches ^[A-Za-z0-9_-]{8,64}$. */
export function newSupportChatClientId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  let id = 'c'
  for (let i = 0; i < 31; i++) id += CLIENT_MSG_ID_ALPHABET[Math.floor(Math.random() * CLIENT_MSG_ID_ALPHABET.length)]
  return id
}

function imageFileName(type: string): string {
  if (type === 'image/png') return 'image.png'
  if (type === 'image/jpeg') return 'image.jpg'
  return 'image.webp'
}

/** Text goes as JSON; an image goes as multipart like the plugin upload. */
async function postMessage(url: string, request: SupportChatSendRequest): Promise<SupportChatSendResult> {
  if (request.image) {
    const form = new FormData()
    form.append('body', request.body)
    form.append('client_msg_id', request.client_msg_id)
    form.append('image', request.image, imageFileName(request.image.type))
    const { data } = await apiClient.post<SupportChatSendResult>(url, form, {
      headers: { 'Content-Type': 'multipart/form-data' },
      timeout: 120000
    })
    return data
  }
  const { data } = await apiClient.post<SupportChatSendResult>(url, { body: request.body, client_msg_id: request.client_msg_id })
  return data
}

async function getBlob(url: string): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(url, { responseType: 'blob' })
  return data
}

// ---- users ----

export async function getMyChat(): Promise<SupportChatUserSummary> {
  const { data } = await apiClient.get<SupportChatUserSummary>('/support-chat')
  return data
}

export async function listMyChatMessages(params: SupportChatThreadQuery = {}): Promise<SupportChatThreadPage> {
  const { data } = await apiClient.get<SupportChatThreadPage>('/support-chat/messages', { params })
  return data
}

export function sendMyChatMessage(request: SupportChatSendRequest): Promise<SupportChatSendResult> {
  return postMessage('/support-chat/messages', request)
}

export async function markMyChatRead(lastMessageId: number): Promise<{ read_message_id: number }> {
  const { data } = await apiClient.post<{ read_message_id: number }>('/support-chat/read', { last_message_id: lastMessageId })
  return data
}

/** Users can recall their own message for SUPPORT_CHAT_RECALL_WINDOW_MS after sending it. */
export const SUPPORT_CHAT_RECALL_WINDOW_MS = 2 * 60 * 1000

export async function recallMyChatMessage(messageId: number): Promise<{ message: SupportChatMessage }> {
  const { data } = await apiClient.delete<{ message: SupportChatMessage }>(`/support-chat/messages/${messageId}`)
  return data
}

export function fetchMyChatImage(id: number): Promise<Blob> {
  return getBlob(`/support-chat/images/${id}`)
}

// ---- admins ----

export async function getAdminChatSummary(): Promise<SupportChatAdminSummary> {
  const { data } = await apiClient.get<SupportChatAdminSummary>('/admin/support-chats/summary')
  return data
}

export async function listAdminChats(params: {
  filter?: SupportChatAdminFilter
  keyword?: string
  page: number
  page_size: number
}): Promise<PaginatedResponse<SupportChatAdminItem>> {
  const { data } = await apiClient.get<PaginatedResponse<SupportChatAdminItem>>('/admin/support-chats', { params })
  return data
}

export async function getAdminChat(id: number): Promise<SupportChatAdminItem> {
  const { data } = await apiClient.get<SupportChatAdminItem>(`/admin/support-chats/${id}`)
  return data
}

export async function listAdminChatMessages(id: number, params: SupportChatThreadQuery = {}): Promise<SupportChatThreadPage> {
  const { data } = await apiClient.get<SupportChatThreadPage>(`/admin/support-chats/${id}/messages`, { params })
  return data
}

export async function markAdminChatRead(id: number, lastMessageId: number): Promise<{ read_message_id: number }> {
  const { data } = await apiClient.post<{ read_message_id: number }>(`/admin/support-chats/${id}/read`, { last_message_id: lastMessageId })
  return data
}

export function fetchAdminChatImage(id: number): Promise<Blob> {
  return getBlob(`/admin/support-chats/images/${id}`)
}

export function sendAdminChatMessage(id: number, request: SupportChatSendRequest): Promise<SupportChatSendResult> {
  return postMessage(`/admin/support-chats/${id}/messages`, request)
}

/** Resolving sends the newest user message the staff member has seen; 409 SUPPORT_CHAT_CHANGED when a newer one arrived. */
export async function setAdminChatStatus(id: number, status: SupportChatStatus, lastSeenMessageId: number): Promise<SupportChatAdminItem> {
  const { data } = await apiClient.post<SupportChatAdminItem>(`/admin/support-chats/${id}/status`, { status, last_seen_message_id: lastSeenMessageId })
  return data
}

export async function setAdminChatBlocked(id: number, blocked: boolean, reason = ''): Promise<SupportChatAdminItem> {
  const { data } = await apiClient.post<SupportChatAdminItem>(`/admin/support-chats/${id}/block`, blocked ? { blocked, reason } : { blocked })
  return data
}

export async function deleteAdminChatMessage(id: number, messageId: number): Promise<{ message: SupportChatMessage }> {
  const { data } = await apiClient.delete<{ message: SupportChatMessage }>(`/admin/support-chats/${id}/messages/${messageId}`)
  return data
}

export async function deleteAdminChat(id: number): Promise<void> {
  await apiClient.delete(`/admin/support-chats/${id}`)
}
