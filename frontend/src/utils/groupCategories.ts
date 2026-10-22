import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'

// 与后端 service/group_category.go 的规则保持一致
export const MAX_GROUP_CATEGORIES = 50
export const MAX_GROUP_CATEGORY_NAME_LENGTH = 32

// unicode.IsControl：C0、DEL 与 C1 控制字符
const hasControlCharacter = (value: string) =>
  [...value].some((char) => {
    const code = char.codePointAt(0) ?? 0
    return code < 0x20 || (code >= 0x7f && code <= 0x9f)
  })

export const normalizeCategoryName = (raw: string): string => raw.trim()

export interface CategoryDraftError {
  /** i18n key under admin.groupCategories. */
  key: 'nameRequired' | 'nameTooLong' | 'nameInvalid' | 'nameDuplicate' | 'limitReached'
  /** Row of the offending item; -1 for the list as a whole. */
  index: number
}

/** Returns the first problem of a category list draft, or null when it can be saved. */
export function validateCategoryDraft(items: { name: string }[]): CategoryDraftError | null {
  if (items.length > MAX_GROUP_CATEGORIES) return { key: 'limitReached', index: -1 }
  const seen = new Set<string>()
  for (const [index, item] of items.entries()) {
    const name = normalizeCategoryName(item.name)
    if (!name) return { key: 'nameRequired', index }
    if ([...name].length > MAX_GROUP_CATEGORY_NAME_LENGTH) return { key: 'nameTooLong', index }
    if (hasControlCharacter(name)) return { key: 'nameInvalid', index }
    const folded = name.toLowerCase()
    if (seen.has(folded)) return { key: 'nameDuplicate', index }
    seen.add(folded)
  }
  return null
}

type Translate = (key: string, params?: Record<string, unknown>) => string

/** Turns a category API error into a message, preferring the localized known codes. */
export function groupCategoryErrorMessage(err: unknown, t: Translate, fallback: string): string {
  switch (extractApiErrorCode(err)) {
    case 'GROUP_CATEGORY_NAME_DUPLICATE':
      return t('admin.groupCategories.nameDuplicate')
    case 'GROUP_CATEGORY_LIMIT':
      return t('admin.groupCategories.limitReached')
    default:
      return extractApiErrorMessage(err, fallback)
  }
}
