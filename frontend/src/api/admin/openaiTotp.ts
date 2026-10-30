import { apiClient } from '../client'

export type OpenAITotpTaskState = 'queued' | 'running' | 'enrolling' | 'prepared' | 'activating' | 'verifying' | 'uncertain' | 'succeeded' | 'failed' | 'ready' | 'imported'

export interface OpenAITotpTask {
  task_id: number
  account_id?: number
  state: OpenAITotpTaskState
  action: string
  error_code?: string
  has_candidate?: boolean
}

export interface OpenAITotpStatus {
  configured: boolean
  email: string
  revision: number
  worker_ready: boolean
  locked: boolean
  task: OpenAITotpTask | null
}

export interface OpenAILoginCredentials {
  email: string
  password: string
  totp_secret: string
}

export interface OpenAILoginRequest extends OpenAILoginCredentials {
  name?: string
  proxy_id?: number
  group_ids?: number[]
}

export type OpenAITotpExportSource = 'current' | 'candidate' | 'previous'

export interface OpenAITotpExport {
  email: string
  password?: string
  secret: string
  otpauth_uri: string
  state: string
  source: OpenAITotpExportSource
}

const accountPath = (id: number) => `/admin/accounts/${id}/openai-totp`
const loginPath = '/admin/accounts/openai-login'

// The shared client's 401 handler can replay requests after refreshing a token.
// These writes must only be repeated by explicit user action (or a step-up grant).
const writeOptions = (signal?: AbortSignal) => ({ signal, _retry: true })

export const openaiTotpAPI = {
  async status(id: number, signal?: AbortSignal): Promise<OpenAITotpStatus> {
    return (await apiClient.get<OpenAITotpStatus>(accountPath(id), { signal })).data
  },
  async save(id: number, credentials: OpenAILoginCredentials & { revision: number }, signal?: AbortSignal): Promise<OpenAITotpStatus> {
    return (await apiClient.put<OpenAITotpStatus>(accountPath(id), credentials, writeOptions(signal))).data
  },
  async rotate(id: number, signal?: AbortSignal): Promise<OpenAITotpTask> {
    return (await apiClient.post<OpenAITotpTask>(`${accountPath(id)}/rotate`, {}, writeOptions(signal))).data
  },
  async reconcile(id: number, taskID: number, action: 'verify_new' | 'verify_old', signal?: AbortSignal): Promise<OpenAITotpTask | null> {
    return (await apiClient.post<OpenAITotpTask | null>(`${accountPath(id)}/reconcile`, { task_id: taskID, action }, writeOptions(signal))).data
  },
  async export(id: number, source: OpenAITotpExportSource, includePassword: boolean, signal?: AbortSignal): Promise<OpenAITotpExport> {
    return (await apiClient.post<OpenAITotpExport>(`${accountPath(id)}/export`, { source, include_password: includePassword }, writeOptions(signal))).data
  },
  async login(credentials: OpenAILoginRequest, signal?: AbortSignal): Promise<OpenAITotpTask> {
    return (await apiClient.post<OpenAITotpTask>(loginPath, credentials, writeOptions(signal))).data
  },
  async loginTask(taskID: number, signal?: AbortSignal): Promise<OpenAITotpTask> {
    return (await apiClient.get<OpenAITotpTask>(`${loginPath}/${taskID}`, { signal })).data
  },
  async importLogin(taskID: number, signal?: AbortSignal): Promise<{ account_id: number }> {
    return (await apiClient.post<{ account_id: number }>(`${loginPath}/${taskID}/import`, {}, writeOptions(signal))).data
  }
}
