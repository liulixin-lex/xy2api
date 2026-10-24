.PHONY: build build-backend build-frontend test test-backend test-frontend test-frontend-critical

FRONTEND_CRITICAL_VITEST := \
	src/i18n/__tests__/localeKeyCompleteness.spec.ts \
	src/api/__tests__/client.spec.ts \
	src/api/__tests__/tokenRefresh.spec.ts \
	src/api/__tests__/keys.bulkUpdate.spec.ts \
	src/components/account/__tests__/OpenAIReferralCell.spec.ts \
	src/components/account/__tests__/OpenAIReferralCell.transport.spec.ts \
	src/components/account/__tests__/OpenAIQuotaResetCell.spark_shadow.spec.ts \
	src/constants/__tests__/platforms.spec.ts \
	src/components/account/__tests__/credentialsBuilder.platformCatalog.spec.ts \
	src/components/account/__tests__/CreateAccountModal.spec.ts \
	src/components/account/__tests__/EditAccountModal.spec.ts \
	src/components/account/__tests__/credentialsBuilder.spec.ts \
	src/components/account/__tests__/OpenCodeGoProtocolRulesEditor.spec.ts \
	src/components/keys/__tests__/BulkEditKeysModal.spec.ts \
	src/components/admin/user/__tests__/UserPlatformQuotaModal.spec.ts \
	src/views/user/__tests__/KeysView.spec.ts \
	src/api/__tests__/channelMonitorV2.spec.ts \
	src/components/admin/monitor/__tests__/GrokResponses.spec.ts \
	src/views/auth/__tests__/LinuxDoCallbackView.spec.ts \
	src/views/auth/__tests__/WechatCallbackView.spec.ts \
	src/views/user/__tests__/PaymentView.spec.ts \
	src/views/user/__tests__/PaymentResultView.spec.ts \
	src/views/user/__tests__/ChannelStatusView.mode.spec.ts \
	src/components/user/profile/__tests__/ProfileInfoCard.spec.ts \
	src/components/user/profile/__tests__/ProfileIdentityBindingsSection.spec.ts \
	src/views/admin/__tests__/SettingsView.spec.ts \
	src/views/admin/settings/__tests__/ClaudeCacheFallbackSettings.spec.ts \
	src/views/admin/settings/__tests__/ShareSchedulingSettings.spec.ts \
	src/views/admin/__tests__/ShareSchedulingView.spec.ts \
	src/stores/__tests__/shareScheduling.spec.ts \
	src/utils/__tests__/shareScheduling.spec.ts \
	src/api/__tests__/admin.shareScheduling.spec.ts \
	src/components/account/__tests__/ShareAccountRules.spec.ts \
	src/features/channel-monitor-v2/__tests__/designSystem.structure.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorFormat.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorZoom.spec.ts \
	src/components/admin/channel/__tests__/PricingEntryCard.modelDefaultPrice.spec.ts \
	src/stores/__tests__/groupCategories.spec.ts \
	src/composables/__tests__/useGroupCategoryTabs.spec.ts \
	src/api/__tests__/admin.groupCategories.spec.ts \
	src/utils/__tests__/groupCategories.spec.ts \
	src/utils/__tests__/accountGroupSelection.spec.ts \
	src/components/common/__tests__/GroupSelector.spec.ts \
	src/components/admin/group/__tests__/GroupCategoryManagerButton.spec.ts \
	src/components/admin/group/__tests__/GroupCategoryField.spec.ts \
	src/components/admin/group/__tests__/GroupCategoryFilter.spec.ts \
	src/components/admin/account/__tests__/AccountGroupsCellButton.spec.ts \
	src/components/admin/account/__tests__/AccountGroupsDialog.spec.ts \
	src/components/admin/group/__tests__/GroupCategoryTabs.spec.ts \
	src/components/admin/account/__tests__/AccountTableFilters.groupCategories.spec.ts \
	src/components/admin/account/__tests__/AccountTableFilters.spec.ts \
	src/views/admin/__tests__/GroupsView.columnSettings.spec.ts \
	src/views/admin/__tests__/GroupsView.categories.spec.ts \
	src/views/admin/__tests__/AccountsView.groupCategories.spec.ts \
	src/i18n/__tests__/localesNoKeyCollision.spec.ts \
	src/features/cache-observability/__tests__/integrationSurface.spec.ts \
	src/features/cache-observability/settings/__tests__/schema.spec.ts \
	src/features/cache-observability/settings/__tests__/usePolicies.spec.ts \
	src/features/cache-observability/settings/__tests__/contract.spec.ts \
	src/features/cache-observability/settings/__tests__/CacheSettingsPanel.spec.ts \
	src/features/cache-observability/charts/__tests__/viewModel.spec.ts \
	src/features/cache-observability/charts/__tests__/mock.spec.ts \
	src/features/cache-observability/charts/__tests__/ChartsPanel.spec.ts

# 一键编译前后端
build: build-backend build-frontend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck
	@$(MAKE) test-frontend-critical

test-frontend-critical:
	@pnpm --dir frontend exec vitest run $(FRONTEND_CRITICAL_VITEST)
