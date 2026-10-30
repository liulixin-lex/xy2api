export default {
  operatorAccess: {
    title: '普通管理员权限',
    module: '模块',
    view: '查看',
    manage: '管理',
    modules: {
      dashboard: '仪表盘',
      ops: '运维监控',
      users: '用户管理',
      groups: '分组管理',
      channels: '渠道管理',
      accounts: '账号管理',
      share_scheduling: '份额调度',
      support_tickets: '工单处理',
      announcements: '公告',
      proxies: 'IP管理',
      security_audit: '安全审计',
      redeem_codes: '兑换码',
      promo_codes: '优惠码',
      affiliates: '邀请返利',
      orders: '订单管理',
      usage: '使用记录',
      audit_logs: '操作日志',
      subscriptions: '订阅'
    },
    flags: {
      balanceAdjust: {
        label: '加减余额',
        hint: '开：可以加减余额、设初始余额、发放和延期订阅、修改分组倍率和套餐价格。关：只能查看这些内容。'
      },
      upstreamAddress: {
        label: '显示上游地址',
        hint: '开：账号名称显示上游地址，可点击跳转，编辑时显示并可修改接入地址和代理（OAuth 类账号只读），可以新建和修改代理服务器。关：不显示上游地址，不能修改地址和代理。'
      },
      redeemIssue: {
        label: '生成兑换码',
        hint: '开：可以生成兑换码和优惠码、批量修改，列表显示完整的码。关：不能生成，列表里的码打码显示。'
      },
      ordersRefund: {
        label: '退款',
        hint: '开：可以退款、查询退款、重新履约、处理返利提现。关：不能做这些操作。'
      },
      dataExport: {
        label: '导出业务数据',
        hint: '开：可以导出使用记录、兑换码等业务数据。关：不能导出。'
      }
    },
    loadFailed: '加载普通管理员权限失败',
    createdPermissionsPending: '账号已创建，权限尚未保存。请重试保存权限，或关闭后在编辑账号中配置。',
    createHint: '新建的普通管理员没有任何权限，请在编辑中设置。',
    groupExclusiveHint: '没有“加减余额”权限时，新分组只能是专属分组。',
    searchByEmail: "按用户邮箱搜索（需包含 {'@'}）",
    createNoSignupGrants: '没有“加减余额”权限时，新用户余额为 0，也不会获得默认订阅。',
    monitorHeadersDropped: '修改地址、服务商或接口模式后，只保留本次填写的请求头。',
    templateApplySuperOnly: '含请求头或请求体覆盖的模板只能由超级管理员应用。',
    addressNeedsFlag: '没有“显示上游地址”权限时不能设置上游地址，新账号使用平台默认地址。',
    networkLockedRefreshable: 'OAuth 类账号的接入地址、代理、类型和接入方式只有超级管理员可以修改。',
    proxyNetworkNeedsFlag: '没有“显示上游地址”权限时不能新建代理，也不能修改代理的协议、主机、端口、用户名、密码和备用链。',
    groupDeleteNeedsEmpty: '没有“加减余额”权限时，只能删除没有账号的分组。',
    errors: {
      keyRequired: '修改代理、类型、协议、接入模式或区域时，需要重新填写或清除所有已保存的密钥（缺少：{field}）。',
      invalidRegion: 'AWS 区域格式不正确，例如 us-east-1。',
      newApiTokenRequired: '修改代理、类型、协议、接入模式或区域时，需要重新填写或清除 new-api 访问令牌。',
      privateAddress: '不能使用内网或本机地址。',
      networkChangeForbidden: '没有“显示上游地址”权限时不能修改地址和代理；OAuth 类账号的代理、类型和接入方式只有超级管理员可以修改；批量编辑不能修改这些设置。',
      reservedEmail: '不能使用系统保留的邮箱域名。',
      proxyInUse: '该代理正在使用中（账号、回退来源、备用链或系统配置），不能删除。'
    }
  }
}
