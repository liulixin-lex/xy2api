# 文本预算截止锚点与截断归因（0.5.12 第一期）

## 背景与目标

上传占用 arrival 起算的首输出预算。第一期允许在读到请求体 EOF 后后移绝对截止，并单独识别没有拿满账号档位的预算截断。发版缺省 arrival / observe，行为保持 0.5.11。

## 范围

- C1：KindText 的截止 D = min(anchor + base, arrival + 290 s)，body_eof 仅在首个 EOF 晚于到达至少 1 s 时生效。
- C3：每个计时器持有自己的不可变 TimeoutPlan；applied 叠加在既有 planned 规则之上，仅追加慢连击、latency-hold、近期健康展示和 share 的豁免。
- 请求策略快照、请求时序、OpenAI 真实 HTTP 派出观测、迁移 292/293、配置表单。
- 同步修订旧 OpenSpec 的预算和保活契约；≥60 s 即账号级失败的定义不改动。

第一期不新增准入、grant、请求级取消、非流式 guard、Gemini 末候选计时器，不改变 spent、lastMark、心跳、计费和既有 planned 豁免。高级调度器失败上报留待阶段 2 前 PM 裁决。

## 验收与发布

见 [设计](design.md)、[任务](tasks.md)、[发布说明](release-notes.md) 和 [值班 runbook](runbook.md)。测试结果以实施报告为准；设计条目不是已运行证据。本变更不授权部署或生产操作。
