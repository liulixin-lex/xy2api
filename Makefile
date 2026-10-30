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
	src/components/common/__tests__/Select.spec.ts \
	src/components/common/__tests__/DataTable.spec.ts \
	src/components/common/__tests__/GroupOptionItem.spec.ts \
	src/components/common/__tests__/TruncatedTextPopover.spec.ts \
	src/components/account/__tests__/UpstreamBalanceCell.spec.ts \
	src/components/account/__tests__/NewApiWalletSection.spec.ts \
	src/components/account/__tests__/EditAccountModal.newapiWallet.spec.ts \
	src/api/__tests__/admin.accounts.upstreamBalances.spec.ts \
	src/views/admin/__tests__/AccountsView.upstreamBalance.spec.ts \
	src/stores/__tests__/adminAccess.spec.ts \
	src/router/__tests__/adminPermissionMeta.spec.ts \
	src/components/layout/__tests__/AppSidebar.operator.spec.ts \
	src/components/admin/user/__tests__/OperatorPermissionsTable.spec.ts \
	src/components/admin/user/__tests__/UserEditModal.operator.spec.ts \
	src/views/admin/__tests__/AccountsView.operatorAccess.spec.ts \
	src/api/__tests__/client.permissionDenied.spec.ts \
	src/components/admin/__tests__/AdminComplianceDialog.operator.spec.ts \
	src/stores/__tests__/adminSettings.operator.spec.ts \
	src/components/account/__tests__/EditAccountModal.operatorAddress.spec.ts \
	src/components/account/__tests__/CreateAccountModal.operator.spec.ts \
	src/components/account/__tests__/BulkEditAccountModal.operator.spec.ts \
	src/components/admin/account/__tests__/ReAuthAccountModal.operator.spec.ts \
	src/views/admin/__tests__/ProxiesView.operator.spec.ts \
	src/components/admin/monitor/__tests__/MonitorTemplateApplyPickerDialog.operator.spec.ts \
	src/views/admin/ops/components/__tests__/OpsSettingsDialog.operator.spec.ts \
	src/views/admin/__tests__/UsersView.operator.spec.ts \
	src/views/admin/__tests__/ChannelsView.operator.spec.ts \
	src/views/admin/__tests__/GroupsView.operator.spec.ts \
	src/components/admin/__tests__/accountToolPanels.readonly.spec.ts \
	src/components/admin/monitor/__tests__/MonitorFormDialog.operator.spec.ts \
	src/views/admin/__tests__/RiskControlView.spec.ts \
	src/api/admin/__tests__/openaiTotp.spec.ts \
	src/components/admin/account/__tests__/OpenAITotpDialogs.spec.ts \
	src/components/admin/user/__tests__/UserCreateModal.permissions.spec.ts \
	src/features/support-tickets/__tests__/supportTickets.spec.ts \
	src/features/support-tickets/__tests__/TicketComponents.spec.ts \
	src/features/support-tickets/__tests__/SupportTicketRequests.spec.ts \
	src/views/user/__tests__/SupportTicketsViews.spec.ts \
	src/views/admin/__tests__/SupportTicketsAdmin.spec.ts \
	src/stores/__tests__/supportTickets.spec.ts \
	src/router/__tests__/feature-access.spec.ts

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
