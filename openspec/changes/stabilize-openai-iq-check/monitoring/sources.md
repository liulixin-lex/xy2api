# 资料核实与证据范围

核实日期：2026-09-14 UTC。该目录为规划文档，未改变业务代码。主对象为本地 `/xy/xy2api`，分支 `fix/iq-response-protocol`，HEAD `cb5dc5ae060ee2c04ce996a67cfe289f0224f657`，包含上一轮尚未提交的协议兼容修复。

| 来源 | 日期/定位 | 本方案使用的事实 | 不能据此推出 |
| --- | --- | --- | --- |
| [用户提供的CISA AA26-251A](https://www.cisa.gov/news-events/cybersecurity-advisories/aa26-251a) | 本次直连读取返回403 | 通过下列联合发布方核对同题报告 | 403不说明该报告不存在，也不说明本项目账号被限制 |
| [NSA联合公告发布页](https://www.nsa.gov/Press-Room/Press-Releases-Statements/Press-Release-View/Article/4592113/nsa-and-others-warn-china-based-ai-companies-are-distilling-us-frontier-ai-mode/)及[官方PDF](https://media.defense.gov/2026/Sep/08/2003992823/-1/-1/1/CSA_CHINA_BASED_AI_COMPANIES_MALICIOUS_DISTILLATION_AGAINST_US.PDF) | 2026-09-08；PDF第13–14页 | 联合报告确实建议对高置信度恶意蒸馏采取响应调整；讨论区分恶意活动与评估者 | 这是防御建议；没有公开OpenAI的分类阈值、账号标签或本项目的触发原因。不能由它断言“糖果题/JSON导致降智” |
| [OpenAI评测实践](https://developers.openai.com/api/docs/guides/evaluation-best-practices) | 2026-09-14读取 | 同一输入可以产生不同输出；评测应有明确目标和可复现判据 | 单题通过不等于模型整体能力或实际部署身份得到认证 |
| [GPT-6 Astra模型页](https://developers.openai.com/api/docs/models/gpt-6-astra) | 2026-09-14读取 | 模型ID为gpt-6-astra；支持low/medium/high/xhigh/max，声明支持结构化输出 | OAuth或任意兼容上游必然支持全部参数；固定别名永不变化；存在未列出的日期快照 |
| [结构化输出](https://developers.openai.com/api/docs/guides/structured-outputs) | Handling mistakes | Schema约束格式，内容仍可能出错 | JSON输出本身保证答案正确，或会被认定为蒸馏 |
| [推理最佳实践](https://developers.openai.com/api/docs/guides/reasoning-best-practices) | Advice on prompting | 提示应清楚简洁，不必要求展开思维链 | 需要伪造聊天上下文才能正常推理 |
| [推理模型](https://developers.openai.com/api/docs/guides/reasoning) | Allocating space for reasoning | 输出token预算也影响推理空间，耗尽可能没有可见答案 | 要求短答案就能把总生成预算安全压到几十token |
| [限流说明](https://developers.openai.com/api/docs/guides/rate-limits) | Retrying with exponential backoff | 尊重Retry-After、限制尝试和总耗时、避免嵌套重试；额度/计费错误需处理后恢复 | 任意本地限频数值等于OpenAI反滥用阈值或保证不被误判 |
| [数据控制](https://developers.openai.com/api/docs/guides/your-data) | Abuse monitoring/application state | 应用状态存储与服务方滥用监控是不同控制 | store:false或本地只保留两轮能关闭服务方的检测/所有留存 |
| [联系OpenAI支持](https://help.openai.com/en/articles/6614161-how-can-i-contact-support) | API request IDs | 故障反馈可附请求ID、时间和配置等证据 | 支持请求会保证白名单、恢复质量或承诺结果 |
| [LINUX DO：Astra与糖果题](https://linux.do/t/topic/2862502) | 2026-09-06，作者自述 | 用户报告糖果题作答速度和结果，并讨论题目是否仍有代表性 | “已经背题”是已证实的模型训练事实 |
| [LINUX DO：降智困惑](https://linux.do/t/topic/2859772) | 2026-09-05，作者自述 | 有用户报告糖果题通过但其他任务表现差 | 已受控测得误判率，或能确认所有问题由反蒸馏引起 |

社区帖只用于说明存在体验报告和评测有效性疑问；技术参数与恢复策略采用官方资料和本地代码。检索中出现的推广帖、转述和“100%识别降级”断言不作为设计依据。

本次没有查询或修改生产服务器、发送真实检测请求。此前生产只读记录中的OAuth `invalid_response`属于协议处理失败，尚无失败原文，因此不能在此替换为“已命中反蒸馏”的根因结论。当前本地协议回放使用合成样本。
