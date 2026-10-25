# FX-POL-2 快照陈旧样例

复制自 F 发布的 cache_policies_contract/pol2 v1.1，14 个样例保持原字节，SHA256.json 固定输入，摘要与 pol2/SHA256SUMS 一致。

本构建包含 M3.1 与 M4 T4.0，回放 v1.1 的 7 个组合构建样例（pol2_m31_*），按 `_fixture.build_capabilities` 预置能力，缺省时三项均为 true。v1.0 的 7 个样例保留原字节但不回放：M3 样例描述仅 M3 的构建，且 v1.0 都不含 M3.1 的 upstream 增量。测试中的取代表逐一列出替代样例，并断言每个替代样例都在回放。

回放先成功刷新一次，再让设置读取失败并越过 `gateway.cache_policy.max_stale_seconds`，然后恢复读取并发出请求，逐字段比较完整响应。PUT 样例另外验证写入成功后陈旧标记保留到下一次成功刷新。
