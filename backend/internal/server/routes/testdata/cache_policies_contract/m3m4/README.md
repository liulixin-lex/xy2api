# M3 管理接口样例

复制自 F 发布的 cache_policies_contract/m3m4 v1.0.3，22 个 M3 JSON 文件保持原字节；SHA256.json 固定输入。父目录 M1 夹具不改。M4 样例留给 M4 实现。

HTTP 回放的前置状态由样例提供：列表种入两组 Claude fallback；validate 种入对应 revision 与 diff.before；history 使用固定时钟及审计项。只归一化已验证为 UTC RFC3339 的时间值。

依据上游 README §2，northstar 仅为列表完整而提供，不属于 M3 契约。本分支尚未合入 M2，列表样例的 northstar 卡显式按冻结 M1 的 not_in_build 卡比较，并独立断言 available=false；其余字段仍全量比较。合入 M2 时须显式升级测试中的构建预期。M3 原始 JSON 不改。

候选未加载的 4 个 v1.0.3 样例原字节引入；List/GET 由 error 桩触发，disable validate/PUT 由 panic 桩证明完全没有账号查询。warnings 仅追加到含 schema 响应的 effective.warnings，validate 保持原生效警告。v1.0.2 的 4 个列表 northstar 选项修正与 mixed.diff.before=[101,102] 同步保留。
