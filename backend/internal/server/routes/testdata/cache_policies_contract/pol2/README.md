# FX-POL-2 快照陈旧样例（M3 构建）

复制自 F 发布的 cache_policies_contract/pol2 v1.0，7 个样例保持原字节，SHA256.json 固定输入，摘要与 pol2/SHA256SUMS 一致。

回放沿用 m3m4 源样例（`_derived_from`）的夹具，先成功刷新一次，再让设置读取失败并越过 `gateway.cache_policy.max_stale_seconds`，然后恢复读取并发出请求，比较完整响应。PUT 样例另外验证写入成功后陈旧标记保留到下一次成功刷新。

四个 M4 样例与三个 M3 样例都在 POL2 回放测试中使用真实路由执行。M3 样例按原构建能力回放；M4 样例分别使用完整与 dormant 能力。M31 组合只向 upstream_identity 投影 max_inflight_per_key，其余字段仍严格比较。
