# Tasks

## P1′ 门控、接线与 schema（B）

- [x] 设置 defaults/normalize/validate/clone 与账号 bool true 开关。
- [x] 最终出站模型资格判定；原生 HTTP 尝试冻结，非资格零转换。
- [x] 流、JSON、SSE→JSON 三出口与本地终止。
- [x] collaboration 整节点 schema 豁免、真实 0.166 fixture 基线审计。
- [x] 分开记录 EFA 与 action 边际计数，异常 Warn，无正文。
- [x] 现行文档、部署说明与同步钩子登记。
- [x] B 定向测试、文档一致性、干净树同步审计。

## P1′ 核心变换（A）

- [x] 集成 apply/restorer 实现及核心反例测试，验收三回声与密文否决。

## P0 状态族（C）

- [x] 统一 ClassifyStateRejection、Result.StateFamily 与请求级类型标识，保留 v3 窄边界。
- [x] 接入 HTTP/SSE、账号副作用、WS 与 handler 的换号/罚号守卫，独立于设置与请求预算。
- [x] 定向验证六个家族、分类优先级、近似反例与 Encrypted 恒等旧谓词。
- [x] 定向验证修复边界，保留 v3 输出后状态拒绝回归用例，确认 S2/S6 可修复与 S1/S3/S4 正文不变。
- [x] 接入 state_rejection 观测、最终规则与协作 Applied/计数、协议位置片段 census 及已有 upstream_errors 的 state_family。
- [x] 同步现行文档与上游钩子登记；P0 定向测试、文档一致性与干净树同步审计通过。
- [x] 合入共享 OpenSpec 骨架，补全 P0 design/spec/tasks 并运行本次文档一致性检查；集成全量门禁仍由 PM 验收。

## 首轮红队修复

- [x] A-1/A-2/A-3：错误体、提示回声与协议容器重复键核心修复及定向回归（A）。
- [x] A-4/A-6/B-3：原始状态排除、仅边界发布统计、applied 失败 Warn 与真实 Forward 回归（B）。
- [x] B-2：消息工具专用 Calls/EFA 分母、MsgOpaque/MsgText 与指标接线（A/B）。
- [x] B-1/B-4/B-6：验证范围、混池/回退限制、缓存代价、unsupported 口径与版本抽检文档（B）。
- [x] r1 定向测试、vet 与 CI 口径 lint（service/resilience/handler）。
- [x] r1 文档一致性与提交后的干净树审计。
- [x] B-1 用户批准：历史产物连续性、资格来源观测、软/硬回退文档及正式回归。

## 第二轮红队修复

- [x] R2-A-3：任何归一化前冻结原始根对象唯一键结果，正式 OAuth 重复 store/previous 回归。
- [x] B2-1：曾支持 EFA 缺失且 message 非可疑；该分支已由 R3-B-1 撤回，当前仅显式 [] 连续性。
- [x] B2-4：曾按 HTTP 4xx 非 429 触发；已由 R3-B-2 统一为客户端错误分类并覆盖带内拒绝。
- [x] B2-2/B2-3：灰度覆盖兜底/借用、OAuth 与模型切换边界，客户端信号非授权，非 OpenAI provider 实测说明。
- [x] r2 B 定向测试、vet、CI lint 与文档一致性。
- [x] r2 B 提交后的干净树审计。

## 第三轮 PM 自审修复

- [x] R3-PM-1：Forward 收尾统一还原 applied 的换号错误体/消息，handler 不改，正式 applied/只观测表驱动回归。
- [x] r3 B 定向测试、vet、CI lint 与文档一致性。
- [x] r3 B 提交后干净树审计。

## 第三轮第二批修复

- [x] R3-B-1：撤回 message 连续性分支，仅显式 [] 生效；更新 provider 限制与实测边界。
- [x] R3-A-2：仅字面候选惰性冻结原始根/协议容器/状态/连续性，正式 map 归一化和转义声明回归。
- [x] R3-B-2：HTTP/带内统一复用客户端错误分类，in_band 标识，402 不告警与带内请求拒绝告警回归。
- [x] R3-B-3/R3-A-4：编号 runbook、可达账号查询步骤、规范名匹配兼容说明和内部诊断日志边界。
- [x] 同步甲错误解码/全载体还原文档；新集成基线定向测试、vet、CI lint 与 docs 通过。
- [x] 第三轮第二批提交后的干净树 audit。

## 第四轮红队修复

- [x] R4-A-1：在原始冻结中递归检查完整协作 namespace schema；两种 encrypted 重复键顺序纳入现有归一化回归表。
- [x] r4 B 定向测试、vet、CI lint 与文档一致性。
- [x] R4-B-1/B-2/B-3/B-4：收窄转义声明说明、request_id 诊断关联、网关服务标签、HTTP 观测钩子与跨分组改绑提醒。
- [x] 记录真实预检的生成正文私有地址边界（真实上游一轮出现 1 次，协议身份与父子闭环正常）。
- [x] r4 B 最终文档/登记补充后的干净树 audit。

## 集成与验收（PM）

- [x] API Key 三站 × 两模型预检 2a/2b。
- [ ] 两路红队复核与集成全量门禁。
- [ ] 授权后推送、PR、上线及逐模型验收。
