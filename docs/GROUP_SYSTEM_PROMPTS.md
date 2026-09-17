# 分组标识与系统提示词

## 本地实现范围

基线：`main / 41fd8591c25b075e3f95df58d3b46283a3a70b68`。实现目录：`/xy/artifacts/group-system-prompts/work`。原仓库 `/xy/xy2api` 保持原样。本次不包含提交、推送、发版或生产部署。

管理员新建、编辑分组时，可分别设置真实专属权限和用户端专属标识展示。`show_exclusive_badge` 默认 `true`，隐藏标识不改变 `is_exclusive`、订阅要求或授权名单。用户端仍显示获授权分组的名称、倍率和模型，隐藏的专属分组不归入公开分类。管理员仍看到真实分组类型。

`system_prompt_config` 是管理员专用配置，普通用户 DTO、模型广场和可用渠道响应不包含提示词。标准模式和简易模式均可配置提示词。

```json
{
  "prompt": "通用系统提示词",
  "scope": "selected",
  "models": ["team-alias"],
  "model_prompts": {
    "other-model": "这个模型的独立系统提示词"
  }
}
```

模型名去除首尾空白后精确匹配，区分大小写，不支持通配符；匹配的是客户端调用名称。非空模型独立提示词优先于通用提示词，且可用于通用名单之外的模型。独立内容清空则回到通用规则；通用内容空白或未命中范围时不注入。通用内容非空且范围为指定模型时，至少选择一个模型。编辑时省略配置保留旧值，提交完整空配置清除；复制分组深复制两项新配置。

## 请求处理

入口中间件在账号/Composite 模型映射之前保存分组配置快照与客户端模型，后续账号切换及 HTTP 回退复用此快照。最终出站请求在已有协议转换和模板处理之后注入，Bedrock 在签名前注入。每次重试从尚未注入的基础请求构造，不通过文本比较删除用户原有内容。

| 协议 | 注入位置 |
| --- | --- |
| Chat Completions | `messages` 最前方的 `system` 消息 |
| Responses | `instructions` 前缀，与原值通过空行分隔 |
| Anthropic | `system` 字符串前缀或文本块，保留平台固定前导块 |
| Gemini / Code Assist / Antigravity | `systemInstruction.parts` 前置文本，兼容已有 snake_case 和外层 `request` |

原有 system、developer、结构化对话、工具定义和缓存控制块保留。配置未命中时直接返回原请求字节。启用提示词时，冲突模型字段及有歧义的被修改字段报错；注入错误不会继续发送缺少管理员配置的请求。

Responses WebSocket 在每个 `response.create` 注入一次，包括首帧、后续帧和 HTTP bridge。模型省略时沿用对应入口已有的会话模型语义；透传入口的 `session.update` 会更新会话模型。连接使用建立时的配置快照，管理员修改对新连接生效。

文本生成及其 token 计数路径使用相同有效提示词。原本不支持计数的接口仍保持不支持；已有本地估算路径计入新增文本。图片、视频、实时音频接口，以及 Gemini IMAGE 输出，不属于本次注入范围。

## 数据与交接

追加迁移 `249_group_system_prompt_policy.sql` 和对应 checksum。已有行默认展示标识、空提示词；Ent schema 和生成代码同步新增 boolean、JSONB 字段。认证查询投影、L1/L2 快照同步新增字段，快照版本为 26；旧快照不再复用。数据库触发器将两项配置变更送入既有失效 outbox，经 Redis 广播刷新其他实例。

本地验证的命令、字面 stdout/stderr、退出状态与失败后修正记录位于 `/xy/artifacts/group-system-prompts/VERIFICATION.txt`；机器记录为同目录 `events.jsonl`。浏览器使用隔离模拟数据，不连接真实账号。四角色交付为 `MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh`。回滚脚本恢复源码归档，不执行数据库降级或生产操作。

复验环境使用 Go 1.27.0、项目固定的 pnpm 9.15.9；PostgreSQL 18.1 和 Redis 8.4 通过一次性容器运行。受本机内存限制，大型构建串行执行。最终通过与未通过范围以验证记录和交付结果为准，不将中断或跳过标记为通过。
