# 平台观测设计

## 运行时边界

沿用 cache_observability_policy，不增加 settings 键。platforms 是 openai、anthropic、gemini、messages_bridge 的非空子集，默认只有 openai；ingress_chain_percent 默认 10，必须不大于 chain_percent。静态 kill_switch 与资源上限仍有效。每请求持有不可变策略快照，切换不影响在途请求。

排除的平台在采集、worker、结算、事件和历史发布前停止观测；数据只进管理员观测与内部日志，公开设置和用户响应不增加字段。既有缓存、调度、韧性和 claude_cache_fallback 行为不变。

## 捕获与归因

Anthropic 捕获既有 fallback、映射与转换后的真实请求，覆盖原生 Messages、Chat/Responses 转换、Vertex、Bedrock、API key 透传；Gemini 覆盖 compat 与 native。Messages 桥沿用 OpenAI 的诊断与计费关联，不含 OpenAI 账号的 Anthropic 原生子路径。

复用有界异步队列及预算，正文扫描与前缀计算在 worker 内执行；路由只记录既有选择结果。非故障改绑记录 owner_skip_reason，不新增选号状态读取或写入。

P/read 取同一请求的最终计费 usage，字段缺失与显式零分别记录。Anthropic 缓存断点与 thinking、Gemini usageMetadata 按协议解析，平台事件不混入 OpenAI 专属字段。

## 追加字段组

入站链按策略采样；出站分叉且入站已采样时拆分 client/gateway 原因。S1/S2/S6 按契约使用不可用值；S2 使用观测自己的在途计数，不读取分组并发健康缓存。

origin_group_id 与 serving group 分开，relay_family 缺省 unknown，OpenAI ingress_ws_bridge 读现有标记，均只进内部观测与管理员汇总。

## 等值与隐私

先在未修改的主分支录制出站和实际计费 golden，再接入挂点。动态 SigV4 时间与签名以固定合成凭据和固定时刻规范化，其他请求内容逐字节比较。保留普通非流式与其他 Anthropic 路径的现有差异；只合并逐路径计费相等的归一化。

粘性日志的 session、device 与原始 metadata 标识改为 HMAC；缺密钥时不回退输出原值。会话哈希返回值及选号输入不变。
