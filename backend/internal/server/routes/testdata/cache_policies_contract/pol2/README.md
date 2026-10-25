# FX-POL-2 快照陈旧样例（M3 构建）

复制自 F 发布的 cache_policies_contract/pol2 v1.0，3 个适用于 M3 构建的样例保持原字节，SHA256.json 固定输入，摘要与 pol2/SHA256SUMS 一致。

回放沿用 m3m4 源样例（`_derived_from`）的夹具，先成功刷新一次，再让设置读取失败并越过 `gateway.cache_policy.max_stale_seconds`，然后恢复读取并发出请求，比较完整响应。PUT 样例另外验证写入成功后陈旧标记保留到下一次成功刷新。

pol2_m4_list_on、pol2_m4_list_masked_by_master、pol2_m4_dormant_list_db_enabled、pol2_list_killed_and_stale 需要 M4 构建，在组装 M4 时回放。
