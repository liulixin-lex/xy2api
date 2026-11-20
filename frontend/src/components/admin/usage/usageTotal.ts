/**
 * Whether the admin usage list's total is exact. The list asks with exact_total=false: unless a user, an API key or an
 * account narrows it (the server then counts), the server skips the count and answers "this page's end + 1" while more
 * rows follow (backend usage_log_repo_query.go, listUsageLogsWithFastPagination). That total is only a lower bound; on
 * the last page it is exact. The user page's list is always narrowed to the user, so its total is always exact.
 */
export function usageTotalIsExact(
  filters: { user_id?: number | null; api_key_id?: number | null; account_id?: number | null },
  page: { page: number; pageSize: number; rows: number; total: number },
): boolean {
  if (filters.user_id || filters.api_key_id || filters.account_id) return true
  return page.total <= (page.page - 1) * page.pageSize + page.rows
}
