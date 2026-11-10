# 插件开发

本文面向为 XY2API 开发、打包和发布插件的开发者，说明插件能做什么、宿主与插件怎样分工、运行时要遵守的协议、配置界面如何与宿主通信，以及包清单、签名和发布前检查。插件是一个独立的可执行文件加一组静态页面，打成 `.s2plugin` 包，由管理员手动上传到 XY2API。

仓库只提供公开协议和宿主实现，不提供插件示例工程、打包器或密钥生成工具。协议以 `backend/pkg/pluginapi/` 为准，本文与其中文档冲突时以代码为准。

## 概览

- **交付物**：`.s2plugin` 是 ZIP 文件，包含清单、签名、各平台的可执行文件和静态 UI。XY2API 不从网络下载插件，也不需要 Docker。
- **运行方式**：插件启用后，宿主用 `hashicorp/go-plugin` 拉起与当前操作系统和 CPU 架构匹配的可执行文件，通过本机 gRPC 流传递请求和响应。插件进程随宿主退出。停用时先停止接收新请求，再等待进行中的请求结束。
- **多实例**：宿主把验签通过的原始包存入数据库。其他实例缺少本地文件时会重新验签、解包，并周期性对齐启用状态、灰度比例和加密配置。所有实例必须连接同一个数据库并使用相同的加密密钥。
- **不是沙箱**：独立进程是代码和发布边界，不是操作系统级隔离。宿主启动插件时不传递自己的环境变量，也丢弃插件的标准输出和标准错误。但插件以 XY2API 的系统用户运行，拥有该用户能访问的文件和网络。只应安装可信发布者签名的包。

## 能力边界

宿主目前只接受一种能力：`openai.oauth.outbound_transport.v1`。

| 范围 | 插件负责 | 宿主负责 |
|---|---|---|
| 适用账号 | `platform=openai` 且 `account_type=oauth` 的上游 HTTP 请求 | API Key 账号、其他平台、OAuth 登录与 Token 刷新 |
| 网络 | 建立真实的上游 HTTP/TLS 连接，返回原始 HTTP 响应 | 账号选择、代理配置的下发 |
| 响应 | 原样返回状态、响应头和响应体 | 响应状态处理、SSE 解析、错误映射、用量统计、计费和下游输出 |

其他约束：

- 命中插件的 OAuth 账号如果配置为 WebSocket 模式，会改用 XY2API 的 HTTP Bridge，不直接建立上游 WebSocket，避免绕过 v1 HTTP 协议。
- 灰度按账号 ID 稳定分桶。未命中的 OAuth 账号继续走内置路径。
- 已命中插件的账号在插件不可用时**失败关闭**：请求返回错误，不会悄悄切回内置网络路径。

## 公开契约

| 文件 | 内容 |
|---|---|
| [`v1/plugin.proto`](../../backend/pkg/pluginapi/v1/plugin.proto) | `TransportPlugin` 与 `HostService` 两个 gRPC 服务的消息定义 |
| [`v1/runtime.go`](../../backend/pkg/pluginapi/v1/runtime.go) | 协议版本常量、握手配置和插件进程入口 `pluginv1.Serve` |
| [`v1/manifest.schema.json`](../../backend/pkg/pluginapi/v1/manifest.schema.json) | `manifest.json` 的 JSON Schema |
| [`README.md`](../../backend/pkg/pluginapi/README.md) 与 [`docs/`](../../backend/pkg/pluginapi/docs/) | 协议目录下的说明：[开发指南](../../backend/pkg/pluginapi/docs/development.md)、[UI Bridge](../../backend/pkg/pluginapi/docs/ui-bridge.md)、[包格式](../../backend/pkg/pluginapi/docs/package-format.md)、[安全边界](../../backend/pkg/pluginapi/docs/security.md) |

当前协议版本：`plugin_protocol` 1、`transport_api` 1、`ui_bridge` 1、`HostServiceAPIVersion` 2。插件通过进程协议协作，不使用 Go 动态链接，不要求与宿主使用相同的编译器。

## 创建插件工程

用 Go 编写时，直接复用宿主的 `pluginv1` 包。宿主的 Go 模块（`github.com/liulixin-lex/xy2api`）位于仓库的 `backend/` 子目录，建议检出仓库后在插件工程的 `go.mod` 中用 `replace` 指向本地 `backend/` 目录。插件工程的 Go 版本不能低于 `backend/go.mod` 的 `go` 指令。

推荐目录：

```text
my-plugin/
├── cmd/<plugin>/main.go      入口，只调用 pluginv1.Serve
├── internal/config/          配置结构、默认值、严格校验与规范化
├── internal/transport/       HTTP 客户端、代理、请求头与请求体、响应流、资源回收
├── ui/index.html             配置界面
├── ui/assets/                界面用到的脚本、样式、图片
├── manifest.source.json      手工维护的清单源文件
└── README.md
```

配置解析和传输逻辑放在独立包里，这样不启动子进程也能做单元测试。宿主通过 go-plugin 握手（环境变量中的 magic cookie）识别插件，直接在终端运行插件可执行文件不会进入服务状态。

## 运行时契约

插件实现 `TransportPlugin` 服务：

| 方法 | 要求 |
|---|---|
| `GetInfo` | 返回的 `plugin_id`、`plugin_version`、`protocol_version`、`transport_api_version` 必须与清单和宿主协议版本一致，否则宿主拒绝启动 |
| `Health` | 快速返回能否接收新请求，不做昂贵探测。启动时不健康会导致启用失败。`status_json` 是可选的自定义状态快照，必须无副作用 |
| `ValidateConfig` | 严格解析 JSON，拒绝未知字段、非法范围和受保护的请求头，返回完整的规范化配置。规范化结果必须是 JSON 对象，不超过 4 MiB |
| `ApplyConfig` | 原子切换配置。失败时保留旧配置和旧连接。必须允许重复应用同一份配置 |
| `TestConfig` | 针对当前环境和已保存配置做快速诊断，返回简短可展示的 `message`，可附 `latency_ms` 和 `status_json` |
| `Forward` | 双向流：接收请求帧，发出上游请求，按顺序返回响应帧 |
| `InitHostServices` | 可选。不需要宿主服务时返回 `Unimplemented`，宿主会跳过 |

**帧顺序**：请求为 `start`、零到多个 `body_chunk`、`body_end`；响应为 `start`、零到多个 `body_chunk`、`end`。无法继续时发送 `error` 帧。

- 请求 `start` 携带方法、URL、Host、请求头、`proxy_url`、账号 ID、账号并发数、平台、账号类型、`content_length` 和 `has_body`。
- 响应 `start` 携带状态码、状态行、协议版本、响应头和 `content_length`；`end` 携带接收字节数和耗时。

**`request_sent` 必须准确**：`ForwardResponseError.request_sent` 只有在能确认尚未调用上游 HTTP Transport 时才能为 `false`。一旦已经调用，或无法确认上游是否收到请求，就返回 `true`。宿主据此决定能否切换账号重试，判断错误会导致同一请求被重复执行。

**资源管理**：

- 复用 HTTP Transport 和连接池，不要为每个请求新建连接池，也不要无限缓存按账号区分的客户端。
- 配置切换后关闭旧的空闲连接。
- 用 gRPC 流的 context 取消 DNS 解析、建连、上传和响应读取。
- 始终关闭上游响应体。

**敏感数据**：插件会处理真实的 OAuth `Authorization` 请求头。请求头、请求体、代理凭据和上游敏感响应不能出现在错误消息、诊断结果或 UI 通知中。插件不应读取、刷新或持久化 OAuth Token。

## 宿主服务（可选）

`HostService` 是宿主反向提供给插件的通用能力，适合有状态的插件。宿主启动插件后调用 `InitHostServices`，传入一个 go-plugin broker 流 ID 和 `host_service_api_version`；插件拨号回宿主，得到 `HostServiceClient`。宿主服务与插件进程同生命周期，插件退出时自动关闭。新增宿主能力只追加 RPC，不改变传输协议版本。

**键值存储**：`KVGet`、`KVSet`、`KVDelete`、`KVList`，存储在 Redis 中，多实例共享，跨重启保留。

| 限制 | 值 |
|---|---|
| 命名空间隔离 | 宿主按服务该连接的插件身份划分，插件无法读写其他插件的数据 |
| 字符集 | `namespace` 与 `key` 只允许 `[A-Za-z0-9._-]` |
| 长度 | `namespace` 最长 128，`key` 最长 256 |
| 单个值 | 最大 256 KiB |
| `ttl_seconds` | `0` 表示不过期；正值最长 90 天；负值拒绝 |
| `KVList` 条数 | 默认 100，最多 1000 |

**账号目录**（`HostServiceAPIVersion` 2）：

- `ListAccounts` 列出插件能力范围内的账号。返回的 `AccountInfo` 包含可调度状态（`schedulable`）和 `metadata_json` 形式的完整可读字段，不含凭据。暂停中的账号也会列出，插件可据此跳过它们。
- `ResolveOutboundIdentity` 返回宿主为该账号实际请求附加的访问令牌、请求头和代理地址。这些数据与凭据同等敏感，不得写入日志或持久化。
- 两个调用都限制在插件所声明能力覆盖的账号范围内。

## 配置

- 插件定义配置结构和默认值，宿主用 XY2API 的密钥加密后存入数据库。
- 字段统一使用 `snake_case`。空对象应规范化为包含全部默认值的完整配置。
- `ValidateConfig` 与 `ApplyConfig` 复用同一套校验，可用 `json.Decoder.DisallowUnknownFields` 之类的严格解析。
- 保存顺序：宿主先调用 `ValidateConfig`，拿到规范化配置后调用 `ApplyConfig`，成功后再加密写库；写库失败时宿主会把旧配置重新应用回去。
- 敏感配置不要放进 URL、UI 通知、诊断结果或日志。

## 配置界面与 UI Bridge

配置界面是包内的静态页面，不需要修改 XY2API 前端。

**加载方式**：

- 管理员每次打开配置页，宿主创建一个 30 分钟有效的 UI 会话，在 `sandbox="allow-scripts"` 的 iframe 中加载 `/api/v1/plugin-ui/<asset-token>/index.html#bridge_token=<bridge-token>`。iframe 不授予 `allow-same-origin`。
- 资源令牌只用于读取包内 `ui/` 文件。Bridge Token 只出现在 URL fragment 中，不会发到服务器。
- 页面响应带有严格的 CSP：`default-src 'none'`，脚本和样式只允许包内文件和内联代码，`connect-src 'none'`、`form-action 'none'`。页面只能加载包内资源，不能发起 `fetch` 或 XHR，CDN、远程脚本、外部字体都不可用。
- iframe 中没有管理员 Token，不能访问管理 API、Cookie、父页面 DOM 或宿主的浏览器存储。

**页面流程**：加载包内资源 → 注册 `message` 监听 → 发送 `sub2api.plugin.ready` → 调用 `config.load` 渲染表单 → 编辑后 `config.save` → 需要测试时先保存再 `config.test` → 页面卸载时清理监听和未完成的请求。

**消息**：

| `type` | 参数 | 成功时返回 | 管理员二次验证 | 对应插件 RPC |
|---|---|---|---|---|
| `sub2api.plugin.ready` | 无 | 无响应 | — | — |
| `config.load` | 无 | `config` | 否 | 读宿主数据库 |
| `config.save` | `config` 对象 | 规范化后的 `config` | 是 | `ValidateConfig` + `ApplyConfig` |
| `config.test` | 无 | `result` | 是 | `TestConfig`（测试已保存的配置） |
| `plugin.status` | 无 | `result`：`{healthy, message, status_json}` | 否 | `Health` |
| `ui.resize` | `height` | 无响应 | — | — |
| `ui.notify` | `level`、`message` | 无响应 | — | — |

`config.test` 可能产生副作用，结果由插件页面自己展示，宿主不会对成功结果弹提示。需要轮询运行状态时用只读的 `plugin.status`（宿主侧对应 `GET /api/v1/admin/plugins/:id/status`），不要拿 `config.test` 轮询。插件未运行时 `plugin.status` 返回 `healthy=false` 且不含 `status_json`。

**信封格式**：页面发出 `{"source": "sub2api-plugin-ui", "bridge_token": "...", "type": "config.load", "request_id": "..."}`；宿主以 `<type>.result` 回复 `{"source": "sub2api-plugin-host", "bridge_token": "...", "request_id": "...", "ok": true, ...}`。

**页面必须执行的校验**：`event.source === parent`、消息来源标识、Bridge Token、等待中的 `request_id`。每个请求设置超时。页面要适配窄屏和明暗主题，并处理加载、保存、测试、超时和未保存的状态。

## 包清单

只维护 `manifest.source.json`，由自己的打包流程生成最终 `manifest.json`，补全运行时路径和文件哈希。签名之后不要再格式化 `manifest.json`。宿主严格解析清单：出现未知字段、包含多个 JSON 对象或超过 2 MiB 都会拒绝安装。

| 字段 | 要求 |
|---|---|
| `schema_version` | 固定为 `1` |
| `id` | 小写命名空间标识，至少包含一个 `.`、`_` 或 `-` 分隔段，最长 160，例如 `com.example.openai-transport` |
| `name`、`version` | 名称不能为空，最长 160；版本为语义化版本 |
| `requires.sub2api` | 宿主兼容范围，必填 |
| `requires.recommended_sub2api_version`、`requires.tested_sub2api_versions` | 建议版本（用于管理页展示）与实际验证过的版本 |
| `requires.plugin_protocol`、`transport_api`、`ui_bridge` | 必须等于宿主当前的协议版本，目前都是 `1`，不一致时拒绝安装 |
| `capabilities` | 至少一项，目前只能是 `{"id": "openai.oauth.outbound_transport.v1", "platform": "openai", "account_type": "oauth"}` |
| `runtimes` | 以 `<GOOS>-<GOARCH>` 为键（如 `linux-amd64`、`windows-amd64`、`darwin-arm64`），值为包内可执行文件路径。宿主只启动与自身平台匹配的那一项，缺少该项时拒绝安装 |
| `ui.entrypoint` | `ui/` 下的入口页面，通常为 `ui/index.html` |
| `files` | 所有运行时和 UI 文件的小写十六进制 SHA-256；不包括清单和签名文件 |

**版本兼容**：`requires.sub2api` 与宿主的 **Sub2API 兼容基线**比较，而不是 XY2API 产品版本。兼容基线记录在 `backend/cmd/server/SUB2API_COMPAT_VERSION`，管理页的插件详情也会显示。范围表达式由空格或逗号分隔的条件组成，每个条件形如 `>=X.Y.Z`、`<X.Y.Z`、`<=`、`>`、`=`，必须全部满足。

| 结果 | 条件 | 管理端表现 |
|---|---|---|
| 不兼容 | 兼容基线不在 `requires.sub2api` 范围内，或宿主升级后协议版本不再一致 | 可以安装和查看，不能启用 |
| 未测试 | 在范围内，但不在 `tested_sub2api_versions` 中 | 管理员确认后才能启用 |
| 兼容 | 在范围内且已列入测试版本 | 可以直接启用 |

示例（兼容基线为 0.2.15 时）：

```json
{
  "schema_version": 1,
  "id": "com.example.openai-transport",
  "name": "Example OpenAI Transport",
  "version": "0.1.0",
  "requires": {
    "sub2api": ">=0.2.15 <0.3.0",
    "recommended_sub2api_version": "0.2.15",
    "tested_sub2api_versions": ["0.2.15"],
    "plugin_protocol": 1,
    "transport_api": 1,
    "ui_bridge": 1
  },
  "capabilities": [
    {"id": "openai.oauth.outbound_transport.v1", "platform": "openai", "account_type": "oauth"}
  ],
  "runtimes": {
    "linux-amd64": {"path": "runtimes/linux-amd64/plugin"},
    "windows-amd64": {"path": "runtimes/windows-amd64/plugin.exe"}
  },
  "ui": {"entrypoint": "ui/index.html"},
  "files": {
    "runtimes/linux-amd64/plugin": "<sha256>",
    "runtimes/windows-amd64/plugin.exe": "<sha256>",
    "ui/index.html": "<sha256>"
  }
}
```

只在清单中声明宿主已经实现的能力。声明其他能力不会产生新的路由，安装时就会被清单校验拒绝。

## 包格式与签名

```text
manifest.json
signature.json                 生产包必需
runtimes/<goos>-<goarch>/<binary>
ui/index.html
ui/assets/...
```

- 包内路径必须是规范的相对路径。不允许绝对路径、`..` 路径、重复路径、符号链接、未声明的文件或声明了却缺失的文件，文件数不超过 512。
- 上传大小与解压后大小分别受 `plugins.max_upload_bytes`（默认 128 MiB）和 `plugins.max_uncompressed_bytes`（默认 256 MiB）限制。
- 启动插件时，宿主还会校验可执行文件的 SHA-256。

`signature.json` 的格式：

```json
{"algorithm": "ed25519", "key_id": "my-publisher-v1", "signature": "<Base64>"}
```

签名对象是 `manifest.json` 的精确原始字节，签名和公钥都使用标准 Base64 编码。清单中的文件哈希再覆盖运行时与 UI 文件。私钥只放在受控的构建机或 CI 密钥中，不能进入插件包、源码仓库或 XY2API 的运行环境。

部署者在 XY2API 配置中添加发布者公钥：

```yaml
plugins:
  allow_unsigned: false
  trusted_publishers:
    my-publisher-v1: "<Base64 Ed25519 公钥>"
```

- `trusted_publishers` 的键必须与 `signature.json` 的 `key_id` 完全一致。
- 官方 OpenAI Transport 插件的公钥内置在宿主中，对应的 `key_id` 保留给该插件使用，配置无法覆盖。第三方发布者请使用其他 `key_id`。
- 轮换密钥时，先让部署者加入新公钥，再发布新签名的包，最后停用旧密钥。
- `plugins.allow_unsigned: true` 只用于在隔离的本地环境调试自己构建的包，测试完立即改回 `false`。

插件相关的其他配置项（`plugins.data_dir`、`plugins.start_timeout_seconds` 等）见 [配置](../deployment/configuration.md) 和 [`deploy/config.example.yaml`](../../deploy/config.example.yaml)。

## 测试

插件工程内至少覆盖：

- 配置：默认值、未知字段、边界值、深复制。
- 身份：插件 ID、版本与协议版本。
- 请求：分块请求体、无请求体、固定 `Content-Length`。
- 响应：状态码、重复响应头、流式响应、读取错误。
- 生命周期：上下文取消、插件退出、超时。
- 代理：开启与关闭两种情况。
- 包：文件哈希、签名、路径穿越、目标平台运行时。
- UI Bridge：加载、保存、测试、错误和超时。

宿主仓库中的 `TestPluginRuntimeIntegration`（`backend/internal/service/plugin_runtime_integration_test.go`）用环境变量 `SUB2API_TEST_PLUGIN_PACKAGE` 指定一个真实构建的包，完成安装、启动、应用配置和转发。该测试是按官方 OpenAI Transport 插件编写的：它要求包由宿主内置公钥签名，并使用该插件的配置字段和 UI 文件。第三方插件可以照着它的写法编写自己的进程级集成测试。

## 安装、启用与灰度

1. 超级管理员在管理后台的 **插件管理** 页（`/admin/plugins`）上传 `.s2plugin` 包。侧栏是否显示该页由系统设置中的“插件管理”开关控制，该开关不影响已加载的插件。上传、启用、停用、删除、保存配置和测试都需要管理员二次验证。
2. 新安装的插件默认停用。先确认兼容性状态和签名状态，再打开配置页保存配置并测试。
3. 启用时设置灰度比例（默认 100%），按账号 ID 稳定分桶。宿主启动进程后依次校验 `GetInfo`、`Health` 和配置，全部通过才开始接收流量。
4. 启用、停用和改配置通常不需要重启 XY2API。

API Key 账号和未命中灰度的 OAuth 账号始终走原有路径。

## 发布前检查清单

- 插件版本与 `GetInfo` 的返回值一致。
- `requires.sub2api` 的范围经过验证，没有覆盖未测试过的不兼容版本；`tested_sub2api_versions` 与实际测试一致。
- 每个支持的操作系统和架构都有运行时文件。
- 生产包带有效的 `signature.json`，公钥已交给部署者。
- 包内没有私钥、源映射、测试数据、日志或临时文件。
- UI 不依赖外部资源，不保存宿主会话信息。
- 配置切换、请求取消、响应关闭和错误重试的语义都测试过。
- 发布说明写明升级、停用、回滚方法和兼容版本。

## 常见问题

| 现象 | 排查方向 |
|---|---|
| 安装时提示发布者密钥不受信任 | `signature.json` 的 `key_id` 与 `plugins.trusted_publishers` 的键是否完全一致，公钥是否为 32 字节的标准 Base64 |
| 安装时提示签名校验失败 | 签名后是否改动过 `manifest.json`，哪怕只是重新格式化 |
| 安装时提示不支持当前运行平台 | `runtimes` 中是否有宿主的 `<GOOS>-<GOARCH>` 键，对应文件是否列入 `files` |
| 插件显示不兼容 | `requires.sub2api` 是否覆盖当前 Sub2API 兼容基线（不是 XY2API 版本号） |
| 启用失败，提示运行时信息与清单不一致 | `GetInfo` 返回的 ID、版本、协议版本是否与清单一致 |
| 插件进程无法启动 | 可执行权限和运行用户权限是否正确，启动是否超过 `plugins.start_timeout_seconds`，启动时 `Health` 是否返回健康 |
| 配置页无法加载 | `ui.entrypoint` 与文件哈希；Bridge Token 与 `event.source` 校验；页面是否尝试加载外部资源 |
| 保存后配置未生效 | `ValidateConfig` 返回的规范化配置与 `ApplyConfig` 的结果 |
| 请求失败后被重复执行 | `ForwardResponseError.request_sent` 是否如实反映请求可能已经发出 |
| 插件内的日志看不到 | 宿主丢弃插件的标准输出和标准错误。把诊断信息放进 `TestConfig` 或 `Health` 的 `status_json` |

## 需要新能力时

如果插件需要支持其他平台、其他账号类型或新的消息字段，应先扩展并版本化公开协议，再由宿主实现能力匹配和生命周期处理。不要在清单中声明宿主尚未实现的能力。这样旧插件可以继续运行，新宿主也能明确拒绝不兼容的插件。
