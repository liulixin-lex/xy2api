# XY2API 仓库记忆与开发日志

> 这是跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读；执行过程中在关键节点更新；结束前必须写明结果、验证、卡点和下一步。项目事实变化时，应同步更新本文件，不能只追加日志而保留过期的顶部状态。

## 当前交接状态

### 分组与支付整合发布 0.1.1（2026-09-17）

- 用户已授权提交确认、推送、PR、合并和正式发版0.1.1，覆盖下方两项本地交付记录中的旧远端操作限制。原提交为分组9fd00b81c、支付cd258bdd9，均完整保留在发布分支历史中。
- 独立候选为/xy/artifacts/release-0.1.1/work，产品VERSION和UPSTREAM_BASE.json.xy2api_version为0.1.1，兼容基线保持0.2.5。两项未发布迁移分别采用249_group_system_prompt_policy.sql和250_stripe_hosted_idempotency.sql；旧迁移字节不变。
- 正在验证组合版本并通过受保护PR发布，实际结果持续写入/xy/artifacts/release-0.1.1/RELEASE_RESULT.json及两项功能原VERIFICATION.txt。Stripe真实测试账户联调尚未进行，不将模拟验收视为真实支付验收。


### 2026-09-17 — `20260917-stripe-hosted` — 本地实现与验收交接

- 按批准方案从 `41fd8591c` 创建独立 `feat/payment-stripe-hosted` worktree；原 `/xy/xy2api` 不修改。本地候选加入官方托管 Checkout，原站内 Stripe、余额与套餐有效期规则保留。
- 资金链路覆盖服务端计价、用户范围幂等键、冻结 Session 参数、重复/乱序回调、实例和模式校验、精确整数金额、延迟支付、取消竞争、历史实例保护及持久化退款扣回。数据库追加迁移 249 和 Ent 生成代码，旧订单字段可空。
- 相关 Go 回归和 PostgreSQL 并发下单、到账及退款场景已通过，托管 Provider/服务层 race exit0；前端 130 项测试、lint、最终类型与生产构建、嵌入前端的后端构建通过。手机配置区换行和长支付名称已调整，1280/390 深浅主题浏览器验收通过；返回页保留 PAID/RECHARGING 请求键直到完成履约，网络检查未加载站内 Stripe SDK。浏览器初期模拟响应缺字段导致提示，补齐测试夹具后重新验收，不将模拟提示归为支付源码故障。
- 同一外部 `acceptance.sh` 和 `acceptance-input.json` 已观察 BASELINE 不支持 `stripe_hosted`、MODIFIED 创建 `hosted_page` Session 且两次事件仅入账 80/COMPLETED、ROLLBACK 与 BASELINE 相同，三者 exit0。独立回滚归档与原始字节一致，补丁重建及回滚后重新应用均通过内容/可执行位/符号链接核验。首轮核验因 git archive 的组写权限差异失败，修正为 Git 保存的模式语义后通过，原失败日志保留。固定四角色路径保持 `/xy/artifacts/stripe-hosted/`，最终哈希及字面记录见 `VERIFICATION.txt`；回滚脚本不修改数据库。
- 配置指引见 `docs/STRIPE_HOSTED.md`。真实 Stripe 账户未使用，3DS、真实异步支付及退款仍需上线前测试；既有依赖公告及扫描范围记录于 `SECURITY_REVIEW.txt`。下一步只在新授权范围内提交或进行真实账户验收，本轮不推送、合并、发版或部署。

### 2026-09-17 — `20260917-stripe-hosted-commit` — 本地提交交接

- 用户明确要求提交。对象仍为 `feat/payment-stripe-hosted` 独立 worktree，提交消息为 `feat(payment): add Stripe hosted Checkout`，包含已验收的 61 个源码、生成代码、迁移、测试及文档文件；本地依赖链接 `frontend/node_modules` 不纳入。
- 提交前 `git diff --check` 通过；业务源码保持上一轮验收版本，本轮仅更新本交接记录。固定四角色重新封存并保留同输入 BASELINE、MODIFIED、ROLLBACK 输出，提交命令、最终 SHA 及提交后状态以 `VERIFICATION.txt` 的执行记录为准。
- 原主工作区保持不变。本轮仅本地提交，未推送或合并，未发版或部署。真实 Stripe 测试账号及既有依赖公告仍按上文作为后续门禁和维护项。
