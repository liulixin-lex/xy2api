# 设计

## 数据与接纳

设置 `anthropic_cache_ttl_default` 仅 5m/1h，默认 5m。Key `anthropic_cache_ttl_mode` 为 inherit/off/5m/1h；创建省略继承，更新省略不变，非法或显式空值 400。贯穿 Ent、仓储、鉴权最小投影与版本化 Redis/L1 快照；单字段更新不能覆盖并发 quota/usage。沿用既有失效机制。

列表只新增 `meta.anthropic_cache_ttl_default`，分页不变。入站、余额预留前冻结解析后策略；修改 Key 或全局设置不改变在途请求及重试。前端沿用宽弹窗与 Select，显示当前全局值，只在公开平台契约可到 Claude 的分组显示，切组保留偏好。2026-10-31 后续变更：管理员默认值从「请求转发行为」移到「Claude 缓存」卡片，作为「自动补缓存」下方的 5 分钟/1 小时两态按钮，卡片只提交改动字段，主表单不再提交 `anthropic_cache_ttl_default`；存储与接口不变。

## 最终出站

转换/映射/正文/system/既有补断点 → 一套 TTL 处理 → 供应商能力清理 → 计算 applied → 签名发送。以真实出口及规范化 Claude 模型判定，不能因分组或兼容端点而污染其他模型。入口与供应商按现有构造器组合适用：Anthropic 原生/透传及 Chat/Responses → Anthropic；Vertex 已支持的原生/转换路径；Bedrock 原生 Messages（Bearer/SigV4）。Chat/Responses → Bedrock 没有转换构造器或 eventstream 解码器，可能落入既有上游认证/通用转发错误而非显式 unsupported；本变更不新增该转换。`count_tokens` 不覆写或补断点，其他供应商出口不适用。

只遍历协议位置，不递归业务 JSON。顶层 automatic 计入四槽，新增断点预留槽位；不任意删合法声明。强制统一后再保护合法性；off 保留客户端合法混合 TTL，保留既有顺序保护。deferred/thinking 不注入。Bedrock legacy automatic 能力边界明确，清理后不再补回。缓存变换只作用当前尝试，跨号/同号重试/兜底不继承前次 wire。

## 财务与观测

5m/1h 桶及 presence 贯穿内部转换；省略保留、显式 0 清零、累计事件不相加。aggregate 省略可由明确子桶归一，显式 0 不推导正数，不反写供应商响应。完整桶分价；未知与部分差额按基础写价，超总量确定性封顶，记录异常。渠道单价、倍率、显式价格优先，不追补历史。

移除全局请求 TTL 自动按 5m 结算。账号计费分类覆盖与 ForceCacheBilling 只影响结算副本，保留计费调整标记；不伪装上游效率。Sonnet 5.5 官方回退读取价为 $0.10/MTok，不改管理员显式报价。

在现有预留输入区分无冷写风险/5m/1h，覆盖客户端断点及既有注入可能性；冷写取对应本站写价与输入价较高者并禁用历史缓存折扣，无断点且无注入路径不提高预留。最终收费仍取实际 usage 和商业规则。

usage 新增可空 policy/applied：policy=off/5m/1h；applied 为最终能力清理后有合法声明且全部匹配指定 TTL，即使字节未改也 true；off/无声明/未落实 false；非目标及历史 NULL。只使用结算尝试。使用记录页面不显示 policy/applied（2026-10-31 后续变更：0.4.15 曾在列表与详情显示，存量 off Key 每行都是「未应用」，按用户要求移除），接口照常返回，NULL 为未记录；已应用不等于供应商生效或命中。复用结构化事件记录有限枚举和原始数字，不新增日志服务。

## 升级与回退

追加迁移 286，旧 332 项不变：已有 Key off、同事务设置新建 inherit，四态约束；usage 两个 NULL 列无回填/索引。表锁与约束扫描在实际 PG 上评估，低峰执行。旧布尔与消息断点重建退出 runtime/UI/API/设置快照，不映射旧 true 或自动打开 fallback。

旧策略为 true 或存在 OAuth 隐式补贴的部署先人工明确断点、默认与账号计费映射，不覆盖已有显式值。旧二进制可保留新列但不保证行为等价，回退须恢复旧全局策略、停用新入口，不删除列或重算历史。部署说明与现行文档同批更新。
