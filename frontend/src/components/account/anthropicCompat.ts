// Claude Code 请求兼容的账号 extra 读写，创建、编辑、批量编辑三个弹窗共用。
// null 只出现在批量编辑，表示「不修改」，对应的键不写。

export interface AnthropicCompatDraft {
  enabled: boolean | null
  newSessions: boolean | null
}

export function anthropicCompatDraftFromExtra(extra?: Record<string, unknown> | null): AnthropicCompatDraft {
  return {
    enabled: extra?.anthropic_compat_profile === 'claude_code',
    newSessions: extra?.anthropic_compat_new_sessions !== false
  }
}

export function applyAnthropicCompatExtra(extra: Record<string, unknown>, draft: AnthropicCompatDraft): void {
  if (draft.enabled !== null) extra.anthropic_compat_profile = draft.enabled ? 'claude_code' : 'off'
  // 关闭兼容时「接收新会话」没有意义，不写。
  if (draft.enabled !== false && draft.newSessions !== null) extra.anthropic_compat_new_sessions = draft.newSessions
}
