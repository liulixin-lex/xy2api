# M3 管理接口样例

复制自 F 发布的 cache_policies_contract/m3m4 v1.1，22 个 M3 JSON 保持原字节，SHA256.json 固定输入；父目录 M1 夹具不改，M4 样例留给 M4。

五份列表包括 M2 的 northstar 与 observability.persist_trace_hourly，实际 HTTP 响应全字段结构比较，不再投影或替换卡片。仅归一化已验证为 UTC RFC3339 的时间值。其他17份样例与v1.0.3相同。

回放按样例固定 revision、diff.before、时钟及审计。候选查询失败由 error 桩注入；disable validate/PUT 使用 panic 桩证明没有账号查询，保留 warnings、错误码与完整 metadata.errors 比较。
