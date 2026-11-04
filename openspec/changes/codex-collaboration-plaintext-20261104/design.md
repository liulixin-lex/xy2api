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

由 P0 工作包补充识别、错误规则优先级与 state_rejection 事件契约。

## 7. 启用与回退

默认关。前后端同版后先验证最终模型，再配置全局 enabled/models 与目标账号开关；不用新增环境变量或重启。关闭开关停止后续转换，在途还原器继续按原 plan 工作。已有密文历史不重标，不保证恢复；上游拒绝走既有错误路径，不靠错误文案触发转换或自动换号补救。
