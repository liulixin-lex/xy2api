export default {
  operatorAccess: {
    title: 'Operator permissions',
    module: 'Module',
    view: 'View',
    manage: 'Manage',
    modules: {
      dashboard: 'Dashboard',
      ops: 'Ops monitoring',
      users: 'Users',
      groups: 'Groups',
      channels: 'Channels',
      accounts: 'Accounts',
      share_scheduling: 'Share scheduling',
      announcements: 'Announcements',
      proxies: 'IP management',
      security_audit: 'Security audit',
      redeem_codes: 'Redeem codes',
      promo_codes: 'Promo codes',
      affiliates: 'Affiliates',
      orders: 'Orders',
      usage: 'Usage',
      audit_logs: 'Audit logs',
      subscriptions: 'Subscriptions'
    },
    flags: {
      balanceAdjust: {
        label: 'Adjust balance',
        hint: 'On: can add or deduct balance, set an initial balance, grant and extend subscriptions, change group rates and plan prices, and put accounts into or out of groups. Off: can only view these.'
      },
      upstreamAddress: {
        label: 'Show upstream address',
        hint: 'On: account names show the upstream address and link to it; the edit dialog shows the access address and proxy and lets you change them (read-only on OAuth-type accounts); you can create and edit proxy servers. Off: no upstream address is shown, and addresses and proxies cannot be changed.'
      },
      redeemIssue: {
        label: 'Generate codes',
        hint: 'On: can generate redeem and promo codes, edit them in bulk, and see full codes. Off: cannot generate; codes are masked.'
      },
      ordersRefund: {
        label: 'Refund',
        hint: 'On: can refund, check refunds, retry fulfilment and handle affiliate withdrawals. Off: cannot do these.'
      },
      dataExport: {
        label: 'Export business data',
        hint: 'On: can export usage records, redeem codes and other business data. Off: cannot export.'
      }
    },
    loadFailed: 'Failed to load operator permissions',
    createHint: 'A new operator has no permissions. Set them in the edit dialog.',
    groupExclusiveHint: 'Without "Adjust balance", new groups must be exclusive.',
    searchByEmail: "Search by user email (must contain {'@'})",
    createNoSignupGrants: 'Without "Adjust balance", new users start at balance 0 and get no default subscription.',
    accountUngroupedHint: 'Without "Adjust balance", new accounts join no group until someone with that permission assigns one.',
    monitorHeadersDropped: 'After changing the endpoint, provider or API mode, only the headers entered in this edit are kept.',
    templateApplySuperOnly: 'Only a super admin can apply a template that has headers or a body override.',
    addressNeedsFlag: 'Without "Show upstream address", you cannot set an upstream address. New accounts use the platform\'s default address.',
    networkLockedRefreshable: 'Only a super admin can change the address, proxy, type and access mode of an OAuth-type account.',
    proxyNetworkNeedsFlag: 'Without "Show upstream address", you cannot create proxies or change a proxy\'s protocol, host, port, user name, password or backup chain.',
    groupDeleteNeedsEmpty: 'Without "Adjust balance", only groups with no accounts can be deleted.',
    errors: {
      keyRequired: 'Changing the proxy, type, API protocol, account mode or region needs every stored secret to be entered again or cleared (missing: {field}).',
      invalidRegion: 'The AWS region is not valid, for example us-east-1.',
      newApiTokenRequired: 'Changing the proxy, type, API protocol, account mode or region needs the new-api access token to be entered again or cleared.',
      privateAddress: 'Private or local addresses are not allowed.',
      networkChangeForbidden: 'Without "Show upstream address", addresses and proxies cannot be changed. Only a super admin can change the proxy, type and access mode of an OAuth-type account. Bulk edits cannot change these settings.',
      reservedEmail: 'This email domain is reserved by the system.',
      proxyInUse: 'This proxy is in use (by an account, as a fallback origin, in a backup chain or in a system setting) and cannot be deleted.'
    }
  }
}
