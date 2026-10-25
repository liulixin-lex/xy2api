# M3 管理接口样例

复制自 F 发布的 cache_policies_contract/m3m4 v1.1，22 个 M3 JSON 保持原字节，SHA256.json 固定输入；父目录 M1 夹具不改，M4 样例留给 M4。

五份列表包括 M2 的 northstar 与 observability.persist_trace_hourly，实际 HTTP 响应全字段结构比较，不再投影或替换卡片。仅归一化已验证为 UTC RFC3339 的时间值。其他17份样例与v1.0.3相同。

回放按样例固定 revision、diff.before、时钟及审计。候选查询失败由 error 桩注入；disable validate/PUT 使用 panic 桩证明没有账号查询，保留 warnings、错误码与完整 metadata.errors 比较。

T4.0采用m3m4 v1.1.1（响应与v1.1相同）：20份M4样例在真实管理员handler逐字段回放，20份相关反例复用同输入的实际输出作负向比较。M4_SHA256.json和M4_INVALID.json分别固定原字节。M3的17份非list样例在M4构建保持完整比较；其5份旧构建list由4份M4完整list替代，另保留账号候选失败与观测关闭两个M3边界，前四卡原样比较，新增两卡明确取F的M4默认卡。原M3 JSON不改写。

构建能力只有fallback_warning样例注入Anthropic观测已覆盖；其余样例按未覆盖，missing在on/masked_by_master出现。关闭方向用panic桩证明零分组查询，不复用旧回退事实。

T4.0 X-B1采用m3m4 v1.2：原20份M4样例显式注入完整L-04/L-03/L-07能力，响应原字节不变；10份m4_dormant样例采用生产默认的全false能力。M4_DORMANT_SHA256.json固定10份原字节正例，M4_DORMANT_INVALID.json固定9份反例；反例与原有20份合计29份，均从发布目录逐字节复制。

默认构建拒绝能力字段的启用或扩张，关闭、收窄及整体恢复默认不查分组；rollout本身不属于能力字段。数据库仅激活未实现行为时为blocked且active=false，并给出not_implemented_in_build。实际HTTP回放和Go编译留给同一个r3重任务；原字节及契约校验不代表这些实跑已经通过。
