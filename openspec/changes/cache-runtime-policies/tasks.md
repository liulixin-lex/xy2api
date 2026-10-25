# 任务与验证

- [x] 策略服务、事务 revision、默认关闭及严格解析。
- [x] 管理接口、确认重算、审计与历史投影。
- [x] 契约样例逐文件 SHA256 登记，M1 与未来构建可用性区分。
- [x] cache_trace 请求快照、运行时采样与 worker 参数接缝。
- [x] 契约、服务、race、真实 PostgreSQL 并发验证。
- [x] 关闭态出站字节与零分配、lint、完整韧性 ci_gate。
- [x] 最终源码与验证证据绑定。
- [ ] T0a.4 之后的增量 rebase、独立评审与 M1 集成。


### M3 / L-01（本地完成，待评审）

- [x] FE-Q3 管理员账号 options、过滤后分页与500上限；查询故障降级，关闭方向免账号查询；账号invalid清理生效warnings。
- [x] 准备22份原字节M3管理HTTP契约回放及候选资格、隐私、查询故障降级与关闭免查询测试。
- [ ] 管理API unit/race与22样例实际回放，合并进M2变基后的单条门禁；此项尚未执行。

- [x] B-M3-1 响应回显还原与 L-01 独立 cohort 域；旧基点 focused 与原生分配矩阵通过。
- [x] B-M3-1-r1 按PM裁定改为原位null，mock无key显式null；响应形态与EOF边界断言已更新。
- [ ] 本轮null与管理员降级修复的Go验证，随M2变基后单批执行；既有r5结果仅作历史证据。
- [x] 七项非公开入口的只读来源核对，区分已有单测和 HTTP 可达性。
- [ ] M2 合入并变基后，单次完成全 tag、补修 race/zero/logger/compat/lint及管理员API契约门禁；旧基点余项按PM优先级中止。

- [x] upstream_identity 类型化快照、严格字段与账号校验、确认/审计接线。
- [x] Responses 缺键头部插入与 Chat 复用既有 UUID 的接线。
- [x] 显式值优先、四重建路径、会话作用域、关闭 golden/分配/零执行验证。
- [x] M3 实际事件契约、质量/重试兼容、race、lint 与四 tag vet 补证。
- [x] M1 合入后重基，并绑定验证产品源码与提交的文档差异。
- [ ] 独立评审、staging 与 CI；后续新 main 重基先跑四 tag vet。

## M4 T4.0 两个管理员策略键

- [x] anthropic_identity/routing 默认关闭的存储、校验、风险确认与审计；仅score。
- [x] 点路径实际diff、两种空列表语义、分组选项与失败降级、关闭零组查询。
- [ ] X-B1：独立L-04/L-03/L-07静态构建能力，默认拒绝启用并防御数据库遗留配置；v1.2回放。
- [x] 准备20份M4实际handler回放、相关反例与17份M3非list回放；额外保留M3列表边界。
- [ ] 单批五tag编译、触及包unit与实际样例回放通过（实现准备不代表已运行）。
- [ ] R增量评审与PR候选关闭态golden；P组装M4 PR及UI联调。


### M3.1 / L-01 启用前安全（开发中）

- [x] 写入 max_inflight_per_key、旧存储补齐、方向/schema/history 与全账号软提示。
- [x] 写入显式种子作用域，防止客户端伪造 v2 标记。
- [x] 写入还原 fail-safe、SSE 有界 oversize 处理、错误体隐私与指标。
- [x] 写入 24h 拒绝记忆、有界 in-flight 预留/释放，不新增重试。
- [x] 准备定向测试与 M31 策略/trace 回放；当前只做轻检。
- [ ] 在 M3 与 T4.0 后按规则执行一次合并 heavy，记录五 tag 与受影响门禁。
- [ ] R 增量评审及最终集成关闭态证据。


### M3.1 转换出口补修

- [x] Chat buffered/streaming 在实际注入门控内统一还原，覆盖成功、failed 与 cyber。
- [x] 原生 Responses 规则错误出口清除已知 U；新增结构注册及实际 Forward 回显测试。
- [x] Messages ingress 不可注入边界与 EOF/读错/关闭态控制测试已准备。
- [ ] 独立评审执行唯一合并定向补跑及关闭态 golden；staging 仅补 active 与 T4.0。
