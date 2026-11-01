# 实施与验收

- [x] Key 四态迁移/Ent/仓储/鉴权快照及失效；省略、非法值、ownership、并发 quota 的定向回归。
- [x] 接纳时冻结、最终 wire 多出口与 count_tokens 边界；顶层四槽、mixed TTL、deferred/thinking、能力清理与签名。
- [x] 同号重试、跨号与兜底无正文污染；真实入口受控 wire 测试。
- [x] 转换 TTL 桶与 presence、部分桶结算、商业覆盖结算副本和 1h 冷写预留。
- [x] 临时真实 PostgreSQL 迁移与 Key/usage 单批往返，旧数据/已发布 checksum 不变。
- [x] Key 表单四态/当前全局默认/切组保留、列表轻量文字；管理员二态与中英文计费分类文案。
- [x] 用量策略/applied 显示、历史 NULL 及未知明细提示，不承诺上游命中。
  - 2026-10-31 后续：按用户要求从使用记录列表与 Token 详情移除策略/applied 文字，接口字段与「TTL 明细未报告」提示保留。
- [x] 前端定向表单/API/用量/i18n 回归 135 项和 typecheck 通过。
- [x] 前端 build、改动文件 lint 和三个修改组件的 impeccable detect 通过。
- [x] 后端相关 unit/race/lint，按实际改动验证。
- [x] 现行文档、部署 runbook、单一 OpenSpec、目录/白名单、退役标识和 OpenSpec ownership 登记。
- [x] xy-anthropic-cache-ttl policy/provenance 登记及 10 项真实测试绑定、14 项 required_literals 内容核验。
- [x] 当前工作树全仓文档一致性通过（临时 index 纳入新增文件；真实 index 不变）。
- [x] C 路改动的 diff-check 通过。

数据层由主协调在隔离 PostgreSQL/Redis 下完成 40 个顶层用例 `-race`（零跳过、自有数据零残留），另完成 Redis-only 四态快照 `-race` 往返。最终 wire/usage/预留 selector、服务包全量 unit、TTL 相关 race、相邻包 race、golangci-lint、整仓 build、韧性 gate（84/281/90/2/18，零跳过）、scheduler-diff、迁移校验及文档一致性均已通过。真实供应商物理缓存能力仍未执行验证。

未授权生产部署/真实供应商试验；不增加截图矩阵、长浸泡、影子模式或重复成功门禁。干净树 audit 在获授权提交后执行。真实供应商支持与缓存效果保留为实际环境验证边界。
