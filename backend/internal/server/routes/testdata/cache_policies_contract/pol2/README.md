# FX-POL-2 快照陈旧样例

复制自 F 发布的 cache_policies_contract/pol2 v1.1，14 个样例保持原字节，SHA256.json 固定输入，摘要与 pol2/SHA256SUMS 一致。

本构建为 PR C（M3.1、T4.0、POL-2、M2b、T2.2、T2.3），回放只含 upstream_identity 的 pol2_m31_m3_validate_enable 与 pol2_m31_m3_put_200，以及 combo_prc 的 5 个 prc_pol2_* 陈旧 list。其余样例保留原字节但不回放：v1.0 不含 M3.1 的 upstream 增量，pol2_m31 的 list 不含 M2b 的 observability 与 T2.2/T2.3 的 anthropic_identity 字段。测试中的取代表逐一列出最终替代样例，并断言每个替代样例都在回放。

回放先成功刷新一次，再让设置读取失败并越过 `gateway.cache_policy.max_stale_seconds`，然后恢复读取并发出请求，逐字段比较完整响应。PUT 样例另外验证写入成功后陈旧标记保留到下一次成功刷新。
