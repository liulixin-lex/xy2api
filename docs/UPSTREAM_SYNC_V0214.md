# Sub2API v0.2.9–v0.2.14 三方同步

## 来源与范围

本次从 XY2API main `8907ac31d028c59bfbc32e5bfbf81982d005eb1b` 开始，完整同步六个稳定版本。产品候选为 `0.2.3-rc.1`，完整上游兼容基线为 `0.2.14`。此次只集成 PR，不创建产品发布标签、Release 或生产部署。

| 固定身份 | 值 |
| --- | --- |
| 官方 Release | https://github.com/Wei-Shaw/sub2api/releases/tag/v0.2.14 |
| Annotated tag object | `1400a7b482974d98db5b284a8b2afbe3eaf9aaef`（官方 unsigned） |
| 命名空间标签 | `sub2api/v0.2.14` |
| 上游 commit U | `0363b8cdba8cec3e2ba4b2dbd49c4481143fa55d` |
| 真实共同祖先 B | `fd80b08c90b55edcad5b00171b53f08721d30da1` |
| 准备提交 F | `515f5250ccb558f4b8a0f75d1cc617dc51760493`（主线加冲突登记与保留的交接文档） |
| 真实两父 merge | `832994d66` |
| 分支 | `sync/sub2api-v0.2.14` |

标准 `sync.py prepare` 接收173个上游提交，408个上游变化文件，其中279个与二开重叠。69个Git冲突逐项比较B/F/U，49个新冲突路径先精确登记到manual_merge。逐项理由见 `UPSTREAM_BASE.json.manual_resolutions`，完整影响矩阵见 `docs/upstream-sync/v0.2.14.json`。

## 最近版本的变化

| 版本 | 主要变化及本项目处理 |
| --- | --- |
| v0.2.9 | 模型glob白名单、客户端/协议兼容、流式usage/cache/search、499断开及工具链路径修正，合入完整实现。 |
| v0.2.10 | Sonnet5.5、Claude reset额度查询、风控白名单、dashboard金额与token、Composite/WS及流式工具修复。 |
| v0.2.11 | GPT6.1 Sol、Claude reset credits兑换、Codex remote catalog/套餐、Astra ultrafast、Key数量限制、在途余额预留；保留本项目已移植的严格参数校验与计费防护。 |
| v0.2.12 | TypeSafe Jev `/v1/systemone`、充值赠金/折扣阶梯、账号优先级和Key按组排序、Grok/Antigravity修复、验证码原子尝试和重置token单次消费。 |
| v0.2.13 | TypeSafe默认计费探测与用量结算修复。 |
| v0.2.14 | 随机新装管理员邮箱、密码强度、EasyPay伪造通知防护、Codex api_key_model_discovery。 |

## 二开边界与适配

- 保留双模式调度、账号质量/IQ/STATE、组提示词和长上下文策略、durable导出、插件v1和持久化标识；不重写品牌兼容字段、Redis/browser key、旧二进制或配置路径。
- PR #78的耐久计费意图、删除主体后结算、余额/用量/平台额度同事务、恢复与确认继续有效。新SystemOne用量也经过本项目持久化交接。首请求余额预留以及未知价格/缓存失败继续fail-closed。
- TypeSafe非流式入口接入可控调度元数据、停止错误、响应验收与一次结算；有效answer envelope才计为健康，HTML/错误/空体/无关JSON不能恢复账号健康。沿用上游对模型与usage形状的宽容解析。
- Stripe托管入口使用与普通支付相同的赠金/折扣报价，冻结到账/实付/赠金快照；配置变更后幂等重试仍复用原报价。真实PG测试覆盖无优惠、赠金、折扣及并发唯一订单。
- GPT6.1 Sol仍拒绝none/minimal及禁用思考，保留大小写/下划线/前缀别名校验和采样参数过滤；其它模型接收上游“disabled优先于output_config”的修复，max在启用思考时仍映射xhigh。
- EasyPay同时保留上游验签修复与本项目拒绝重复参数的约束。首次安装生成邮箱保留XY2API域名，既有管理员不受影响。
- Ent/Wire从源定义重新生成，修正合并后字段偏移；pnpm按合并manifest重建并frozen安装，保留已修复的Axios/source-map-js版本，不恢复xlsx及其高危例外。

## 数据库升级

上游的两条241文件只在本轮首次引入时重编号，避免插入本项目已发布迁移序列：

| 上游新增文件 | XY2API追加文件 |
| --- | --- |
| `241_add_payment_order_bonus_amount.sql` | `268_add_payment_order_bonus_amount.sql` |
| `241_add_typesafe_platform.sql` | `269_add_typesafe_platform.sql` |

原314条SQL逐字等同起点main，旧checksum条目不变；追加后316文件与manifest完全匹配。新列默认0，不给历史订单追赠；TypeSafe约束为旧平台集合的超集。真实集成回归通过正式迁移runner覆盖旧314条数据库、已有金额/路由、升级、幂等重跑和非法平台拒绝；全新数据库由完整repository integration harness覆盖。

## 验证与合并门禁

本地原始日志与最终结果位于 `/lex/upstream-sync-v0.2.14-20261007`。首轮发现重复导入、GPT别名采样及6个旧前端断言，已分别修正；资源不足造成的编译终止保留为失败，不计成功。

- 已完成：14项同步工具测试、干净树来源审计、316项checksum/314条字节不变、Compose解析、前端frozen install/typecheck/lint/生产构建、43项受影响前端回归。
- 合并前继续要求完整unit/integration、lint、生成零差异、完整前端Vitest及固定PR head全部required CI/security成功。不得以本段或旧head结果代替最终门禁。
- CI新增真实PG/Redis的TypeSafe成功/异常结算和Stripe托管优惠快照race回归，明确拒绝skip与空匹配，保留原29项调度可靠性门禁。
- 产品正式发布、镜像升级/回滚、真实供应商请求和生产数据操作不在此次集成验证范围内。
