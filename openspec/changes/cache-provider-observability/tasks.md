# 任务与验证

- [x] 建立独立主分支基线与开发分支，登记契约版本和范围。
- [x] 准备真实请求构造、转发与计费基线夹具。
- [x] 在未修改主分支录制 golden，固定 SHA256 与来源。
- [ ] 放开 platforms、ingress_chain_percent，补管理员校验、schema 与快照。
- [ ] 验证 stats 平台分区：默认兼容、容量污染、EWMA、会话隔离、历史查询及 handler 契约（实现与测试已补，待门禁）。
- [ ] 接入 Anthropic、Gemini、Messages 桥，复用有界 worker 与既有决策（Messages 接线和矩阵测试已补，基线对照待运行；两个 provider 挂点仍待实现）。
- [ ] 实际事件通过追加契约，P/read 与最终计费逐字段相等。
- [ ] 完成等值的 Anthropic 归一化及粘性日志脱敏；计费别名另行处理。
- [ ] 接入 origin、relay_family 与现有 WS Bridge 标记。
- [ ] 接入入站链及 client/gateway 分叉分类。
- [ ] 接入 S1/S2/S6，验证在途数不含自身。
- [ ] 关闭矩阵零执行，出站和计费 golden 不变。
- [ ] 合并执行全 tag 编译、unit/race、lint、契约与兼容门禁。
- [ ] 更新同步登记及部署说明，固定提交并完成独立评审。

- [ ] 验证 FX-OBS-1：alias-only 为 n/a_usage、无 na_reason，canonical 显式零及优先级不变，实际 emitter 输出通过 v1.2（代码和回归已补，待重门禁）。
- [ ] 验证 FX-L04-7：相同内容、同组不同 API key 的 provider digest 与历史键不同。
