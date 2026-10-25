# PR C 构建的 list 样例（combo_prc）

复制自 F 发布的 cache_policies_contract/combo_prc v1.1，10 个样例保持原字节，SHA256.json 固定输入，摘要与 combo_prc/SHA256SUMS 一致。

构建为 #136 合入后的 main 再加 M2b、T2.2、T2.3。`prc_list_*` 由 TestCacheProviderPolicyPRCListReplay 回放（新鲜快照），取代 cache_policies_m2b 中 10 个 m2/m2b list 样例；`prc_pol2_*` 由 TestCachePolicyPOL2ContractReplay 回放（陈旧快照），取代 pol2 中对应的 list 样例。能力按 `_fixture.build_capabilities` 预置，构建声明 T0b.4 的 Anthropic 观测。被取代的样例保留原字节，测试逐一列出替代样例并断言其在回放。
