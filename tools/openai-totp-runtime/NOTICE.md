# Worker 与运行依赖来源

`tools/openai_totp_rotation.py`、`tools/openai_totp_journal.mjs` 和 journal/rotation 的测试方案参考并改编自 `ranxi2001/sub2api` 提交 `12a49ef4ef6a2f116389701ac8f1fe620f27bbf5` 的同名文件。上游及 XY2API 仓库采用 LGPL-3.0，许可证正文见 [仓库 LICENSE](../../LICENSE)。XY2API 独立实现内部 API 客户端、claim-token 租约与 heartbeat、固定源码/运行依赖预检、取消和恢复保护；Node adapter 明确关闭安全页自动重放。

`source.json` 固定 `poxiao33/toSub2` 提交 `8548397e89bf80e508eda64a87e0d556d43abc84` 的源码 SHA-256。toSub2 采用 MIT License，Copyright (c) 2026 poxiao33；安装时完整保留该 checkout 的 LICENSE。toSub2 源码不复制进本目录，由操作者按 runbook 安装。

`package.json` 与 `package-lock.json` 基于上述 Sub2API 提交 `tools/reauth-runtime/` 的固定 `jsdom==26.1.0` 运行依赖锁；包名改为 XY2API，下载地址规范到 npm 官方 registry，包版本和 integrity 保留。完整传递依赖及各自许可证信息见锁文件。

Python 包固定在 `../openai_totp_requirements.txt`。不启动 toSub2 Web 控制台、SMS 或托管登录服务，不自动获取移动版本。
