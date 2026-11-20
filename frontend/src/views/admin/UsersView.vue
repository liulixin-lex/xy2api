<template>
  <AppLayout>
    <TablePageLayout table-layout>
      <template #filters>
        <!-- The compact toolbar (12.9): live, refresh, filter and column settings, a rule, then attributes, the bulk
             menu and create. -->
        <TableToolbar compact filter-sheet :filter-count="filterTags.length" @clear-filters="clearAllFilters">
          <template #status>
            <TableSortHint v-if="!phone && sortHint" :label="sortHint.label" :order="sortHint.order" />
            <LiveStatusIndicator :status="liveStatus" :pause="liveUsers.pause" :resume="liveUsers.resume" :detail="t('admin.users.liveDetail')" />
          </template>
          <template #search>
            <label class="table-toolbar__search-box">
              <span class="sr-only">{{ t('admin.users.searchUsers') }}</span>
              <Icon name="search" size="sm" class="table-toolbar__search-icon" />
              <input
                v-model="searchQuery"
                type="text"
                :placeholder="t('admin.users.searchUsers')"
                class="table-toolbar__input"
                @input="handleSearch"
              />
            </label>
          </template>

          <template v-if="!phone" #filters>
            <!-- Role Filter (visible when enabled) -->
            <div v-if="visibleFilters.has('role')" class="table-toolbar__filter" data-filter="role">
              <Select
                v-model="filters.role"
                size="sm"
                :options="roleFilterOptions"
                @change="applyFilter"
              />
            </div>

            <!-- Status Filter (visible when enabled) -->
            <div v-if="visibleFilters.has('status')" class="table-toolbar__filter" data-filter="status">
              <Select
                v-model="filters.status"
                size="sm"
                :options="statusFilterOptions"
                @change="applyFilter"
              />
            </div>

            <!-- Group Filter (visible when enabled) -->
            <div v-if="visibleFilters.has('group')" class="table-toolbar__filter" data-filter="group">
              <Select
                v-model="filters.group"
                size="sm"
                :options="groupFilterOptions"
                searchable
                creatable
                :creatable-prefix="t('admin.users.fuzzySearch')"
                :search-placeholder="t('admin.users.searchAuthorizedGroups')"
                @change="applyFilter"
              />
            </div>

            <!-- API Key Group Filter (visible when enabled) -->
            <div v-if="visibleFilters.has('apiKeyGroup')" class="table-toolbar__filter" data-filter="apiKeyGroup">
              <Select
                v-model="filters.apiKeyGroup"
                size="sm"
                :options="apiKeyGroupFilterOptions"
                searchable
                :search-placeholder="t('admin.users.searchApiKeyGroups')"
                @change="applyFilter"
              />
            </div>

            <!-- Dynamic Attribute Filters -->
            <template v-for="(value, attrId) in activeAttributeFilters" :key="attrId">
              <div
                v-if="visibleFilters.has(`attr_${attrId}`)"
                class="relative table-toolbar__filter"
                :data-filter="`attr_${attrId}`"
              >
                <!-- Text/Email/URL/Textarea/Date type: styled input -->
                <input
                  v-if="['text', 'textarea', 'email', 'url', 'date'].includes(getAttributeDefinition(Number(attrId))?.type || 'text')"
                  :value="value"
                  @input="(e) => updateAttributeFilter(Number(attrId), (e.target as HTMLInputElement).value)"
                  @keyup.enter="applyFilter"
                  :placeholder="getAttributeDefinitionName(Number(attrId))"
                  class="table-toolbar__input table-toolbar__input--plain"
                />
                <!-- Number type: number input -->
                <input
                  v-else-if="getAttributeDefinition(Number(attrId))?.type === 'number'"
                  :value="value"
                  type="number"
                  @input="(e) => updateAttributeFilter(Number(attrId), (e.target as HTMLInputElement).value)"
                  @keyup.enter="applyFilter"
                  :placeholder="getAttributeDefinitionName(Number(attrId))"
                  class="table-toolbar__input table-toolbar__input--plain"
                />
                <!-- Select/Multi-select type -->
                <template v-else-if="['select', 'multi_select'].includes(getAttributeDefinition(Number(attrId))?.type || '')">
                  <div class="w-full">
                    <Select
                      :model-value="value"
                      size="sm"
                      :options="[
                        { value: '', label: getAttributeDefinitionName(Number(attrId)) },
                        ...(getAttributeDefinition(Number(attrId))?.options || [])
                      ]"
                      @update:model-value="(val) => { updateAttributeFilter(Number(attrId), String(val ?? '')); applyFilter() }"
                    />
                  </div>
                </template>
                <!-- Fallback -->
                <input
                  v-else
                  :value="value"
                  @input="(e) => updateAttributeFilter(Number(attrId), (e.target as HTMLInputElement).value)"
                  @keyup.enter="applyFilter"
                  :placeholder="getAttributeDefinitionName(Number(attrId))"
                  class="table-toolbar__input table-toolbar__input--plain"
                />
              </div>
            </template>
          </template>

          <!-- A phone (mobile list plan): every filter in the bottom sheet, the attributes' too, applied at once; the chosen
               ones as removable tags; the count and the sort (when its column is hidden) beside the live state. -->
          <template v-if="phone" #sheet>
            <div class="users-sheet__field" data-filter="role">
              <span class="users-sheet__k">{{ t('admin.users.columns.role') }}</span>
              <Select :model-value="filters.role" :options="roleFilterOptions" :aria-label="t('admin.users.columns.role')" @update:model-value="setSheetFilter('role', $event)" />
            </div>
            <div class="users-sheet__field" data-filter="status">
              <span class="users-sheet__k">{{ t('admin.users.columns.status') }}</span>
              <Select :model-value="filters.status" :options="statusFilterOptions" :aria-label="t('admin.users.columns.status')" @update:model-value="setSheetFilter('status', $event)" />
            </div>
            <div class="users-sheet__field" data-filter="group">
              <span class="users-sheet__k">{{ t('admin.users.authorizedGroupFilter') }}</span>
              <Select
                :model-value="filters.group"
                :options="groupFilterOptions"
                searchable
                creatable
                :creatable-prefix="t('admin.users.fuzzySearch')"
                :search-placeholder="t('admin.users.searchAuthorizedGroups')"
                :aria-label="t('admin.users.authorizedGroupFilter')"
                @update:model-value="setSheetFilter('group', $event)"
              />
            </div>
            <div class="users-sheet__field" data-filter="apiKeyGroup">
              <span class="users-sheet__k">{{ t('admin.users.apiKeyGroupFilter') }}</span>
              <Select
                :model-value="filters.apiKeyGroup"
                :options="apiKeyGroupFilterOptions"
                searchable
                :search-placeholder="t('admin.users.searchApiKeyGroups')"
                :aria-label="t('admin.users.apiKeyGroupFilter')"
                @update:model-value="setSheetFilter('apiKeyGroup', $event)"
              />
            </div>
            <label v-for="attr in filterableAttributes" :key="attr.id" class="users-sheet__field" :data-filter="`attr_${attr.id}`">
              <span class="users-sheet__k">{{ attr.name }}</span>
              <Select
                v-if="attr.type === 'select' || attr.type === 'multi_select'"
                :model-value="activeAttributeFilters[attr.id] ?? ''"
                :options="[{ value: '', label: attr.name }, ...(attr.options || [])]"
                :aria-label="attr.name"
                @update:model-value="setSheetAttribute(attr.id, String($event ?? ''), true)"
              />
              <input
                v-else
                :value="activeAttributeFilters[attr.id] ?? ''"
                :type="attr.type === 'number' ? 'number' : 'text'"
                class="table-toolbar__input table-toolbar__input--plain"
                :placeholder="attr.name"
                @change="setSheetAttribute(attr.id, ($event.target as HTMLInputElement).value, true)"
              />
            </label>
          </template>
          <template v-if="phone" #tags>
            <span v-for="tag in filterTags" :key="tag.key" class="users-tag" :data-key="tag.key">
              <span class="users-tag__k">{{ tag.label }}：</span>
              <span class="users-tag__v">{{ tag.text }}</span>
              <button type="button" class="users-tag__x" :aria-label="t('common.table.removeFilter', { label: tag.label })" @click="removeFilterTag(tag.key)">
                <Icon name="x" size="xs" />
              </button>
            </span>
          </template>
          <template v-if="phone" #info>
            <span data-test="users-count">{{ t('common.table.count', { n: usersTotal.toLocaleString() }) }}</span>
            <template v-if="sortHint">
              <span class="table-toolbar__sep" aria-hidden="true">·</span>
              <TableSortHint :label="sortHint.label" :order="sortHint.order" />
            </template>
          </template>

          <template #actions>
            <button
              type="button"
              @click="refreshUsers"
              :disabled="loading"
              class="table-toolbar__icon-btn table-toolbar__icon"
              :aria-label="t('common.refresh')"
              v-tip="t('common.refresh')"
            >
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            </button>
            <!-- Filter Settings Dropdown (a phone has every filter in its sheet) -->
            <div v-if="!phone" class="relative" ref="filterDropdownRef">
              <button
                type="button"
                @click="showFilterDropdown = !showFilterDropdown"
                class="table-toolbar__icon-btn table-toolbar__icon"
                :aria-label="t('admin.users.filterSettings')"
                aria-haspopup="true"
                :aria-expanded="showFilterDropdown ? 'true' : 'false'"
                v-tip="t('admin.users.filterSettings')"
              >
                <Icon name="filter" size="sm" />
              </button>
              <div
                v-if="showFilterDropdown"
                class="absolute right-0 top-full z-50 mt-1 w-48 rounded-popover border border-line-popover bg-surface-popover py-1 shadow-dropdown"
              >
                <!-- Built-in filters -->
                <button
                  v-for="filter in builtInFilters"
                  :key="filter.key"
                  type="button"
                  @click="toggleBuiltInFilter(filter.key)"
                  :class="[POPOVER_ITEM, POPOVER_ITEM_TEXT]"
                >
                  <span>{{ filter.name }}</span>
                  <Icon
                    v-if="visibleFilters.has(filter.key)"
                    name="check"
                    size="sm"
                    class="text-primary-500"
                    :stroke-width="2"
                  />
                </button>
                <!-- Divider if custom attributes exist -->
                <div
                  v-if="filterableAttributes.length > 0"
                  class="my-1 border-t border-line-popover"
                ></div>
                <!-- Custom attribute filters -->
                <button
                  v-for="attr in filterableAttributes"
                  :key="attr.id"
                  type="button"
                  @click="toggleAttributeFilter(attr)"
                  :class="[POPOVER_ITEM, POPOVER_ITEM_TEXT]"
                >
                  <span>{{ attr.name }}</span>
                  <Icon
                    v-if="visibleFilters.has(`attr_${attr.id}`)"
                    name="check"
                    size="sm"
                    class="text-primary-500"
                    :stroke-width="2"
                  />
                </button>
              </div>
            </div>
            <!-- Column Settings Dropdown -->
            <div class="relative" ref="columnDropdownRef">
              <!-- A phone: this width's columns in a bottom sheet (mobile list plan); wider, the dropdown. -->
              <button
                type="button"
                @click="phone ? (showColumnSheet = true) : (showColumnDropdown = !showColumnDropdown)"
                class="table-toolbar__icon-btn table-toolbar__icon"
                :aria-label="t('admin.users.columnSettings')"
                :aria-haspopup="phone ? 'dialog' : 'true'"
                :aria-expanded="(phone ? showColumnSheet : showColumnDropdown) ? 'true' : 'false'"
                v-tip="t('admin.users.columnSettings')"
                data-test="users-columns"
              >
                <svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5" aria-hidden="true">
                  <path stroke-linecap="round" stroke-linejoin="round" d="M9 4.5v15m6-15v15m-10.875 0h15.75c.621 0 1.125-.504 1.125-1.125V5.625c0-.621-.504-1.125-1.125-1.125H4.125C3.504 4.5 3 5.004 3 5.625v12.75c0 .621.504 1.125 1.125 1.125z" />
                </svg>
              </button>
              <div
                v-if="showColumnDropdown && !phone"
                class="absolute right-0 top-full z-50 mt-1 max-h-80 w-48 overflow-y-auto rounded-popover border border-line-popover bg-surface-popover py-1 shadow-dropdown"
              >
                <button
                  v-for="col in toggleableColumns"
                  :key="col.key"
                  type="button"
                  :disabled="isForcedVisibleColumn(col.key)"
                  @click="toggleColumn(col.key)"
                  :class="[
                    POPOVER_ITEM,
                    isForcedVisibleColumn(col.key)
                      ? 'cursor-not-allowed text-gray-400 dark:text-gray-500'
                      : POPOVER_ITEM_TEXT
                  ]"
                  :title="isForcedVisibleColumn(col.key) ? t('admin.users.columnAlwaysVisible') : ''"
                >
                  <span>{{ col.label }}</span>
                  <Icon
                    v-if="isColumnVisible(col.key)"
                    name="check"
                    size="sm"
                    :class="isForcedVisibleColumn(col.key) ? 'text-gray-400 dark:text-gray-500' : 'text-primary-500'"
                    :stroke-width="2"
                  />
                </button>
                <button
                  v-if="!columnVisibility.atDefault.value"
                  type="button"
                  :class="[POPOVER_ITEM, 'border-t border-line-popover text-primary-600 hover:bg-surface-popover-hover dark:text-primary-400']"
                  data-test="reset-columns"
                  @click="resetColumns"
                >
                  {{ t('common.table.resetColumns') }}
                </button>
              </div>
            </div>
            <span v-if="canManage" class="table-toolbar__divider" aria-hidden="true"></span>
            <button
              v-if="canManage"
              type="button"
              @click="showAttributesModal = true"
              class="table-toolbar__btn table-toolbar__btn--secondary"
              :aria-label="t('admin.users.attributes.configButton')"
              v-tip="t('admin.users.attributes.configButton')"
            >
              <Icon name="cog" size="sm" />
              <span class="table-toolbar__label table-toolbar__label--wide">{{ t('admin.users.attributes.configButton') }}</span>
            </button>

            <!-- The four bulk actions in one menu (12.9-5), disabled until a row is ticked, the count on the button in a
                 slot that keeps its width: ticking rows changes neither the toolbar's rows nor the table's place. -->
            <div v-if="canManage" ref="bulkRootRef" class="relative">
              <button
                ref="bulkButtonRef"
                type="button"
                class="table-toolbar__btn table-toolbar__btn--secondary users-bulk"
                data-test="bulk-actions"
                :disabled="selectedCount === 0"
                aria-haspopup="menu"
                :aria-expanded="bulkMenuOpen ? 'true' : 'false'"
                :aria-label="t('admin.users.bulkActions')"
                v-tip="selectedCount ? t('admin.users.bulkLimits.selectedCount', { count: selectedCount }) : t('admin.users.bulkActions')"
                @click="bulkMenuOpen = !bulkMenuOpen"
              >
                <Icon name="users" size="sm" />
                <span class="table-toolbar__label">{{ t('admin.users.bulkActions') }}</span>
                <span class="users-bulk__count" :class="{ 'is-empty': selectedCount === 0 }" data-test="bulk-count">{{ selectedCount || '' }}</span>
                <Icon name="chevronDown" size="xs" class="users-bulk__chevron" />
              </button>
              <div
                v-if="bulkMenuOpen"
                ref="bulkMenuRef"
                role="menu"
                class="absolute right-0 top-full mt-1 w-44 rounded-popover border border-line-popover bg-surface-popover py-1 shadow-dropdown"
                :style="{ zIndex: bulkMenuLayer.zIndex.value }"
                data-test="bulk-menu"
              >
                <button type="button" role="menuitem" :class="BULK_ITEM" data-test="bulk-edit-limits" @click="bulkMenuOpen = false; showBulkEditModal = true">
                  {{ t('admin.users.bulkLimits.label') }}
                </button>
                <button type="button" role="menuitem" :class="BULK_ITEM" data-test="bulk-enable-users" :disabled="bulkStatusUpdating" @click="bulkMenuOpen = false; openBulkStatus('active')">
                  {{ t('admin.users.enable') }}
                </button>
                <button type="button" role="menuitem" :class="BULK_ITEM" data-test="bulk-disable-users" :disabled="bulkStatusUpdating" @click="bulkMenuOpen = false; openBulkStatus('disabled')">
                  {{ t('admin.users.disable') }}
                </button>
                <div class="my-1 border-t border-line-popover"></div>
                <button type="button" role="menuitem" :class="[BULK_ITEM, BULK_DANGER]" data-test="bulk-delete-users" :disabled="bulkDeleting" @click="bulkMenuOpen = false; bulkDeleteIds = [...selectedIds]">
                  {{ t('common.delete') }}
                </button>
              </div>
            </div>

          </template>
          <template v-if="canManage" #create>
            <button type="button" class="table-toolbar__btn table-toolbar__btn--primary" :aria-label="t('admin.users.createUser')" data-test="users-create" @click="showCreateModal = true">
              <Icon name="plus" size="sm" />
              <span>{{ phone ? t('admin.users.createShort') : t('admin.users.createUser') }}</span>
            </button>
          </template>
        </TableToolbar>
      </template>

      <!-- Users Table -->
      <template #table>
        <PendingChangesPill :count="pending" :label="changesText" :tip="t('common.pendingChanges.tip')" @merge="applyPending" />
        <DataTable
          ref="usersTableRef"
          class="dense-cells"
          layout="table"
          :scroll-label="t('admin.users.title')"
          :columns="columns"
          :data="users"
          :loading="loading"
          :row-class="userRowClass"
          row-key="id"
          selectable
          :selected-keys="selectedIds"
          :selection-label="getUserSelectionLabel"
          :row-selectable="canSelectUser"
          :actions-count="7"
          :server-side-sort="true"
          :sort="tableSort"
          :sort-storage-key="USER_SORT_STORAGE_KEY"
          @sort="handleSort"
          @update:selected-keys="handleSelectedKeysUpdate"
        >
          <!-- The frozen identity (mobile list plan): the address, the user name under it, each cut with "…" at the column's
               width (on a phone clamp(120px, 36vw, 160px), from 768px 240px); hovered or tapped, the tip has both whole. -->
          <template #cell-email="{ value, row }">
            <TableIdentityCell class="users-identity" :primary="value" :secondary="row.username || undefined" data-test="user-email" />
          </template>

          <template #cell-username="{ value }">
            <span v-if="value" class="text-sm text-gray-700 dark:text-gray-300">{{ value }}</span>
            <CellEmpty v-else />
          </template>

          <template #cell-notes="{ value }">
            <div class="max-w-xs">
              <span
                v-if="value"
                :title="value.length > 30 ? value : undefined"
                class="block truncate text-sm text-gray-600 dark:text-gray-400"
              >
                {{ value.length > 30 ? value.substring(0, 25) + '...' : value }}
              </span>
              <CellEmpty v-else />
            </div>
          </template>

          <!-- Dynamic attribute columns -->
          <template
            v-for="def in attributeDefinitions.filter(d => d.enabled)"
            :key="def.id"
            #[`cell-attr_${def.id}`]="{ row }"
          >
            <div class="max-w-xs">
              <span
                v-if="getAttributeValue(row, def.id)"
                class="block truncate text-sm text-gray-700 dark:text-gray-300"
                :title="getAttributeValue(row, def.id)"
              >
                {{ getAttributeValue(row, def.id) }}
              </span>
              <CellEmpty v-else />
            </div>
          </template>

          <template #cell-role="{ value }">
            <span :class="['badge', value === 'admin' ? 'badge-purple' : value === 'operator' ? 'badge-primary' : 'badge-gray']">
              {{ t('admin.users.roles.' + value) }}
            </span>
          </template>

          <template #cell-groups="{ row }">
            <div v-if="allGroups.length > 0" class="flex flex-col gap-1">
              <!-- 专属分组行 -->
              <span
                v-if="getUserGroups(row).exclusive.length > 0"
                class="group/ex relative inline-flex cursor-pointer items-center gap-1 whitespace-nowrap text-xs"
                @click.stop="toggleExpandedGroup(row.id)"
              >
                <Icon name="shield" size="xs" class="h-3.5 w-3.5 text-purple-500 dark:text-purple-400" />
                <span class="font-medium text-purple-600 dark:text-purple-400">{{ getUserGroups(row).exclusive.length }}</span>
                <span class="text-gray-500 dark:text-dark-400">{{ t('admin.users.exclusiveLabel') }}</span>
                <!-- Hover tooltip（操作菜单未打开时显示） -->
                <div
                  v-if="expandedGroupUserId !== row.id"
                  class="pointer-events-none absolute left-0 top-full z-50 mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 text-xs text-white opacity-0 shadow-lg transition-opacity duration-75 group-hover/ex:opacity-100 dark:bg-dark-600"
                >
                  <div class="absolute left-4 bottom-full border-4 border-transparent border-b-gray-900 dark:border-b-dark-600"></div>
                  <div class="flex flex-col gap-0.5 whitespace-nowrap">
                    <span v-for="g in getUserGroups(row).exclusive" :key="g.id">{{ g.name }}</span>
                  </div>
                </div>
                <!-- 点击展开分组操作菜单 -->
                <div
                  v-if="expandedGroupUserId === row.id"
                  class="absolute left-0 top-full z-50 mt-1.5 min-w-[160px] overflow-hidden rounded-lg border border-gray-200 bg-white py-1 text-xs shadow-xl dark:border-dark-600 dark:bg-dark-700"
                >
                  <div class="border-b border-gray-100 px-3 py-1.5 text-xs font-medium uppercase tracking-wider text-gray-400 dark:border-dark-600 dark:text-dark-400">
                    {{ t('admin.users.clickToReplace') }}
                  </div>
                  <div
                    v-for="g in getUserGroups(row).exclusive"
                    :key="g.id"
                    class="flex cursor-pointer items-center gap-2 px-3 py-2 text-gray-700 transition-colors hover:bg-primary-50 hover:text-primary-600 dark:text-dark-200 dark:hover:bg-primary-900/30 dark:hover:text-primary-400"
                    @click.stop="openGroupReplace(row, g)"
                  >
                    <Icon name="swap" size="xs" class="h-3.5 w-3.5 flex-shrink-0 opacity-50" />
                    <span class="flex-1">{{ g.name }}</span>
                  </div>
                </div>
              </span>
              <!-- 公开分组行 -->
              <span
                v-if="getUserGroups(row).publicGroups.length > 0"
                class="group/pub relative inline-flex cursor-default items-center gap-1 whitespace-nowrap text-xs"
              >
                <Icon name="globe" size="xs" class="h-3.5 w-3.5 text-gray-400 dark:text-dark-500" />
                <span class="font-medium text-gray-600 dark:text-dark-300">{{ getUserGroups(row).publicGroups.length }}</span>
                <span class="text-gray-400 dark:text-dark-500">{{ t('admin.users.publicLabel') }}</span>
                <!-- Tooltip: 向下弹出 -->
                <div class="pointer-events-none absolute left-0 top-full z-50 mt-1.5 rounded bg-gray-900 px-2.5 py-1.5 text-xs text-white opacity-0 shadow-lg transition-opacity duration-75 group-hover/pub:opacity-100 dark:bg-dark-600">
                  <div class="absolute left-4 bottom-full border-4 border-transparent border-b-gray-900 dark:border-b-dark-600"></div>
                  <div class="flex flex-col gap-0.5 whitespace-nowrap">
                    <span v-for="g in getUserGroups(row).publicGroups" :key="g.id">{{ g.name }}</span>
                  </div>
                </div>
              </span>
              <!-- 都没有 -->
              <CellEmpty v-if="getUserGroups(row).exclusive.length === 0 && getUserGroups(row).publicGroups.length === 0" />
            </div>
            <CellEmpty v-else />
          </template>

          <template #cell-subscriptions="{ row }">
            <div
              v-if="row.subscriptions && row.subscriptions.length > 0"
              class="flex flex-wrap gap-1.5"
            >
              <GroupBadge
                v-for="sub in row.subscriptions"
                :key="sub.id"
                :name="sub.group?.name || ''"
                :platform="sub.group?.platform"
                :subscription-type="sub.group?.subscription_type"
                :rate-multiplier="sub.group?.rate_multiplier"
                :days-remaining="sub.expires_at ? getDaysRemaining(sub.expires_at) : null"
                :title="sub.expires_at ? formatDateTime(sub.expires_at) : ''"
              />
            </div>
            <span
              v-else
              class="inline-flex items-center gap-1.5 rounded-md bg-gray-50 px-2 py-1 text-xs text-gray-400 dark:bg-dark-700/50 dark:text-dark-500"
            >
              <Icon name="ban" size="xs" class="h-3.5 w-3.5" />
              <span>{{ t('admin.users.noSubscription') }}</span>
            </span>
          </template>

          <template #cell-balance="{ value, row }">
            <div class="flex items-center gap-2">
              <div class="group relative">
                <button
                  class="font-medium text-gray-900 underline decoration-dashed decoration-gray-300 underline-offset-4 transition-colors hover:text-primary-600 dark:text-white dark:decoration-dark-500 dark:hover:text-primary-400"
                  :disabled="!canReadRow(row)"
                  @click="handleBalanceHistory(row)"
                >
                  ${{ value.toFixed(2) }}
                </button>
                <!-- Instant tooltip -->
                <div class="pointer-events-none absolute bottom-full left-1/2 z-50 mb-1.5 -translate-x-1/2 whitespace-nowrap rounded bg-gray-900 px-2 py-1 text-xs text-white opacity-0 shadow-lg transition-opacity duration-75 group-hover:opacity-100 dark:bg-dark-600">
                  {{ t('admin.users.balanceHistoryTip') }}
                  <div class="absolute left-1/2 top-full -translate-x-1/2 border-4 border-transparent border-t-gray-900 dark:border-t-dark-600"></div>
                </div>
              </div>
              <button
                v-if="canAdjustBalanceOf(row)"
                @click.stop="handleDeposit(row)"
                class="rounded px-2 py-0.5 text-xs font-medium text-emerald-600 transition-colors hover:bg-emerald-50 dark:text-emerald-400 dark:hover:bg-emerald-900/20"
                :title="t('admin.users.deposit')"
              >
                {{ t('admin.users.deposit') }}
              </button>
            </div>
          </template>

          <template #cell-balance_platform_quota="{ row }">
            <button
              type="button"
              class="block text-left underline decoration-dashed decoration-gray-300 underline-offset-4 transition-colors hover:decoration-primary-400 dark:decoration-dark-500"
              :title="t('admin.users.platformQuota.cellColumnTooltip')"
              :disabled="!canReadRow(row)"
              @click="handlePlatformQuota(row)"
            >
              <UserPlatformQuotaCell :quotas="row.platform_quotas" />
            </button>
          </template>

          <!-- 用量列自定义表头：列名 + 单个排序图标按钮，点击展开"今日/近30天"菜单。
               column.sortable=false，DataTable 内置点击逻辑不会触发；
               菜单项三态循环：desc → asc → off。 -->
          <template
            v-for="usageKey in USAGE_COLUMN_KEYS"
            :key="usageKey"
            #[`header-${usageKey}`]="{ column }"
          >
            <div class="flex items-center gap-1.5">
              <span>{{ column.label }}</span>
              <div class="usage-sort-trigger relative">
                <button
                  type="button"
                  class="flex items-center gap-1 rounded px-1 py-0.5 transition-colors hover:bg-gray-200 dark:hover:bg-dark-700"
                  :class="usageSort && usageSort.key === usageKey
                    ? 'text-primary-600 dark:text-primary-400'
                    : 'text-gray-400 dark:text-dark-500'"
                  :title="t('admin.users.sortBy')"
                  :data-test="`usage-sort-trigger-${usageKey}`"
                  @click.stop="toggleUsageSortMenu(usageKey)"
                >
                  <span
                    v-if="usageSort && usageSort.key === usageKey"
                    class="text-xs normal-case font-medium tracking-normal"
                  >{{ usageSort.metric === 'today' ? t('admin.users.today') : t('admin.users.total') }}</span>
                  <svg
                    v-if="usageSort && usageSort.key === usageKey"
                    class="h-3.5 w-3.5"
                    :class="{ 'rotate-180': usageSort.order === 'desc' }"
                    fill="currentColor"
                    viewBox="0 0 20 20"
                  >
                    <path
                      fill-rule="evenodd"
                      d="M14.707 12.707a1 1 0 01-1.414 0L10 9.414l-3.293 3.293a1 1 0 01-1.414-1.414l4-4a1 1 0 011.414 0l4 4a1 1 0 010 1.414z"
                      clip-rule="evenodd"
                    />
                  </svg>
                  <svg v-else class="h-3.5 w-3.5" fill="currentColor" viewBox="0 0 20 20">
                    <path d="M10 3l-4 5h8l-4-5zM10 17l4-5H6l4 5z" />
                  </svg>
                </button>
                <!-- 弹出菜单：今日 / 近30天，点击进行三态循环切换。 -->
                <div
                  v-if="openUsageSortMenu === usageKey"
                  class="absolute right-0 top-full z-50 mt-1 min-w-[120px] rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-dark-600 dark:bg-dark-800"
                >
                  <button
                    v-for="metric in (['today', 'total'] as const)"
                    :key="metric"
                    type="button"
                    class="flex w-full items-center justify-between gap-3 px-3 py-1.5 text-left text-xs normal-case tracking-normal hover:bg-gray-100 dark:hover:bg-dark-700"
                    :class="isUsageSortActive(usageKey, metric)
                      ? 'font-medium text-primary-600 dark:text-primary-400'
                      : 'text-gray-700 dark:text-gray-300'"
                    :data-test="`usage-sort-${usageKey}-${metric}`"
                    @click.stop="toggleUsageSort(usageKey, metric)"
                  >
                    <span>{{ metric === 'today' ? t('admin.users.today') : t('admin.users.total') }}</span>
                    <svg
                      v-if="getUsageSortOrder(usageKey, metric)"
                      class="h-3 w-3"
                      :class="{ 'rotate-180': getUsageSortOrder(usageKey, metric) === 'desc' }"
                      fill="currentColor"
                      viewBox="0 0 20 20"
                    >
                      <path
                        fill-rule="evenodd"
                        d="M14.707 12.707a1 1 0 01-1.414 0L10 9.414l-3.293 3.293a1 1 0 01-1.414-1.414l4-4a1 1 0 011.414 0l4 4a1 1 0 010 1.414z"
                        clip-rule="evenodd"
                      />
                    </svg>
                  </button>
                  <div class="mt-1 border-t border-gray-100 px-3 py-1 text-xs normal-case tracking-normal text-gray-400 dark:border-dark-700 dark:text-dark-500">
                    {{ t('admin.users.sortCurrentPageOnly') }}
                  </div>
                </div>
              </div>
            </div>
          </template>

          <template #cell-usage="{ row }">
            <PlatformUsageBreakdown
              :today="row.usage_stats?.today_actual_cost ?? 0"
              :total="row.usage_stats?.total_actual_cost ?? 0"
              :by-platform="row.usage_stats?.by_platform"
            />
          </template>

          <template #cell-usage_anthropic="{ row }">
            <PlatformCostCell :usage="getPlatformUsage(row, 'anthropic')" />
          </template>

          <template #cell-usage_openai="{ row }">
            <PlatformCostCell :usage="getPlatformUsage(row, 'openai')" />
          </template>

          <template #cell-usage_gemini="{ row }">
            <PlatformCostCell :usage="getPlatformUsage(row, 'gemini')" />
          </template>

          <template #cell-usage_antigravity="{ row }">
            <PlatformCostCell :usage="getPlatformUsage(row, 'antigravity')" />
          </template>

          <template #cell-concurrency="{ row }">
            <UserConcurrencyCell
              :current="row.current_concurrency ?? 0"
              :max="row.concurrency"
            />
          </template>

          <template #cell-status="{ value }">
            <span class="inline-flex items-center gap-1.5 whitespace-nowrap" :class="{ 'status-text-normal': value === 'active' }">
              <StatusDot :tone="value === 'active' ? 'ok' : 'off'" />
              {{ value === 'active' ? t('common.active') : t('admin.users.disabled') }}
            </span>
          </template>

          <template #cell-created_at="{ value }">
            <span class="text-sm tabular-nums text-gray-500 dark:text-dark-400">{{ formatListDateTime(value) }}</span>
          </template>

          <template #cell-last_used_at="{ value }">
            <span v-if="value" class="text-sm tabular-nums text-gray-500 dark:text-dark-400">{{ formatListDateTime(value) }}</span>
            <CellEmpty v-else />
          </template>

          <template #cell-last_active_at="{ value }">
            <span v-if="value" class="text-sm tabular-nums text-gray-500 dark:text-dark-400">{{ formatListDateTime(value) }}</span>
            <CellEmpty v-else />
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1">
              <!-- Edit Button -->
              <button
                v-if="isSuperAdmin || canActOn(row)"
                @click="handleEdit(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              >
                <Icon name="edit" size="sm" />
                <span class="text-xs">{{ t('common.edit') }}</span>
              </button>

              <!-- Toggle Status Button (not for admin) -->
              <button
                v-if="canActOn(row)"
                @click="handleToggleStatus(row)"
                :class="[
                  'flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors',
                  row.status === 'active'
                    ? 'hover:bg-orange-50 hover:text-orange-600 dark:hover:bg-orange-900/20 dark:hover:text-orange-400'
                    : 'hover:bg-green-50 hover:text-green-600 dark:hover:bg-green-900/20 dark:hover:text-green-400'
                ]"
              >
                <Icon v-if="row.status === 'active'" name="ban" size="sm" />
                <Icon v-else name="checkCircle" size="sm" />
                <span class="text-xs">{{ row.status === 'active' ? t('admin.users.disable') : t('admin.users.enable') }}</span>
              </button>

              <!-- More Actions Menu Trigger -->
              <button
                v-if="canReadRow(row)"
                @click="openActionMenu(row, $event)"
                class="action-menu-trigger flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-700 dark:hover:text-white"
                :class="{ 'bg-gray-100 text-gray-900 dark:bg-dark-700 dark:text-white': activeMenuId === row.id }"
              >
                <Icon name="more" size="sm" />
                <span class="text-xs">{{ t('common.more') }}</span>
              </button>
            </div>
          </template>

          <template #empty>
            <EmptyState
              :title="t('admin.users.noUsersYet')"
              :description="t('admin.users.createFirstUser')"
              :action-text="canManage ? t('admin.users.createUser') : undefined"
              @action="showCreateModal = true"
            />
          </template>
        </DataTable>
      </template>

      <!-- Pagination -->
      <template #pagination>
      <Pagination
        v-if="usersTotal > 0"
        :page="pagination.page"
        :total="usersTotal"
        :page-size="pagination.page_size"
        @update:page="handlePageChange"
        @update:pageSize="handlePageSizeChange"
      />
      </template>
    </TablePageLayout>

    <!-- Action Menu (Teleported) -->
    <Teleport to="body">
      <div
        v-if="activeMenuId !== null && menuPosition"
        class="action-menu-content fixed z-[9999] w-48 overflow-hidden rounded-xl bg-white shadow-lg ring-1 ring-black/5 dark:bg-dark-800 dark:ring-white/10"
        :style="{ top: menuPosition.top + 'px', left: menuPosition.left + 'px', zIndex: actionMenuLayer.zIndex.value }"
        ref="actionMenuRef"
      >
        <div class="py-1">
          <template v-for="user in users" :key="user.id">
            <template v-if="user.id === activeMenuId">
              <!-- View API Keys -->
              <button
                @click="handleViewApiKeys(user); closeActionMenu()"
                class="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
              >
                <Icon name="key" size="sm" class="text-gray-400" :stroke-width="2" />
                {{ t('admin.users.apiKeys') }}
              </button>

              <!-- Allowed Groups -->
              <button
                v-if="isSuperAdmin || canActOn(user)"
                @click="handleAllowedGroups(user); closeActionMenu()"
                class="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
              >
                <Icon name="users" size="sm" class="text-gray-400" :stroke-width="2" />
                {{ t('admin.users.groups') }}
              </button>

              <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>

              <!-- Deposit -->
              <button
                v-if="canAdjustBalanceOf(user)"
                @click="handleDeposit(user); closeActionMenu()"
                class="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
              >
                <Icon name="plus" size="sm" class="text-emerald-500" :stroke-width="2" />
                {{ t('admin.users.deposit') }}
              </button>

              <!-- Withdraw -->
              <button
                v-if="canAdjustBalanceOf(user)"
                @click="handleWithdraw(user); closeActionMenu()"
                class="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
              >
                <svg class="h-4 w-4 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20 12H4" />
                </svg>
                {{ t('admin.users.withdraw') }}
              </button>

              <!-- Platform Quotas -->
              <button
                v-if="isSuperAdmin || canActOn(user)"
                @click="handlePlatformQuota(user); closeActionMenu()"
                class="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
              >
                <Icon name="chartBar" size="sm" class="text-gray-400" :stroke-width="2" />
                {{ t('admin.users.platformQuota.menuItem') }}
              </button>

              <!-- Balance History -->
              <button
                @click="handleBalanceHistory(user); closeActionMenu()"
                class="flex w-full items-center gap-2 px-4 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
              >
                <Icon name="dollar" size="sm" class="text-gray-400" :stroke-width="2" />
                {{ t('admin.users.balanceHistory') }}
              </button>

              <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>

              <!-- Delete (not for admin) -->
              <button
                v-if="canActOn(user)"
                @click="handleDelete(user); closeActionMenu()"
                class="flex w-full items-center gap-2 px-4 py-2 text-sm text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20"
              >
                <Icon name="trash" size="sm" :stroke-width="2" />
                {{ t('common.delete') }}
              </button>
            </template>
          </template>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog :show="showDeleteDialog" :title="t('admin.users.deleteUser')" :message="t('admin.users.deleteConfirm', { email: deletingUser?.email })" :danger="true" @confirm="confirmDelete" @cancel="showDeleteDialog = false" />
    <ConfirmDialog
      :show="bulkDeleteIds.length > 0"
      :title="t('admin.users.bulkDelete.title')"
      :message="t('admin.users.bulkDelete.confirm', { count: bulkDeleteIds.length })"
      :confirm-text="t('common.delete')"
      danger
      @confirm="confirmBulkDelete"
      @cancel="bulkDeleteIds = []"
    />
    <ConfirmDialog
      :show="bulkStatusTarget !== null"
      :title="bulkStatusTarget === 'disabled' ? t('admin.users.bulkStatus.disableTitle') : t('admin.users.bulkStatus.enableTitle')"
      :message="bulkStatusTarget === 'disabled'
        ? t('admin.users.bulkStatus.disableConfirm', { count: bulkStatusIds.length })
        : t('admin.users.bulkStatus.enableConfirm', { count: bulkStatusIds.length })"
      :confirm-text="bulkStatusTarget === 'disabled' ? t('admin.users.disable') : t('admin.users.enable')"
      :danger="bulkStatusTarget === 'disabled'"
      @confirm="confirmBulkStatus"
      @cancel="closeBulkStatus"
    />
    <UserCreateModal :show="showCreateModal" @close="showCreateModal = false" @success="reloadUsers" />
    <UserEditModal :show="showEditModal" :user="editingUser" @close="closeEditModal" @success="reloadUsers" />
    <BulkEditUserModal
      :show="showBulkEditModal"
      :selected-ids="selectedIds"
      @close="showBulkEditModal = false"
      @success="handleBulkLimitsSuccess"
    />
    <UserPlatformQuotaModal
      :show="showPlatformQuotaModal"
      :user="platformQuotaUser"
      @close="closePlatformQuotaModal"
      @success="reloadUsers"
    />
    <UserApiKeysModal :show="showApiKeysModal" :user="viewingUser" @close="closeApiKeysModal" />
    <UserAllowedGroupsModal :show="showAllowedGroupsModal" :user="allowedGroupsUser" @close="closeAllowedGroupsModal" @success="reloadUsers" />
    <UserBalanceModal :show="showBalanceModal" :user="balanceUser" :operation="balanceOperation" @close="closeBalanceModal" @success="reloadUsers" />
    <UserBalanceHistoryModal :show="showBalanceHistoryModal" :user="balanceHistoryUser" @close="closeBalanceHistoryModal" @deposit="handleDepositFromHistory" @withdraw="handleWithdrawFromHistory" />
    <GroupReplaceModal :show="showGroupReplaceModal" :user="groupReplaceUser" :old-group="groupReplaceOldGroup" :all-groups="allGroups" @close="closeGroupReplaceModal" @success="reloadUsers" />
    <UserAttributesConfigModal :show="showAttributesModal" @close="handleAttributesModalClose" />
    <!-- A phone's column settings: this width's own columns and their "restore default columns". -->
    <BottomSheet :show="showColumnSheet" :title="t('common.table.columnsTitle')" test-id="column-sheet" @close="showColumnSheet = false">
      <div class="users-colsheet">
        <button
          v-for="col in toggleableColumns"
          :key="col.key"
          type="button"
          class="users-colsheet__item"
          :disabled="isForcedVisibleColumn(col.key)"
          :aria-pressed="isColumnVisible(col.key) ? 'true' : 'false'"
          data-test="column-setting"
          @click="toggleColumn(col.key)"
        >
          <span class="truncate">{{ col.label }}</span>
          <Icon v-if="isColumnVisible(col.key)" name="check" size="sm" class="text-primary-500" :stroke-width="2" />
        </button>
        <button v-if="!columnVisibility.atDefault.value" type="button" class="users-colsheet__reset" data-test="reset-columns" @click="resetColumns">
          {{ t('common.table.resetColumns') }}
        </button>
      </div>
    </BottomSheet>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { useOverlayLayer } from '@/composables/useOverlayLayer'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useTableSelection } from '@/composables/useTableSelection'
import { useColumnVisibility } from '@/composables/useColumnVisibility'
import { useTableViewport } from '@/composables/useTableViewport'
import { formatDateTime, formatListDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import Icon from '@/components/icons/Icon.vue'
import { vTip } from '@/utils/tip'

const { t } = useI18n()
import { adminAPI } from '@/api/admin'
import type { AdminUser, AdminGroup, UserAttributeDefinition } from '@/types'
import type { BatchUserUsageStats } from '@/api/admin/dashboard'
import { useLiveTable, type LivePatches } from '@/composables/useLiveTable'
import { userUsageOrDefault } from './liveRowDefaults'
import type { PlatformQuotaItem } from '@/api/admin/users'
import type { Column } from '@/components/common/types'
import type { SelectOption } from '@/components/common/Select.vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import TableToolbar from '@/components/layout/TableToolbar.vue'
import DataTable from '@/components/common/DataTable.vue'
import TableIdentityCell from '@/components/common/TableIdentityCell.vue'
import CellEmpty from '@/components/common/CellEmpty.vue'
import TableSortHint from '@/components/common/TableSortHint.vue'
import LiveStatusIndicator from '@/components/common/LiveStatusIndicator.vue'
import PendingChangesPill from '@/components/common/PendingChangesPill.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Select from '@/components/common/Select.vue'
import BottomSheet from '@/components/common/BottomSheet.vue'
import { buildApiKeyGroupFilterOptions } from './apiKeyGroupFilterOptions'
import UserAttributesConfigModal from '@/components/user/UserAttributesConfigModal.vue'
import UserConcurrencyCell from '@/components/user/UserConcurrencyCell.vue'
import PlatformUsageBreakdown from '@/components/user/PlatformUsageBreakdown.vue'
import PlatformCostCell from '@/components/user/PlatformCostCell.vue'
import UserPlatformQuotaCell from '@/components/user/UserPlatformQuotaCell.vue'
import UserCreateModal from '@/components/admin/user/UserCreateModal.vue'
import UserEditModal from '@/components/admin/user/UserEditModal.vue'
import BulkEditUserModal from '@/components/admin/user/BulkEditUserModal.vue'
import UserPlatformQuotaModal from '@/components/admin/user/UserPlatformQuotaModal.vue'
import UserApiKeysModal from '@/components/admin/user/UserApiKeysModal.vue'
import UserAllowedGroupsModal from '@/components/admin/user/UserAllowedGroupsModal.vue'
import UserBalanceModal from '@/components/admin/user/UserBalanceModal.vue'
import UserBalanceHistoryModal from '@/components/admin/user/UserBalanceHistoryModal.vue'
import GroupReplaceModal from '@/components/admin/user/GroupReplaceModal.vue'

const appStore = useAppStore()
const authStore = useAuthStore()
const { canManage, has, isSuperAdmin } = useAdminPermission('users')
// Super admins act on everyone except admins. Operators with users:manage act
// on plain users other than themselves (the backend target rule).
const canActOn = (row: AdminUser) =>
  isSuperAdmin.value ? row.role !== 'admin' : canManage.value && row.role === 'user' && row.id !== authStore.user?.id
const canSelectUser = (row: AdminUser) => isSuperAdmin.value || canActOn(row)
// Operators can only open plain users' details (never admins, operators or themselves).
const canReadRow = (row: AdminUser) => isSuperAdmin.value || row.role === 'user'
// Super admins keep deposit and withdraw on every row; operators need the row and "Adjust balance".
const canAdjustBalanceOf = (row: AdminUser) =>
  isSuperAdmin.value || (canActOn(row) && has('users.balance_adjust'))

// Generate dynamic attribute columns from enabled definitions
const attributeColumns = computed<Column[]>(() =>
  attributeDefinitions.value
    .filter(def => def.enabled)
    .map(def => ({
      key: `attr_${def.id}`,
      label: def.name,
      sortable: false
    }))
)

// Get formatted attribute value for display in table
/** The attribute's text in a row, '' when it has none (the cell then shows the empty "—"). */
const getAttributeValue = (row: LiveUser, attrId: number): string => {
  const userAttrs = row.attribute_values
  if (!userAttrs) return ''
  const value = userAttrs[attrId]
  if (!value) return ''

  // Find definition for this attribute
  const def = attributeDefinitions.value.find(d => d.id === attrId)
  if (!def) return value

  // Format based on type
  if (def.type === 'multi_select' && value) {
    try {
      const arr = JSON.parse(value)
      if (Array.isArray(arr)) {
        // Map values to labels
        return arr.map(v => {
          const opt = def.options?.find(o => o.value === v)
          return opt?.label || v
        }).join(', ')
      }
    } catch {
      return value
    }
  }

  if (def.type === 'select' && value && def.options) {
    const opt = def.options.find(o => o.value === value)
    return opt?.label || value
  }

  return value
}

// All possible columns (for column settings)
const allColumns = computed<Column[]>(() => [
  { key: 'email', label: t('admin.users.columns.user'), sortable: true },
  { key: 'id', label: t('admin.users.columns.id'), sortable: true },
  { key: 'username', label: t('admin.users.columns.username'), sortable: true },
  { key: 'notes', label: t('admin.users.columns.notes'), sortable: false },
  // Dynamic attribute columns
  ...attributeColumns.value,
  { key: 'role', label: t('admin.users.columns.role'), sortable: true },
  { key: 'groups', label: t('admin.users.columns.groups'), sortable: false },
  { key: 'subscriptions', label: t('admin.users.columns.subscriptions'), sortable: false },
  { key: 'balance', label: t('admin.users.columns.balance'), sortable: true },
  { key: 'balance_platform_quota', label: t('admin.users.columns.balancePlatformQuota'), sortable: false },
  { key: 'usage', label: t('admin.users.columns.usage'), sortable: false },
  { key: 'usage_anthropic', label: t('admin.users.columns.usageAnthropic'), sortable: false },
  { key: 'usage_openai', label: t('admin.users.columns.usageOpenAI'), sortable: false },
  { key: 'usage_gemini', label: t('admin.users.columns.usageGemini'), sortable: false },
  { key: 'usage_antigravity', label: t('admin.users.columns.usageAntigravity'), sortable: false },
  { key: 'concurrency', label: t('admin.users.columns.concurrency'), sortable: true },
  { key: 'status', label: t('admin.users.columns.status'), sortable: true, cardHeadEnd: true },
  { key: 'last_active_at', label: t('admin.users.columns.lastActive'), sortable: true },
  { key: 'last_used_at', label: t('admin.users.columns.lastUsed'), sortable: true },
  { key: 'created_at', label: t('admin.users.columns.created'), sortable: true },
  { key: 'actions', label: t('admin.users.columns.actions'), sortable: false }
])

// Columns that can be toggled (exclude email and actions which are always visible)
const toggleableColumns = computed(() =>
  allColumns.value.filter(col => col.key !== 'email' && col.key !== 'actions')
)

// Default hidden columns from 768px (columns hidden by default on first load)
const DEFAULT_HIDDEN_COLUMNS = [
  'notes', 'groups', 'subscriptions', 'usage', 'concurrency',
  'usage_anthropic', 'usage_openai', 'usage_gemini', 'usage_antigravity',
  'balance_platform_quota'
]
const REMOVED_COLUMNS = new Set(['last_login_at'])
// 强制可见列：加载时会被移出隐藏集，并在列设置 UI 上 disabled。
// 当前没有列需要强制可见 —— last_active_at 已改为可被用户隐藏。
const FORCED_VISIBLE_COLUMNS = new Set<string>()

// localStorage keys for column settings: the page's own key is the preference from 768px (mobile list plan, two
// memories); a phone has its own key and defaults.
const HIDDEN_COLUMNS_KEY = 'user-hidden-columns'
const PHONE_HIDDEN_COLUMNS_KEY = 'user-hidden-columns-phone-v1'
/** Below 768px only these show until the user changes them; the rest, attribute columns included, in the settings. */
const PHONE_DEFAULT_COLUMNS = ['balance', 'status', 'last_used_at']
// 列设置 schema 版本号。每次给 DEFAULT_HIDDEN_COLUMNS 新增列时 bump 一次，
// 并在 VERSION_NEW_HIDDEN_COLUMNS 中登记该版本新增的 key。
// 这样老用户升级后这些新列会被自动隐藏一次，而不会影响他们对其它老列的偏好。
const COLUMN_SETTINGS_VERSION_KEY = 'user-column-settings-version'
const COLUMN_SETTINGS_VERSION = 3
const VERSION_NEW_HIDDEN_COLUMNS: Record<number, string[]> = {
  2: ['usage_anthropic', 'usage_openai', 'usage_gemini', 'usage_antigravity'],
  3: ['balance_platform_quota']
}
// A first visit has no saved set: the defaults already carry every version.
try {
  if (localStorage.getItem(HIDDEN_COLUMNS_KEY) === null) localStorage.setItem(COLUMN_SETTINGS_VERSION_KEY, String(COLUMN_SETTINGS_VERSION))
} catch {
  // Storage unavailable: the defaults apply.
}
/** The wide memory's version chain, run once on a saved set: 老用户升级时把未应用过的版本新增的默认隐藏列追加进去。 */
const migrateWideColumns = (hidden: Set<string>) => {
  let mutated = false
  for (const key of [...hidden]) {
    if (REMOVED_COLUMNS.has(key) || FORCED_VISIBLE_COLUMNS.has(key)) {
      hidden.delete(key)
      mutated = true
    }
  }
  const storedVersion = Number(localStorage.getItem(COLUMN_SETTINGS_VERSION_KEY) ?? '1')
  for (let v = storedVersion + 1; v <= COLUMN_SETTINGS_VERSION; v++) {
    for (const key of VERSION_NEW_HIDDEN_COLUMNS[v] ?? []) {
      if (!hidden.has(key)) {
        hidden.add(key)
        mutated = true
      }
    }
  }
  if (storedVersion < COLUMN_SETTINGS_VERSION) localStorage.setItem(COLUMN_SETTINGS_VERSION_KEY, String(COLUMN_SETTINGS_VERSION))
  return mutated
}

const columnVisibility = useColumnVisibility({
  keys: computed(() => toggleableColumns.value.map((col) => col.key)),
  wide: { storageKey: HIDDEN_COLUMNS_KEY, defaultHidden: DEFAULT_HIDDEN_COLUMNS, migrate: migrateWideColumns },
  narrow: { storageKey: PHONE_HIDDEN_COLUMNS_KEY, defaultVisible: PHONE_DEFAULT_COLUMNS },
  onShown: (keys) => loadShownColumns(keys)
})
/** A source skips a hidden column; once it shows again (a toggle, a reset, the other width's memory) it runs now. */
const loadShownColumns = (keys: string[]) => {
  if (keys.some((key) => key === 'usage' || key.startsWith('usage_'))) void liveUsers.refresh('usage')
  if (keys.some((key) => key.startsWith('attr_'))) void liveUsers.refresh('attributes')
  if (keys.includes('balance_platform_quota')) void liveUsers.refresh('quotas')
  if (keys.includes('subscriptions')) void reloadUsers()
  if (keys.includes('groups')) loadAllGroups()
}

// Toggle column visibility
const isForcedVisibleColumn = (key: string) => FORCED_VISIBLE_COLUMNS.has(key)
const toggleColumn = (key: string) => {
  // 强制可见列在加载时会被恢复成可见，这里阻止用户在当前会话隐藏它。
  if (FORCED_VISIBLE_COLUMNS.has(key)) return
  columnVisibility.toggle(key)
  if (key === 'subscriptions' && !isColumnVisible(key)) void reloadUsers()
}
/** This width's columns back to its defaults (the other width keeps its own). */
const resetColumns = () => columnVisibility.reset()

const isColumnVisible = columnVisibility.isVisible
const { phone } = useTableViewport()
const showColumnSheet = ref(false)
// The sheet belongs to a phone: crossing 768px closes it.
watch(phone, (on) => { if (!on) showColumnSheet.value = false })
// usage 主列或任意 usage_<platform> 子列可见时都需要批量拉取用量数据
// 列 key → 平台名（'usage' 主列汇总所有平台时为 null）
// 显式数组取代 Object.keys()：保证迭代顺序（决定列头排序按钮渲染顺序）
// 不会因 JS 引擎差异或 USAGE_COLUMN_PLATFORMS 属性顺序调整而静默变化。
const USAGE_COLUMN_KEYS: readonly string[] = ['usage', 'usage_anthropic', 'usage_openai', 'usage_gemini', 'usage_antigravity']
const USAGE_COLUMN_PLATFORMS: Record<string, string | null> = {
  usage: null,
  usage_anthropic: 'anthropic',
  usage_openai: 'openai',
  usage_gemini: 'gemini',
  usage_antigravity: 'antigravity'
}
const PLATFORM_USAGE_COLUMNS = USAGE_COLUMN_KEYS.filter((k) => k !== 'usage')
const hasVisibleUsageColumn = computed(
  () => isColumnVisible('usage') || PLATFORM_USAGE_COLUMNS.some((k) => isColumnVisible(k))
)
const hasVisibleGroupsColumn = computed(() => isColumnVisible('groups'))
const hasVisiblePlatformQuotaColumn = computed(() => isColumnVisible('balance_platform_quota'))
const hasVisibleAttributeColumns = computed(() =>
  attributeDefinitions.value.some((def) => def.enabled && isColumnVisible(`attr_${def.id}`))
)

// Filtered columns based on visibility
const columns = computed<Column[]>(() =>
  allColumns.value.filter(col =>
    col.key === 'email' || col.key === 'actions' || isColumnVisible(col.key)
  )
)

const searchQuery = ref('')
const USER_SORT_STORAGE_KEY = 'admin-users-table-sort'
const loadInitialSortState = (): { sort_by: string; sort_order: 'asc' | 'desc' } => {
  const fallback = { sort_by: 'created_at', sort_order: 'desc' as 'asc' | 'desc' }
  const sortable = new Set(['email', 'id', 'username', 'role', 'balance', 'concurrency', 'status', 'last_used_at', 'last_active_at', 'created_at'])
  try {
    const raw = localStorage.getItem(USER_SORT_STORAGE_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as { key?: string; order?: string }
    const key = typeof parsed.key === 'string' ? parsed.key : ''
    if (!sortable.has(key)) return fallback
    return {
      sort_by: key,
      sort_order: parsed.order === 'asc' ? 'asc' : 'desc'
    }
  } catch {
    return fallback
  }
}
const sortState = reactive(loadInitialSortState())
/** The page's sort, as the table shows it (it owns the sort: the headers follow this). */
const tableSort = computed(() => ({ key: sortState.sort_by, order: sortState.sort_order }))

// Groups data for the groups column and the existing "authorised group" filter (active only)
const allGroups = ref<AdminGroup[]>([])
const loadAllGroups = async () => {
  if (allGroups.value.length > 0) return
  try {
    allGroups.value = await adminAPI.groups.getAll()
  } catch (e) {
    console.error('Failed to load groups:', e)
  }
}

// Groups for the API Key group filter — includes disabled groups so admins can
// filter users whose keys are still bound to a now-disabled group.
const allGroupsForApiKeyFilter = ref<AdminGroup[]>([])
const loadAllGroupsForApiKeyFilter = async () => {
  if (allGroupsForApiKeyFilter.value.length > 0) return
  try {
    allGroupsForApiKeyFilter.value = await adminAPI.groups.getAllIncludingInactive()
  } catch (e) {
    console.error('Failed to load groups for API key filter:', e)
  }
}
// Resolve user's accessible groups: exclusive groups first, then public groups
const getUserGroups = (user: AdminUser) => {
  const exclusive: AdminGroup[] = []
  const publicGroups: AdminGroup[] = []
  for (const g of allGroups.value) {
    if (g.status !== 'active' || g.subscription_type !== 'standard') continue
    if (g.is_exclusive) {
      if (user.allowed_groups?.includes(g.id)) {
        exclusive.push(g)
      }
    } else {
      publicGroups.push(g)
    }
  }
  return { exclusive, publicGroups }
}

const roleFilterOptions = computed(() => [
  { value: '', label: t('admin.users.allRoles') },
  { value: 'admin', label: t('admin.users.admin') },
  { value: 'operator', label: t('admin.users.operator') },
  { value: 'user', label: t('admin.users.user') }
])
const statusFilterOptions = computed(() => [
  { value: '', label: t('admin.users.allStatus') },
  { value: 'active', label: t('common.active') },
  { value: 'disabled', label: t('admin.users.disabled') }
])

// Group filter options: "All Groups" + active exclusive groups (value = group name for fuzzy match)
const groupFilterOptions = computed(() => {
  const options: { value: string; label: string }[] = [
    { value: '', label: t('admin.users.allAuthorizedGroups') }
  ]
  for (const g of allGroups.value) {
    if (g.status !== 'active' || !g.is_exclusive || g.subscription_type !== 'standard') continue
    options.push({ value: g.name, label: g.name })
  }
  return options
})

// API Key group filter options: "All" + groups partitioned by type (value = group id).
// Uses allGroupsForApiKeyFilter which includes disabled groups.
const apiKeyGroupFilterOptions = computed(() =>
  buildApiKeyGroupFilterOptions(allGroupsForApiKeyFilter.value, {
    all: t('admin.users.allApiKeyGroups'),
    exclusive: t('admin.users.apiKeyGroupExclusive'),
    public: t('admin.users.apiKeyGroupPublic'),
    subscription: t('admin.users.apiKeyGroupSubscription'),
    disabled: t('admin.users.apiKeyGroupDisabled'),
  }) as SelectOption[]
)

// Filter values (role, status, and custom attributes)
const filters = reactive({
  role: '',
  status: '',
  group: '',  // group name for fuzzy match, '' = all
  apiKeyGroup: null as number | null  // group id bound to the user's API keys, null = all
})
const activeAttributeFilters = reactive<Record<number, string>>({})

// Visible filters tracking (which filters are shown in the UI)
// Keys: 'role', 'status', 'attr_${id}'
const visibleFilters = reactive<Set<string>>(new Set())

// Dropdown states
const showFilterDropdown = ref(false)
const showColumnDropdown = ref(false)

// Dropdown refs for click outside detection
const filterDropdownRef = ref<HTMLElement | null>(null)
const columnDropdownRef = ref<HTMLElement | null>(null)

// localStorage keys
const FILTER_VALUES_KEY = 'user-filter-values'
const VISIBLE_FILTERS_KEY = 'user-visible-filters'

// All filterable attribute definitions (enabled attributes)
const filterableAttributes = computed(() =>
  attributeDefinitions.value.filter(def => def.enabled)
)

// Built-in filter definitions
const builtInFilters = computed(() => [
  { key: 'role', name: t('admin.users.columns.role'), type: 'select' as const },
  { key: 'status', name: t('admin.users.columns.status'), type: 'select' as const },
  { key: 'group', name: t('admin.users.authorizedGroupFilter'), type: 'select' as const },
  { key: 'apiKeyGroup', name: t('admin.users.apiKeyGroupFilter'), type: 'select' as const }
])

// Load saved filters from localStorage
const loadSavedFilters = () => {
  try {
    // Load visible filters
    const savedVisible = localStorage.getItem(VISIBLE_FILTERS_KEY)
    if (savedVisible) {
      const parsed = JSON.parse(savedVisible) as string[]
      parsed.forEach(key => visibleFilters.add(key))
    }
    // Load filter values
    const savedValues = localStorage.getItem(FILTER_VALUES_KEY)
    if (savedValues) {
      const parsed = JSON.parse(savedValues)
      if (parsed.role) filters.role = parsed.role
      if (parsed.status) filters.status = parsed.status
      if (parsed.group) filters.group = parsed.group
      if (typeof parsed.apiKeyGroup === 'number') filters.apiKeyGroup = parsed.apiKeyGroup
      if (parsed.attributes) {
        Object.assign(activeAttributeFilters, parsed.attributes)
      }
    }
  } catch (e) {
    console.error('Failed to load saved filters:', e)
  }
}

// Save filters to localStorage
const saveFiltersToStorage = () => {
  try {
    // Save visible filters
    localStorage.setItem(VISIBLE_FILTERS_KEY, JSON.stringify([...visibleFilters]))
    // Save filter values
    const values = {
      role: filters.role,
      status: filters.status,
      group: filters.group,
      apiKeyGroup: filters.apiKeyGroup,
      attributes: activeAttributeFilters
    }
    localStorage.setItem(FILTER_VALUES_KEY, JSON.stringify(values))
  } catch (e) {
    console.error('Failed to save filters:', e)
  }
}

// Get attribute definition by ID
const getAttributeDefinition = (attrId: number): UserAttributeDefinition | undefined => {
  return attributeDefinitions.value.find(d => d.id === attrId)
}
const getPlatformUsage = (row: LiveUser, platform: string) =>
  row.usage_stats?.by_platform?.find((p) => p.platform === platform)

// 用量列前端排序：DataTable 工作在 server-side-sort 模式，所有 sortable
// 字段都会触发后端查询，而用量列数据是异步批量拉取后再合并到当前页，
// 因此采用独立的前端排序状态对当前页 users 做本地排序。
// 排序状态独立于后端 sortState 持久化；缺失数据按 0 处理（desc 沉底、asc 置顶）。
type UsageMetric = 'today' | 'total'
type UsageSortState = { key: string; metric: UsageMetric; order: 'asc' | 'desc' } | null
const USAGE_SORT_STORAGE_KEY = 'admin-users-usage-sort'
// 列头排序按钮点击后弹出的"今日/近30天"选择菜单，同时只允许一个列展开。
const openUsageSortMenu = ref<string | null>(null)

const loadInitialUsageSort = (): UsageSortState => {
  try {
    const raw = localStorage.getItem(USAGE_SORT_STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<{ key: string; metric: string; order: string }>
    if (!parsed.key || !USAGE_COLUMN_KEYS.includes(parsed.key)) return null
    const metric: UsageMetric = parsed.metric === 'total' ? 'total' : 'today'
    const order: 'asc' | 'desc' = parsed.order === 'asc' ? 'asc' : 'desc'
    return { key: parsed.key, metric, order }
  } catch {
    return null
  }
}
const usageSort = ref<UsageSortState>(loadInitialUsageSort())
const persistUsageSort = () => {
  try {
    if (usageSort.value) {
      localStorage.setItem(USAGE_SORT_STORAGE_KEY, JSON.stringify(usageSort.value))
    } else {
      localStorage.removeItem(USAGE_SORT_STORAGE_KEY)
    }
  } catch (e) {
    console.error('Failed to persist usage sort:', e)
  }
}
const clearUsageSort = () => {
  if (!usageSort.value) return
  usageSort.value = null
  openUsageSortMenu.value = null
  persistUsageSort()
}

const isUsageSortActive = (key: string, metric: UsageMetric) =>
  !!usageSort.value && usageSort.value.key === key && usageSort.value.metric === metric
const getUsageSortOrder = (key: string, metric: UsageMetric): 'asc' | 'desc' | null =>
  isUsageSortActive(key, metric) ? usageSort.value!.order : null

// 三态循环：desc → asc → off。选完即关闭菜单（用户大多希望"选中即应用"，
// 想再切换 order 时重新打开菜单点同一项即可）。
const toggleUsageSort = (key: string, metric: UsageMetric) => {
  const cur = usageSort.value
  if (cur && cur.key === key && cur.metric === metric) {
    usageSort.value = cur.order === 'desc' ? { key, metric, order: 'asc' } : null
  } else {
    usageSort.value = { key, metric, order: 'desc' }
  }
  persistUsageSort()
  openUsageSortMenu.value = null
}

// 点击图标本身不触发排序，仅开关菜单；首次排序由用户在菜单内选择 metric 触发（默认 desc，详见 toggleUsageSort）。
const toggleUsageSortMenu = (key: string) => {
  openUsageSortMenu.value = openUsageSortMenu.value === key ? null : key
}

const getUsageValue = (row: LiveUser, key: string, metric: UsageMetric): number => {
  const stats = row.usage_stats
  if (!stats) return 0
  const platform = USAGE_COLUMN_PLATFORMS[key]
  if (platform === null) {
    return metric === 'today' ? stats.today_actual_cost ?? 0 : stats.total_actual_cost ?? 0
  }
  const p = stats.by_platform?.find((x) => x.platform === platform)
  if (!p) return 0
  return metric === 'today' ? p.today_actual_cost ?? 0 : p.total_actual_cost ?? 0
}

// The current-page usage sort is the live engine's local sort: it runs when the user picks it, on every later page
// and once more when a new page's usage lands; passive updates never re-order (spec 8.4a). Rows whose usage has not
// arrived count as 0; equal values keep the server's order.
const usageComparator = (s: NonNullable<UsageSortState>) => (a: LiveUser, b: LiveUser) => {
  const av = getUsageValue(a, s.key, s.metric)
  const bv = getUsageValue(b, s.key, s.metric)
  return s.order === 'asc' ? av - bv : bv - av
}


const handleSelectedKeysUpdate = (keys: Array<string | number>) => {
  const ids = keys.filter((key): key is number => typeof key === 'number')
  // Operators never select rows they cannot act on (admins, operators, themselves),
  // so bulk actions never send a target the backend refuses.
  setSelectedIds(isSuperAdmin.value ? ids : ids.filter((id) => {
    const row = users.value.find((user) => user.id === id)
    return !row || canActOn(row)
  }))
}

const getUserSelectionLabel = (user: AdminUser) =>
  t('admin.users.bulkLimits.selectUser', { email: user.email })

// User attribute definitions and values
const attributeDefinitions = ref<UserAttributeDefinition[]>([])
const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize()
})

const showCreateModal = ref(false)
const showEditModal = ref(false)
const showBulkEditModal = ref(false)
const showDeleteDialog = ref(false)
const bulkDeleteIds = ref<number[]>([])
const bulkDeleting = ref(false)
const bulkStatusTarget = ref<'active' | 'disabled' | null>(null)
const bulkStatusIds = ref<number[]>([])
const bulkStatusUpdating = ref(false)
const showApiKeysModal = ref(false)
const showAttributesModal = ref(false)
const showPlatformQuotaModal = ref(false)
const editingUser = ref<AdminUser | null>(null)
const deletingUser = ref<AdminUser | null>(null)
const viewingUser = ref<AdminUser | null>(null)
const platformQuotaUser = ref<AdminUser | null>(null)

const handlePlatformQuota = (user: AdminUser) => {
  if (!canReadRow(user)) return
  platformQuotaUser.value = user
  showPlatformQuotaModal.value = true
}

const closePlatformQuotaModal = () => {
  showPlatformQuotaModal.value = false
  platformQuotaUser.value = null
}
// The list is a live table (spec 8.5): balance, current concurrency and last activity come from the page fetched again
// with the same query every 10 s, which also counts rows that appeared or went (liveUsers.pending: the changes pill,
// taken in by liveUsers.applyPending). Usage is a 30 s partial source; platform quotas (one request per user) and
// attribute values run after each load and on refresh only. The 10 s re-fetch of the current page is also the
// membership check: under a fast-changing server-side sort a row may drift off the current page between two checks,
// and it shows dimmed until the next active load.
type LiveUser = AdminUser & {
  usage_stats?: BatchUserUsageStats
  platform_quotas?: PlatformQuotaItem[]
  attribute_values?: Record<number, string>
}
const userListFilters = () => {
  const attrFilters: Record<number, string> = {}
  for (const [attrId, value] of Object.entries(activeAttributeFilters)) {
    if (value) attrFilters[Number(attrId)] = value
  }
  return {
    role: filters.role as any,
    status: filters.status as any,
    search: searchQuery.value || undefined,
    group_name: filters.group || undefined,
    api_key_group_id: filters.apiKeyGroup ?? undefined,
    attributes: Object.keys(attrFilters).length > 0 ? attrFilters : undefined,
    // 始终请求 subscriptions：列隐藏时仍需用于 UserPlatformQuotaModal 的 active-subscription 警示 banner
    include_subscriptions: true,
    sort_by: sortState.sort_by,
    sort_order: sortState.sort_order
  }
}
// Operators only read the users they may view (not admins, other operators or themselves).
const readableIds = (ids: number[]) => {
  if (isSuperAdmin.value) return ids
  const readable = new Set(users.value.filter(canReadRow).map((user) => user.id))
  return ids.filter((id) => readable.has(id))
}
/** Page count as the server reports it, for keeping a page change in range. */
let serverPages = 0
const liveUsers = useLiveTable<LiveUser>({
  getId: (user) => user.id,
  fetchPage: async (signal) => {
    const page = await adminAPI.users.list(pagination.page, pagination.page_size, userListFilters(), { signal })
    serverPages = page.pages
    return page
  },
  membershipIntervalMs: 10_000,
  // A row whose status or role no longer matches the filter stays in place, dimmed.
  matches: (user) => (!filters.status || user.status === filters.status) && (!filters.role || user.role === filters.role),
  sources: [
    {
      name: 'usage',
      intervalMs: 30_000,
      fetch: async (ids) => {
        if (!hasVisibleUsageColumn.value) return []
        const userIds = ids.map(Number)
        const { stats } = await adminAPI.dashboard.getBatchUsersUsage(userIds)
        return userIds.map((id) => [id, { usage_stats: userUsageOrDefault(id, stats?.[id]) }] as const)
      }
    },
    {
      name: 'quotas',
      intervalMs: 0,
      fetch: async (ids, signal) => {
        if (!hasVisiblePlatformQuotaColumn.value) return []
        // 无批量端点：逐个拉取，分块并发（每批 6），批间检查中止，避免大 pageSize 时请求洪峰
        const quotaIds = readableIds(ids.map(Number))
        const patches: Array<readonly [number, Partial<LiveUser>]> = []
        const CHUNK = 6
        for (let i = 0; i < quotaIds.length && !signal.aborted; i += CHUNK) {
          const chunk = quotaIds.slice(i, i + CHUNK)
          const results = await Promise.allSettled(chunk.map((id) => adminAPI.users.getPlatformQuotas(id)))
          results.forEach((r, idx) => {
            if (r.status === 'fulfilled') patches.push([chunk[idx], { platform_quotas: r.value.platform_quotas || [] }])
          })
        }
        return patches
      }
    },
    {
      name: 'attributes',
      intervalMs: 0,
      fetch: async (ids): Promise<LivePatches<LiveUser>> => {
        if (attributeDefinitions.value.length === 0 || !hasVisibleAttributeColumns.value) return []
        const attributeUserIds = readableIds(ids.map(Number))
        if (attributeUserIds.length === 0) return []
        const response = await adminAPI.userAttributes.getBatchUserAttributes(attributeUserIds)
        const values = response.attributes ?? {}
        return attributeUserIds.map((id) => [id, { attribute_values: values[id] ?? {} }] as const)
      }
    }
  ]
})
const { rows: users, loading, total: usersTotal, pending, status: liveStatus } = liveUsers
const changesText = (n: string) => t('common.pendingChanges.changes', { n })
const {
  selectedIds,
  selectedCount,
  setSelectedIds,
  clear: clearSelection,
  removeMany: removeSelectedIds
} = useTableSelection<AdminUser>({
  rows: users,
  getId: (user) => user.id
})
const usersTableRef = ref<InstanceType<typeof DataTable> | null>(null)

// The toolbar's dropdown rows in the popover material (12.6). Filter and column settings are multi-select lists: a
// ticked row is told by its brand check alone, without the selected background or text colour (12.6-5).
const POPOVER_ITEM = 'flex w-full items-center justify-between px-4 py-2 text-left text-sm'
const POPOVER_ITEM_TEXT = 'text-gray-700 hover:bg-surface-popover-hover dark:text-gray-300'
const BULK_ITEM = 'flex w-full items-center px-4 py-2 text-left text-sm text-gray-700 hover:bg-surface-popover-hover disabled:cursor-not-allowed disabled:opacity-50 dark:text-gray-200'
// The danger item (delete): the error text, on a light red under the pointer and keyboard focus (12.12-6).
const BULK_DANGER = '!text-status-error-text hover:!bg-surface-popover-danger-hover focus-visible:!bg-surface-popover-danger-hover'
// The bulk menu is a sub-layer like the row menu: Escape closes it and gives focus back to its button, a dialog it
// opens closes it, a press outside closes it (handleClickOutside). It closes when the last row is unticked.
const bulkMenuOpen = ref(false)
const bulkRootRef = ref<HTMLElement | null>(null)
const bulkButtonRef = ref<HTMLButtonElement | null>(null)
const bulkMenuRef = ref<HTMLElement | null>(null)
const bulkMenuLayer = useOverlayLayer(bulkMenuOpen, {
  root: bulkMenuRef,
  modal: false,
  floor: 9999,
  onEscape: () => {
    bulkMenuOpen.value = false
    bulkButtonRef.value?.focus({ preventScroll: true })
  },
  onCovered: () => { bulkMenuOpen.value = false }
})
watch(selectedCount, (count) => {
  if (count === 0) bulkMenuOpen.value = false
})
const userRowClass = (row: LiveUser) =>
  liveUsers.transitioning.value || liveUsers.isDimmed(row.id) ? 'opacity-60' : undefined
watch(usageSort, (sort, previous) => {
  // A usage sort the user picks starts from the first row (spec 8.4a); the first run applies the stored one.
  if (previous !== undefined) usersTableRef.value?.scrollToTop?.()
  liveUsers.sortBy(sort ? usageComparator(sort) : null)
}, { immediate: true })

const showLoadError = (error: any) => {
  appStore.showError(error?.response?.data?.detail || error?.message || t('admin.users.failedToLoad'))
  console.error('Error loading users:', error)
}
/** A silent reload: the rows stay on screen and are replaced in place. */
const reloadUsers = () => liveUsers.reload().catch(showLoadError)
const applyPending = () => liveUsers.applyPending().catch(showLoadError)
/** The manual refresh button. */
const refreshUsers = () => reloadUsers()
/** A filter or search change: back to the first row, then reload. */
const applyUserFilters = () => {
  usersTableRef.value?.scrollToTop()
  return reloadUsers()
}

// Action Menu State
const activeMenuId = ref<number | null>(null)
const menuPosition = ref<{ top: number; left: number } | null>(null)
// A sub-layer of the overlay stack (design spec 0.2): while open, Escape closes it before the layer or page
// below, re-renders below keep their hands off its focus, and a dialog opening above closes it.
const actionMenuRef = ref<HTMLElement | null>(null)
const actionMenuLayer = useOverlayLayer(() => activeMenuId.value !== null && !!menuPosition.value, { root: actionMenuRef, modal: false, floor: 9999, onEscape: () => closeActionMenu() })

const openActionMenu = (user: AdminUser, e: MouseEvent) => {
  if (activeMenuId.value === user.id) {
    closeActionMenu()
  } else {
    const target = e.currentTarget as HTMLElement
    if (!target) {
      closeActionMenu()
      return
    }

    const rect = target.getBoundingClientRect()
    const menuWidth = 200
    const menuHeight = 240
    const padding = 8
    const viewportWidth = window.innerWidth
    const viewportHeight = window.innerHeight

    let left, top

    if (viewportWidth < 768) {
      // 居中显示,水平位置
      left = Math.max(padding, Math.min(
        rect.left + rect.width / 2 - menuWidth / 2,
        viewportWidth - menuWidth - padding
      ))

      // 优先显示在按钮下方
      top = rect.bottom + 4

      // 如果下方空间不够,显示在上方
      if (top + menuHeight > viewportHeight - padding) {
        top = rect.top - menuHeight - 4
        // 如果上方也不够,就贴在视口顶部
        if (top < padding) {
          top = padding
        }
      }
    } else {
      left = Math.max(padding, Math.min(
        e.clientX - menuWidth,
        viewportWidth - menuWidth - padding
      ))
      top = e.clientY
      if (top + menuHeight > viewportHeight - padding) {
        top = viewportHeight - menuHeight - padding
      }
    }

    menuPosition.value = { top, left }
    activeMenuId.value = user.id
  }
}

const closeActionMenu = () => {
  activeMenuId.value = null
  menuPosition.value = null
}

// Close menu when clicking outside
const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as HTMLElement
  if (!target.closest('.action-menu-trigger') && !target.closest('.action-menu-content')) {
    closeActionMenu()
  }
  // Close filter dropdown when clicking outside
  if (filterDropdownRef.value && !filterDropdownRef.value.contains(target)) {
    showFilterDropdown.value = false
  }
  // Close column dropdown when clicking outside
  if (columnDropdownRef.value && !columnDropdownRef.value.contains(target)) {
    showColumnDropdown.value = false
  }
  if (bulkRootRef.value && !bulkRootRef.value.contains(target)) {
    bulkMenuOpen.value = false
  }
  // Close usage sort dropdown when clicking outside any usage-sort-trigger
  if (openUsageSortMenu.value !== null && !target.closest('.usage-sort-trigger')) {
    openUsageSortMenu.value = null
  }
  // Close expanded group dropdown when clicking outside
  if (expandedGroupUserId.value !== null) {
    expandedGroupUserId.value = null
  }
}

// Allowed groups modal state
const showAllowedGroupsModal = ref(false)
const allowedGroupsUser = ref<AdminUser | null>(null)

// Expanded group dropdown state (click to show exclusive groups list)
const expandedGroupUserId = ref<number | null>(null)
const toggleExpandedGroup = (userId: number) => {
  expandedGroupUserId.value = expandedGroupUserId.value === userId ? null : userId
}

// Group replace modal state
const showGroupReplaceModal = ref(false)
const groupReplaceUser = ref<AdminUser | null>(null)
const groupReplaceOldGroup = ref<{ id: number; name: string } | null>(null)

// Balance (Deposit/Withdraw) modal state
const showBalanceModal = ref(false)
const balanceUser = ref<AdminUser | null>(null)
const balanceOperation = ref<'add' | 'subtract'>('add')

// Balance History modal state
const showBalanceHistoryModal = ref(false)
const balanceHistoryUser = ref<AdminUser | null>(null)

// 计算剩余天数
const getDaysRemaining = (expiresAt: string): number => {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diffMs = expires.getTime() - now.getTime()
  return Math.ceil(diffMs / (1000 * 60 * 60 * 24))
}

const loadAttributeDefinitions = async () => {
  try {
    attributeDefinitions.value = await adminAPI.userAttributes.listEnabledDefinitions()
  } catch (e) {
    console.error('Failed to load attribute definitions:', e)
  }
}

// Handle attributes modal close - reload definitions and users
const handleAttributesModalClose = async () => {
  showAttributesModal.value = false
  await loadAttributeDefinitions()
  void reloadUsers()
}

const handleBulkLimitsSuccess = async () => {
  clearSelection()
  await reloadUsers()
}

let searchTimeout: ReturnType<typeof setTimeout>
const handleSearch = () => {
  clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => {
    pagination.page = 1
    void applyUserFilters()
  }, 300)
}

const handlePageChange = (page: number) => {
  // 确保页码在有效范围内
  const validPage = Math.max(1, Math.min(page, serverPages || 1))
  pagination.page = validPage
  void reloadUsers()
}

const handlePageSizeChange = (pageSize: number) => {
  pagination.page_size = pageSize
  pagination.page = 1
  void reloadUsers()
}

const handleSort = (key: string, order: 'asc' | 'desc') => {
  clearUsageSort()
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  void reloadUsers()
}

// Filter helpers
const getAttributeDefinitionName = (attrId: number): string => {
  const def = attributeDefinitions.value.find(d => d.id === attrId)
  return def?.name || String(attrId)
}

// Toggle a built-in filter (role/status)
const toggleBuiltInFilter = (key: string) => {
  if (visibleFilters.has(key)) {
    visibleFilters.delete(key)
    if (key === 'role') filters.role = ''
    if (key === 'status') filters.status = ''
    if (key === 'group') filters.group = ''
    if (key === 'apiKeyGroup') filters.apiKeyGroup = null
  } else {
    visibleFilters.add(key)
    if (key === 'group') loadAllGroups()
    if (key === 'apiKeyGroup') loadAllGroupsForApiKeyFilter()
  }
  saveFiltersToStorage()
  pagination.page = 1
  void applyUserFilters()
}

// Toggle a custom attribute filter
const toggleAttributeFilter = (attr: UserAttributeDefinition) => {
  const key = `attr_${attr.id}`
  if (visibleFilters.has(key)) {
    visibleFilters.delete(key)
    delete activeAttributeFilters[attr.id]
  } else {
    visibleFilters.add(key)
    activeAttributeFilters[attr.id] = ''
  }
  saveFiltersToStorage()
  pagination.page = 1
  void applyUserFilters()
}

const updateAttributeFilter = (attrId: number, value: string) => {
  activeAttributeFilters[attrId] = value
}

// Apply filter and save to localStorage
const applyFilter = () => {
  saveFiltersToStorage()
  void applyUserFilters()
}

// ---- A phone's filter sheet and tags (mobile list plan) ----
type SheetFilterKey = 'role' | 'status' | 'group' | 'apiKeyGroup'
/** A filter set from the sheet also shows from 768px, so a chosen filter is never hidden there. */
const showFilter = (key: string) => {
  if (visibleFilters.has(key)) return
  visibleFilters.add(key)
  if (key === 'group') loadAllGroups()
  if (key === 'apiKeyGroup') loadAllGroupsForApiKeyFilter()
}
const setSheetFilter = (key: SheetFilterKey, value: string | number | boolean | null) => {
  if (key === 'apiKeyGroup') filters.apiKeyGroup = value === '' || value === null || typeof value === 'boolean' ? null : Number(value)
  else filters[key] = value === null || typeof value === 'boolean' ? '' : String(value)
  showFilter(key)
  pagination.page = 1
  applyFilter()
}
const setSheetAttribute = (attrId: number, value: string, apply: boolean) => {
  showFilter(`attr_${attrId}`)
  updateAttributeFilter(attrId, value)
  if (!apply) return
  pagination.page = 1
  applyFilter()
}
const optionLabel = (options: { value: unknown; label: string }[], value: unknown) =>
  options.find((option) => option.value === value)?.label ?? String(value)
/** The chosen filters: the "Filters n" count and the phone's removable tags. */
const filterTags = computed(() => {
  const tags: { key: string; label: string; text: string }[] = []
  if (filters.role) tags.push({ key: 'role', label: t('admin.users.columns.role'), text: optionLabel(roleFilterOptions.value, filters.role) })
  if (filters.status) tags.push({ key: 'status', label: t('admin.users.columns.status'), text: optionLabel(statusFilterOptions.value, filters.status) })
  if (filters.group) tags.push({ key: 'group', label: t('admin.users.authorizedGroupFilter'), text: filters.group })
  if (filters.apiKeyGroup !== null) tags.push({ key: 'apiKeyGroup', label: t('admin.users.apiKeyGroupFilter'), text: optionLabel(apiKeyGroupFilterOptions.value, filters.apiKeyGroup) })
  for (const [id, value] of Object.entries(activeAttributeFilters)) {
    if (!value) continue
    const def = getAttributeDefinition(Number(id))
    tags.push({ key: `attr_${id}`, label: getAttributeDefinitionName(Number(id)), text: def?.options ? optionLabel(def.options, value) : value })
  }
  return tags
})
const clearFilterValue = (key: string) => {
  if (key === 'apiKeyGroup') filters.apiKeyGroup = null
  else if (key === 'role' || key === 'status' || key === 'group') filters[key] = ''
  else if (key.startsWith('attr_')) activeAttributeFilters[Number(key.slice(5))] = ''
}
const removeFilterTag = (key: string) => {
  clearFilterValue(key)
  pagination.page = 1
  applyFilter()
}
/** The sheet's "Clear all": every chosen value goes, the filters stay where they are shown. */
const clearAllFilters = () => {
  for (const tag of filterTags.value) clearFilterValue(tag.key)
  pagination.page = 1
  applyFilter()
}
const columnLabel = (key: string) => allColumns.value.find((column) => column.key === key)?.label ?? key
/**
 * The sort, in the toolbar while its column is hidden (a phone's defaults hide "created"): the page's own usage sort
 * when it is on, else the server's.
 */
const sortHint = computed(() => {
  const usage = usageSort.value
  if (usage) {
    if (isColumnVisible(usage.key)) return null
    return { label: `${columnLabel(usage.key)} · ${usage.metric === 'today' ? t('admin.users.today') : t('admin.users.total')}`, order: usage.order }
  }
  return columns.value.some((column) => column.key === sortState.sort_by) ? null : { label: columnLabel(sortState.sort_by), order: sortState.sort_order }
})
// A phone's sheet offers the group filters: their options load once the phone toolbar shows.
watch(phone, (on) => {
  if (!on) return
  if (!allGroups.value.length) loadAllGroups()
  if (!allGroupsForApiKeyFilter.value.length) loadAllGroupsForApiKeyFilter()
}, { immediate: true })

const handleEdit = (user: AdminUser) => {
  editingUser.value = user
  showEditModal.value = true
}

const closeEditModal = () => {
  showEditModal.value = false
  editingUser.value = null
}

const handleToggleStatus = async (user: AdminUser) => {
  const newStatus = user.status === 'active' ? 'disabled' : 'active'
  try {
    // The row shows the new status at once and the field is locked: no list answer sent before the change lands
    // writes it; a failure rolls it back.
    await liveUsers.edit(user.id, 'status', newStatus, async () => {
      const updated = await adminAPI.users.toggleStatus(user.id, newStatus)
      // 更新接口的响应不含 current_concurrency、subscriptions 等列表专属字段，只回写状态相关字段。
      return { status: updated.status, updated_at: updated.updated_at }
    })
    appStore.showSuccess(
      newStatus === 'active' ? t('admin.users.userEnabled') : t('admin.users.userDisabled')
    )
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.failedToToggle'))
    console.error('Error toggling user status:', error)
  }
}

const handleViewApiKeys = (user: AdminUser) => {
  viewingUser.value = user
  showApiKeysModal.value = true
}

const closeApiKeysModal = () => {
  showApiKeysModal.value = false
  viewingUser.value = null
}

const handleAllowedGroups = (user: AdminUser) => {
  allowedGroupsUser.value = user
  showAllowedGroupsModal.value = true
}

const closeAllowedGroupsModal = () => {
  showAllowedGroupsModal.value = false
  allowedGroupsUser.value = null
}

const openGroupReplace = (user: AdminUser, group: { id: number; name: string }) => {
  expandedGroupUserId.value = null
  groupReplaceUser.value = user
  groupReplaceOldGroup.value = group
  showGroupReplaceModal.value = true
}

const closeGroupReplaceModal = () => {
  showGroupReplaceModal.value = false
  groupReplaceUser.value = null
  groupReplaceOldGroup.value = null
}

const handleDelete = (user: AdminUser) => {
  deletingUser.value = user
  showDeleteDialog.value = true
}

const confirmDelete = async () => {
  if (!deletingUser.value) return
  try {
    const deletedId = deletingUser.value.id
    await adminAPI.users.delete(deletedId)
    appStore.showSuccess(t('common.success'))
    showDeleteDialog.value = false
    deletingUser.value = null
    liveUsers.remove(deletedId)
    removeSelectedIds([deletedId])
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.failedToDelete'))
    console.error('Error deleting user:', error)
  }
}

const confirmBulkDelete = async () => {
  const ids = bulkDeleteIds.value
  bulkDeleteIds.value = []
  bulkDeleting.value = true
  const deletedIds: number[] = []
  for (const id of ids) {
    try {
      await adminAPI.users.delete(id)
      deletedIds.push(id)
      liveUsers.remove(id) // off the page at once; the reload below follows
    } catch (error) {
      console.error('Error deleting user:', error)
    }
  }
  removeSelectedIds(deletedIds)
  if (deletedIds.length > 0) {
    appStore.showSuccess(t('admin.users.bulkDelete.success', { count: deletedIds.length }))
    pagination.page = 1
  }
  const failed = ids.length - deletedIds.length
  if (failed > 0) appStore.showError(t('admin.users.bulkDelete.failed', { count: failed }))
  await reloadUsers()
  bulkDeleting.value = false
}

// 与后端 batch-status 的单次上限一致。
const MAX_BULK_STATUS_USER_IDS = 500

const openBulkStatus = (status: 'active' | 'disabled') => {
  if (selectedIds.value.length > MAX_BULK_STATUS_USER_IDS) {
    appStore.showError(t('admin.users.bulkLimits.selectionLimit', { max: MAX_BULK_STATUS_USER_IDS }))
    return
  }
  bulkStatusIds.value = [...selectedIds.value]
  bulkStatusTarget.value = status
}

const closeBulkStatus = () => {
  bulkStatusTarget.value = null
  bulkStatusIds.value = []
}

const confirmBulkStatus = async () => {
  const status = bulkStatusTarget.value
  const ids = bulkStatusIds.value
  closeBulkStatus()
  if (!status || ids.length === 0) return
  bulkStatusUpdating.value = true
  try {
    const { affected, skipped } = await adminAPI.users.batchUpdateStatus({ user_ids: ids, status })
    appStore.showSuccess(
      status === 'disabled'
        ? t('admin.users.bulkStatus.disableSuccess', { count: affected })
        : t('admin.users.bulkStatus.enableSuccess', { count: affected })
    )
    if (skipped > 0) appStore.showInfo(t('admin.users.bulkStatus.skipped', { count: skipped }))
    clearSelection()
    await reloadUsers()
  } catch (error) {
    // 失败时保留选中，便于直接重试。
    appStore.showError(extractApiErrorMessage(error, t('admin.users.bulkStatus.failed')))
    console.error('Error updating user status in bulk:', error)
  } finally {
    bulkStatusUpdating.value = false
  }
}

const handleDeposit = (user: AdminUser) => {
  balanceUser.value = user
  balanceOperation.value = 'add'
  showBalanceModal.value = true
}

const handleWithdraw = (user: AdminUser) => {
  balanceUser.value = user
  balanceOperation.value = 'subtract'
  showBalanceModal.value = true
}

const closeBalanceModal = () => {
  showBalanceModal.value = false
  balanceUser.value = null
}

const handleBalanceHistory = (user: AdminUser) => {
  if (!canReadRow(user)) return
  balanceHistoryUser.value = user
  showBalanceHistoryModal.value = true
}

const closeBalanceHistoryModal = () => {
  showBalanceHistoryModal.value = false
  balanceHistoryUser.value = null
}

// Handle deposit from balance history modal
const handleDepositFromHistory = () => {
  if (balanceHistoryUser.value) {
    handleDeposit(balanceHistoryUser.value)
  }
}

// Handle withdraw from balance history modal
const handleWithdrawFromHistory = () => {
  if (balanceHistoryUser.value) {
    handleWithdraw(balanceHistoryUser.value)
  }
}

// 滚动时关闭菜单
const handleScroll = () => {
  closeActionMenu()
}

onMounted(async () => {
  await loadAttributeDefinitions()
  loadSavedFilters()
  void reloadUsers()
  if (hasVisibleGroupsColumn.value || visibleFilters.has('group')) {
    loadAllGroups()
  }
  if (visibleFilters.has('apiKeyGroup')) {
    loadAllGroupsForApiKeyFilter()
  }
  document.addEventListener('click', handleClickOutside)
  window.addEventListener('scroll', handleScroll, true)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
  window.removeEventListener('scroll', handleScroll, true)
  clearTimeout(searchTimeout)
})
</script>

<style scoped>
/* A phone's filter sheet: one field per filter, its name above it. */
.users-sheet__field {
  display: grid;
  gap: 6px;
  margin-bottom: 16px;
}
.users-sheet__k {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
}
/* The chosen filters as tags on a phone, removed with their "x". */
.users-tag {
  display: inline-flex;
  max-width: 100%;
  min-height: 32px;
  align-items: center;
  gap: 2px;
  padding-left: 10px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-full);
  background: var(--color-bg-control);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
}
.users-tag__k {
  color: var(--color-text-secondary);
}
.users-tag__v {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.users-tag__x {
  display: inline-grid;
  width: 32px;
  height: 32px;
  flex: none;
  place-items: center;
  border-radius: var(--radius-full);
  color: var(--color-text-muted);
}
@media (pointer: coarse) {
  .users-tag {
    min-height: 40px;
  }
  .users-tag__x {
    width: 40px;
    height: 40px;
  }
}
/* A phone's column settings: one row per column, 44px to hit. */
.users-colsheet {
  display: grid;
  gap: 4px;
  margin: 0 -8px;
}
.users-colsheet__item,
.users-colsheet__reset {
  display: flex;
  min-height: 44px;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 0 8px;
  border-radius: var(--radius-md);
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  text-align: left;
}
.users-colsheet__item:disabled {
  color: var(--color-text-muted);
}
.users-colsheet__reset {
  color: theme('colors.primary.600');
}
:global(.dark) .users-colsheet__reset {
  color: theme('colors.primary.400');
}
/* The identity column from 768px: 240px at most (a phone has the shared clamp). */
@media (min-width: 768px) {
  .users-identity {
    max-width: 15rem;
  }
}
/* Cells 12px from their column's edges, as on the accounts page (--cell-pad-x, 20px elsewhere): at 1920 the columns fit
   beside the open navigation and a classic scrollbar, nothing under the frozen actions column (7o-4). */
.dense-cells :deep(thead th),
.dense-cells :deep(tbody td) {
  padding-right: var(--cell-pad-x);
  padding-left: var(--cell-pad-x);
}
/* The bulk menu's button (12.9-5): the count in a slot as wide as three digits, empty until a row is ticked, so the
   button keeps its width; below 1024px a badge on the corner of the 40px square, the chevron gone. */
.users-bulk {
  position: relative;
}
.users-bulk__count {
  min-width: 30px;
  height: 18px;
  padding: 0 5px;
  border-radius: var(--radius-full);
  background: var(--color-bg-pill-hover);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
  line-height: 18px;
  text-align: center;
}
.users-bulk__count.is-empty {
  visibility: hidden;
}
@media (max-width: 1023px) {
  .users-bulk__count {
    position: absolute;
    top: -6px;
    right: -6px;
    min-width: 18px;
    background: theme('colors.primary.500');
    color: #fff;
  }
  .users-bulk__count.is-empty,
  .users-bulk__chevron {
    display: none;
  }
}
</style>
