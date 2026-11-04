# 设计

## 1. 设置与资格

`gateway_resilience_settings.collaboration_plaintext` 缺省 `{ "enabled": false, "models": [] }`。models 最多 16 条，每条去首尾空白后 1–128 字符；去空白后按不分大小写判重，保留第一项，nil 归一为 []。读取、发布和 Clone 均隔离集合。

所有门同时满足才转换：韧性总开关、功能开关、最终映射后的出站模型精确命中清单、OpenAI 账号 `extra.openai_collaboration_plaintext` 为布尔 true、真实 Codex UA/originator、入站 HTTP 且原生 Responses HTTP 出站、非透传/compact/桥接/摊平账号。ForceCodexCLI 不绕过客户端门。API Key 和 OAuth 均适用。

只在 tools 或 input[type=additional_tools].tools 声明 collaboration namespace 时挂本次尝试的还原器；不合格请求只观测、不改字节，无声明不挂。非原生提前分流不进入该转换。每次从不可变请求体重新派生 plan；不使用 codexToolNameSessionKey 跨请求继承。设置热变更只影响新尝试，在途 plan 保持冻结。

## 2. 原子出站变换（核心契约）

`applyCodexCollaborationPlaintext` 先全量校验冲突，再原子将 namespace 节点 collaboration 改成 collaboration-xy，并只删 spawn_agent/send_message/followup_task 的 parameters.properties.message.encrypted。不得只删标记、不改名。明确工具选择引用随声明改名；applied 时同步把顶层 instructions 字符串与 input[] 的 message(role=developer/system)正文（字符串或 input_text.text）中精确的 functions.collaboration. 替换为 functions.collaboration-xy.，保证开发者提示的寻址示例与声明一致。该范围由预检自然选工具对照暴露后 PM 追加裁决，用户消息/工具输出/参数不在替换范围。历史 agent_message、function_call_output、call_id/item_id、arguments 与 output 正文不改。

返回 outcome=applied|absent|conflict|unsupported|invalid；非 applied 保持原字节、plan=nil。`newCodexCollabRestorer(plan)` 每次尝试创建；nil plan 只观测。`SSEData` / `ResponseJSON` / `Stats` 是三条回程出口共享的契约。

## 3. 回程与终止

在既有 restoreCodexToolNames* 之后，将 namespace 字段+裸名、collaboration-xy.tool、collaboration-xy__tool 三形态统一还原为 type=function_call、namespace=collaboration、name=裸叶名。只对声明内三个消息工具且 message 为非空明文字符串、没有真密文证据的调用补 encrypted_function_args:[]；同 namespace 的 wait/list 等只恢复名字。

非空 encrypted_function_args，或 message 形态可疑（gAAAA 前缀、无空白长 base64）否决补标记。流式按逻辑调用保留早先非空证据，不能被后续缺失/空列表覆盖；早先非空、后来显式 [] 时保留原 []、计 Contradicted/Vetoed。未知私有叶名仍恢复 namespace/裸名，计 Uncovered、不补标记。逻辑调用跨 added/done/completed 去重，EFA 分类允许随非空证据升级。

流、JSON、SSE→JSON 三出口都恢复；聚合前先观察各帧，保证早先密文证据保留。无法安全恢复身份（无效 JSON、身份歧义）则本地终止：未提交发 502，已提交写协议终态，不把私有名或原始错误下发，不返回 FailoverError 重放。applied 的 SSE 无法聚合成最终 JSON 时同样本地终止。只观测解析失败保留原字节，不制造新中断。

## 4. schema 兼容

工具 schema sanitizer 对精确名为 collaboration 的整个 namespace 节点豁免递归修改，包含 wait/list 等叶；其它工具继续原有 null type、object union、null required、lookaround 兼容修复。normalizeCodexTools 不递归 namespace，仅加锁定测试。

用改动前 c8b6bedda 清理器对真实 Codex 0.166 fixture 分别运行顶层 tools 与 Lite additional_tools：均 changed=false、字节完全一致。因此该 fixture 在 P1′ 关闭时不受豁免影响；其它本来会被修复的 collaboration schema 会保持原样，这是独立于开关的兼容变化。

## 5. 观测（PM 裁决）

尝试开始、每次恢复及返回前用 setOpenAICodexCollabAttempt 写本次统计。P0 的收尾 defer 最先注册、最后执行，读取最终统计。

- collab_plaintext：outcome、model，标签编码 outcome=<值>;model=<实际出站模型>。
- collab_call：efa=absent|empty|nonempty，分别取 EFAAbsent/EFAEmpty/EFANonEmpty。
- collab_call_action：action=marker_added|vetoed|contradicted|uncovered，取四个动作计数。

EFA 与 action 不增加联合字段，不做 EFA×action 联合标签。追加 DevTextRewrites 仅统计本次开发者/系统消息与 instructions 中精确字面串的实际替换次数（由出站 plan 冻结传给 restorer）；不新增指标，也不单独触发 Warn。只有 conflict/invalid、Vetoed/Contradicted/Uncovered>0 或还原出错发 Warn resilience.collab_plaintext，带 account_id/group_id/model/client_version 及各计数，不存正文、密文、参数或原始错误。

## 6. P0 状态族识别与观测

请求级决策与有限修复分离。`ClassifyStateRejection(code, message)` 保留 v3 窄边界：专用 `client_tool_continuation_unavailable` code 或旧 `IsEncryptedContentRejection(code, message)` 命中；其余仅 code 为空时允许 S1 完整短语 `opaque conversation state` / `unrecognized conversation state` 和 S2 完整短语 `encrypted content is invalid for this conversation`。type 本身不作为证据。非空 server/rate-limit code 不因 S1/S2 文案降成请求错误；旧加密解密/校验拒绝仍优先。`Encrypted` 恒等旧谓词结果，仅用于诊断，不扩展修复资格。

分类命中统一为 `CLIENT_ERROR`，`Result.StateFamily` 按识别优先级区分 `relay_state`、`conversation_invalid`、`function_output`、`agent_message`、`reasoning`、`other_encrypted` 六个家族。旧加密谓词命中时 reason 保持 `encrypted_content`，其余使用 `conversation_state`。

原生/透传 HTTP、SSE failed/error、Chat/Messages 公共路由、WS 与账号副作用入口、handler 兜底共用该分类。状态拒绝不换号、不罚号，不计会话连击、门槛与份额健康软失败、输出后冷却或 P1-B；handler 在请求预算之前终止恢复，独立于韧性总开关。余额/额度、限流、模型与容量恢复分支保持原有分类和行为。

HTTP/带内 rs/cmp 有限剥离、WS 恢复判据与预算、输出提交门禁继续使用原有修复条件。S2/S6 加原本可修复的 rs/cmp 仍可同号有界重放；S1/S3/S4 加旁路 reasoning 不借机删除协作或工具正文，也不重放。语义输出后不重放、改绑、冷却或增加会话失败，保留真实 usage 与既有错误归因。透传 HTTP 的客户端错误信封与文案保持既有行为。

新增默认观测 Warn 事件 `resilience.state_rejection` 与 `state_rejection` 计数（label 为家族），只记录标识、元数据与计数。原生/透传在 Forward 收尾 defer 汇总最终失败，每次至多一次；最终成功不计状态拒绝。事件含请求关联、`family`、`encrypted`、`path`、`stage`、`headers_committed`、`error_ms`、`client_version` 与片段计数。gin `ops_error_passthrough_rule_id` 每尝试清零、实际规则命中时更新，最终事件读取 `rule_id`。`collab_plaintext` 取最终统计的 Applied，另附 Calls、EFAAbsent/Empty/NonEmpty、MarkerAdded、Vetoed、Contradicted、Uncovered；统计必须在 Forward 返回前写完，P0 defer 最先注册、最后执行。

桥接/WS 在既有判断点直接观测，不带规则字段。按 PM 裁决，WS 处罚 helper 不扩签名或四条调用链；事件 `stage=unknown`，省略 `headers_committed`、`client_version` 与片段计数，不从连接累计 DeliveredBytes 推断 post_output。WS 的观测字段因此不完整。

片段 census 只扫描协议位置：agent_message.content[] / function_call_output.output[] 中的 encrypted_content 片段及其他 input 项顶层 encrypted_content，支持合法 Unicode 转义；不递归 arguments、工具字符串输出或用户 JSON。不存请求/错误正文、密文或参数；原始错误 code 在现场参与分类，不依赖可选正文日志。已有 `upstream_errors` 只补 `state_family`，不为观测制造新的尝试归因事件。

## 7. 启用与回退

默认关。前后端同版后先验证最终模型，再配置全局 enabled/models 与目标账号开关；不用新增环境变量或重启。关闭开关停止后续转换，在途还原器继续按原 plan 工作。已有密文历史不重标，不保证恢复；上游拒绝走既有错误路径，不靠错误文案触发转换或自动换号补救。
