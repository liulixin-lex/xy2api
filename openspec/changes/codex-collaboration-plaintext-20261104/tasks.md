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

- [ ] A-1/A-2/A-3：错误体、提示回声与协议容器重复键核心修复及定向回归（A）。
- [ ] A-4/A-6/B-3：原始状态排除、仅边界发布统计、applied 失败 Warn 与真实 Forward 回归（B）。
- [ ] B-2：消息工具专用 Calls/EFA 分母、MsgOpaque/MsgText 与指标接线（A/B）。
- [x] B-1/B-4/B-6：验证范围、混池/回退限制、缓存代价、unsupported 口径与版本抽检文档（B）。
- [ ] r1 定向测试、CI 口径 lint、文档一致性与干净树审计。
- [ ] B-1 会话连续性方案待用户决定；本轮不实现。

## 集成与验收（PM）

- [x] API Key 三站 × 两模型预检 2a/2b。
- [ ] 两路红队复核与集成全量门禁。
- [ ] 授权后推送、PR、上线及逐模型验收。
