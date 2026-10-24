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

## 管理员 stats 分区

分钟与小时计数、host EWMA、会话配对表按 openai、anthropic、gemini 独立分区，沿用每分区既有容量与 other 折叠。Messages 桥归入 openai。小时行保存真实 platform，迁移 279 不变。

all 只汇总当前覆盖的分区，维度先求和再按小样本折叠；同名 host 衰减到同一时刻后合并。OpenAI 专属指标只来自选中的 OpenAI 分区，不以其他平台请求作分母。单独查询已关闭的平台仍可读取窗口内历史；all 不纳入该分区。

默认 platforms=[openai] 时，openai、all 查询及小时行与 M2 主分支逐字段相同；其他平台查询修正旧的错误覆盖标注。覆盖范围与提示按追加契约输出，查询未覆盖平台仍返回 200。默认兼容、跨平台污染、容量隔离、汇总及历史切换有独立回归与实际 handler 输出验证。

当前只完成策略字段及 stats 分区的实现草稿，新增分区测试尚待门禁；provider 采集与结算接缝继续开发，不作为可部署完成状态。

Messages 桥复用 OpenAI 的有界 worker 与结算链，保持事件 platform=openai；仅在实际出站为 Responses 时计入桥。流式终态的 typed usage 覆盖仍由原业务代码执行，观察器只记录同次字段存在性。开关矩阵包含默认 OpenAI、仅 Anthropic、仅桥、全平台、采样零、总闸及诊断关闭；另测原生子路径排除。追加的桥出站/计费基线必须从未修改 main 录制，禁止用候选生成。

## 观测与租户隔离裁定

OpenAI（含 Messages 桥）的 usage_fields 只认 bit0/2/3 为已知读量，只有 bit1 别名时归 n/a_usage，不带 na_reason；已知 P 的密度保留，依赖读量的标记保持 false。此裁定不修改计费解析。M2b v1.2 schema 接受 M3.1 的追加 l01_skip 与可选恢复标记，后者出现时必须 applied=true。

Anthropic/Gemini 的 session_digest 与 history/LRU 键统一使用 cacheIdentityDigest("<platform>-session", api_key_id|serving_group_id|sessionHash) 的前 16 位；原始会话哈希不得直接用作历史键。platform 取 anthropic 或 gemini。不同租户、serving 组和平台必须隔离；history/LRU 及后续 S2/S6 均使用同一个摘要，缺少 HMAC 密钥时不回退使用原值。

provider 历史基础使用独立有界 LRU，按 platform 与上述 HMAC 摘要索引；发送、计时关闭与链发布分开，淘汰后迟到 worker 必须匹配原 owner 才能发布。基础实现与隔离/并发测试已补，尚未接入 provider 发送和计费挂点，不能视为已完成平台接线。
