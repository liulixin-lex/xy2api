import type { GroupSystemPromptConfig } from '@/types'

export function cloneGroupSystemPromptConfig(config?: GroupSystemPromptConfig): GroupSystemPromptConfig {
  return {
    prompt: config?.prompt ?? '',
    scope: config?.scope ?? 'all',
    models: [...(config?.models ?? [])],
    model_prompts: { ...(config?.model_prompts ?? {}) }
  }
}
