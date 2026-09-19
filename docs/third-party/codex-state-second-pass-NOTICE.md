# STATE 二次优化参考记录

本轮在 XY2API 一期融合提交 `2a743f0afcf205e1f440d3f15a8655b412b38af9` 上独立实现。原 sub2api-state-kit 来源及许可继续见同目录原 NOTICE 和 UPSTREAM.json。

| 参考 | 固定版本／审查边界 | 采用与边界 |
| --- | --- | --- |
| [sub2api-state-kit](https://github.com/liulixin-lex/sub2api-state-kit) | ecf3b9acb6bd40ba9e920b1b21e3db446ecaaf27 | 保留一期账号配置、固定代理复验与守护设计 |
| [ccodex-sleep-state](https://github.com/gylive/ccodex-sleep-state) | b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6；GPL-3.0 | 参考封装时间、同票不续命和限流分类机制；本轮未复制其实现源码，在现有 Go 生命周期内独立实现 |
| [cpa-plugin-codex-turn-state](https://github.com/liulixin-lex/cpa-plugin-codex-turn-state) | 861756372a150788ca4a009bf4f3c7a45d713e32；MIT | 参考按账号／模型展示期限与刷新失败，不采用账号启停探测和明文代理展示 |
| [codex-turn-state-cache](https://github.com/liulixin-lex/codex-turn-state-cache) | 2a2a8f363029b6a4f4c939f2c2f782ae1c120b93；MIT | 参考唯一响应头值，不以长度或内存缓存替代生命周期 |
| [Sub2API #7332](https://github.com/Wei-Shaw/sub2api/issues/7332) | 公开讨论 | 账号／模型隔离及普通会话连续性；长度与效果报告仅为实验观察 |
| [zettabay](https://gpt.zettabay.com/) | 公开 HTML、JS、CSS 和接口调用形状 | 阶段诊断、时间和失败统计；未取得后端源码，未声称后端审计 |
| [Fernet 规范](https://github.com/fernet/spec/blob/master/Spec.md) | 封装结构 | 版本、时间及块长度保守解析；不声称 MAC 验证 |

本轮未引入这些项目的依赖、部署脚本或远端服务调用。自动化测试仅使用合成 STATE 和隔离数据库；真实上游验收仍单独进行。
