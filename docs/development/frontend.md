# 前端约定

本文记录 `frontend/` 的开发约定，包括页面结构、路由权限、国际化、性能、动效与“减少动画”、通用交互，供修改管理端或用户端界面时对照。其中的性能约定都来自实测时找到的卡顿原因，改动相关代码前请先读。前端目录结构见 [项目结构与架构](architecture.md#frontend)，运行和测试命令见 [本地开发环境](local-setup.md)。以 `frontend/src` 的代码为准。

## 基础约定

- **技术栈**：Vue 3 + TypeScript、Vite、Pinia、Vue Router、vue-i18n、Tailwind CSS，测试用 Vitest。脚本驱动的动效用 GSAP。
- **页面外壳**：登录后的页面放在 `AppLayout` 中，认证相关页面用 `AuthLayout`，列表类页面用 `TablePageLayout`（都在 `components/layout/`）。
- **接口调用**：放在 `api/` 下按模块划分的文件中，统一使用 `api/client.ts` 的实例（基础路径 `/api/v1`）。
- **提示与加载**：用 `useAppStore()` 的 `showSuccess`、`showError`、`showInfo`、`showWarning` 弹出提示，用 `withLoading`、`withLoadingAndError` 包装异步操作。
- **权限判断**：`useAuthStore()` 的 `canAccessAdmin` 对管理员和运营人员都为真；`isAdmin` 只对超级管理员为真。

## 路由与权限

- 路由 meta 字段以 `router/meta.d.ts` 为准。`requiresAuth` 默认为 `true`。
- 每个 `requiresAdmin` 路由都必须声明 `meta.permission`，取值为后台模块 ID 或 `'super_only'`。`router/__tests__/adminPermissionMeta.spec.ts` 会检查，漏写时测试失败。
- 页面标题与描述用 `titleKey`、`descriptionKey` 引用 i18n 键，不要写死文案。
- 依赖功能开关的页面用对应的 meta 字段（`requiresPayment`、`requiresRiskControl`、`requiresSupportTickets`、`requiresSubscription` 等）声明，导航守卫会按公开设置拦截。
- 侧栏菜单在 `components/layout/AppSidebar.vue` 中维护，权限、功能开关和简洁模式的过滤也只在这里做一次。

## 国际化

- 文案放在 `i18n/locales/zh/` 和 `i18n/locales/en/`，两边的键必须完全一致，每个叶子都不能为空。
- `i18n/__tests__/localeKeyCompleteness.spec.ts` 负责检查，`pnpm run build` 的第一步就是运行它（`check:i18n`），键不一致时构建失败。

## 性能约定

### 表格

- **不要在 `<table>`、`<tbody>` 及其祖先元素上用 Tailwind 的 `divide-*` 或 `space-*`。**
  - 这两类工具类会编译成 `X > :not([hidden]) ~ :not([hidden])` 这样的兄弟选择器。
  - 虚拟滚动每插入一行，Chromium 都要为整个 `tbody` 重新计算样式，账号页的滚动帧率会掉到个位数。
  - 行分隔线改用 `table-row-border`，样式定义在 `DataTable.vue` 中。
- **桌面表格每行由 `DataTableRow.ts` 单独渲染。**
  - 滚动时只有新进入窗口的行执行单元格插槽，已有的行直接跳过。
  - 插槽读到的响应式数据由这一行自己追踪，数据变化时只更新这一行。
- **虚拟滚动测量行高只读 ResizeObserver 给出的尺寸**（`measureRowFromObserver`），不读 `offsetHeight`，避免强制同步布局。

### 弹窗与按需加载

- **不常用的大弹窗用 `defineLazyComponent` 加 `useLazyMount`**（`composables/useLazyMount.ts`）。
  - 弹窗第一次打开时才下载、才挂载，之后一直保留。
  - 账号页的新建、编辑弹窗会在页面空闲时用 `prefetchWhenIdle` 预取。
- **按需加载的弹窗总是先以关闭状态挂载，下一帧再打开。** `defineLazyComponent` 在内层组件第一次渲染时把打开属性传为 `false`，挂载完成后再传真实值。
  - 原因：这些弹窗大多只在 `show` 从 `false` 变为 `true` 时加载数据（watch 不带 `immediate`）。如果一挂载就是打开状态，第一次打开会是空的，比如测试连接没有模型、账号统计没有数据。
  - 打开属性不叫 `show` 时，用第二个参数指定，例如 `defineLazyComponent(loader, 'visible')`。
  - 给弹窗写测试时，要覆盖“挂载时就是打开状态”的情况，不能只测先关后开。
- **合规确认弹窗（`AdminComplianceDialog`）保持静态引入**，不做按需加载。
- **部署新版本后，旧页面可能加载不到已被删除的分块文件。**
  - 这时 `utils/chunkLoadError.ts` 会自动刷新一次页面；路由跳转失败时直接打开目标页。
  - 10 秒内不会重复刷新。
  - 接入点有三处：`router.onError`、`vite:preloadError` 和 `defineLazyComponent`。
- **`vite.config.ts` 把以下依赖拆成独立分块，首屏不下载**：支付 SDK、二维码、拖拽排序、新手引导、Markdown、虚拟滚动、图表。新增体积较大、只在少数页面使用的依赖时，按同样方式拆分。

### 毛玻璃与重绘

- **大面积区域不用 `backdrop-filter`。**
  - 包括页面顶栏、表头、弹窗遮罩、公告和设置页标签栏。这些区域改用 95% 不透明的底色，看上去几乎一样。
  - 原因：遮罩模糊整页背景时，弹窗内滚动只有 30 多帧。
- **不用 `transition-all`。**
  - 按钮、输入框、卡片、标签只过渡实际会变化的属性。
  - 卡片悬停的阴影画在 `::after` 上，只过渡它的透明度（见 `styles/motion.css`）。
  - 仓库里仍有部分组件在用 `transition-all`，修改这些组件时可以按本约定替换。
- **Tailwind 开启了 `hoverOnlyWhenSupported`**，触屏上点击后不会残留 hover 样式。

### 数据请求

- **页面在后台标签页时，轮询跳过这一轮**，回到前台后下一轮照常执行。目前适用于 `useAutoRefresh`、订阅进度、渠道状态、风控状态、运维看板倒计时。新增轮询时按同样方式处理。
- **`adminSettings.fetch()` 的并发调用共用同一个请求。** 请求进行中如果有强制刷新，会在当前请求结束后再取一次。
- **`utils/format.ts` 缓存了 `Intl.DateTimeFormat` 和 `Intl.NumberFormat` 实例。** 不要在单元格格式化函数里每次新建。

## 动效系统

动效的时长统一定义在 `styles/motion.css`：

| 变量 | 值 | 用途 |
|---|---|---|
| `--motion-fast` | 140ms | 悬停、按压、开关 |
| `--motion-base` | 220ms | 遮罩、下拉、选中指示 |
| `--motion-slow` | 420ms | 弹窗面板、区块入场 |
| `--motion-exit` | 160ms | 退场 |
| `--motion-stagger` | 45ms | 同组错峰，最多 6 个 |

### 规则

- **只对 `transform` 和 `opacity` 做动画**，不要对宽高、位置、外边距做动画。
- **主要内容的入场不从透明度 0 开始**，当前从 0.4 起步，第一帧就看得清。
  - 从 0 淡入时，Chromium 要等动画结束才记录 LCP（最大内容绘制），实测仪表盘冷启动会因此晚约 500ms。
- **退场比进场快。** 同一组错峰的总时长不超过 270ms。
- **除表格行、轮询刷新的数字和加载转圈外，不做持续动画。** 唯一例外是在线客服气泡的外圈光环，见下文。

### 已有的动效

- **弹窗进出场**（`style.css` 的 `.modal-*`）
  - 面板从下方 12px、缩放 0.97 处浮入。
  - 遮罩的时长不能短于面板，否则 Vue 会提前结束过渡，截断面板动画。
  - `BaseDialog` 的 `<Transition name="modal">` 带 `appear`，按需挂载的弹窗第一次打开时也有进场动画。不要去掉这个属性。
- **页面区块入场**（`usePageEntrance`）
  - 只作用于页面挂载时已经存在的前 6 个区块：自下方 8px 上移，同时由淡转实，依次错开 45ms。
- **数据加载完成后插入的区块**用 `.motion-enter`，通过 `--enter-index` 控制错峰。
  - 只用在插入一次后不会反复挂载的内容上，例如仪表盘首次加载完成后的统计卡片。
  - 用户仪表盘刷新时不再整页显示加载转圈，现有内容保留，不会重播入场。
- **侧栏当前项指示条**（`useSidebarActiveIndicator`）
  - 切换页面时，指示条从上一个位置滑到新位置。
  - 布局随路由重建，所以上一个位置保存在模块级变量里。
- **仪表盘核心数字**（`AnimatedNumber.vue`）
  - 首次显示时从 0 滚动到目标值，用时 640ms；之后数值变化直接显示。
- **状态切换的小动效**（数值刷新、区块展开）用 GSAP，通过 `composables/useStateMotion.ts` 接入。
  - 它用 `gsap.matchMedia()` 只在系统未要求减少动画时启用，并检查 `isReducedMotion()`，组件卸载时统一还原。
  - 直接使用 GSAP 的组件也要在播放前检查 `isReducedMotion()`，并把动画放进 `gsap.context()` 以便清理。
- **在线客服气泡外圈光环**（`features/support-widget/SupportBubble.vue`）
  - 气泡收起时，一段渐亮的弧绕按钮旋转，3.2 秒一圈。
  - 只旋转 transform，由合成层完成，不逐帧重绘；面板打开时不显示。

### 减少动画

两条途径都会关闭动画和过渡，但加载转圈 `.animate-spin` 保留：

1. **系统设置开启“减少动画”**（`prefers-reduced-motion: reduce`）。
2. **用户在右上角头像菜单中打开“减少动画”。**
   - 选择保存在浏览器本地存储的 `motion_preference` 中，并写到 `<html data-motion="reduced">`。
   - 系统已开启时，这个开关显示为开且不能修改。

脚本中的动画要自己检查，使用 `isReducedMotion()` 或 `useMotionPreference()`（`composables/useMotionPreference.ts`）。`AnimatedNumber`、`usePageEntrance` 和 `useStateMotion` 已经处理。

循环动画也要自己处理：全局样式只是把动画缩短成 0.01ms 播放一次，循环元素会停在起始帧。因此客服气泡的光环在开启“减少动画”时直接不渲染。

## 通用交互

- **命令面板**：在任意后台页面按 `Ctrl+K`（macOS 为 `⌘K`），或点击顶栏的搜索按钮。
  - 输入页面名称，用方向键或 `Tab` 选择，回车跳转，`Esc` 关闭。
  - 页面清单直接取侧栏当前可见的菜单，新增页面只需在侧栏登记。
  - 其他弹窗打开时，快捷键不生效。
  - 代码在 `components/layout/CommandPalette.vue` 和 `composables/useCommandPalette.ts`。
- **标签栏键盘操作**：在容器上加 `v-roving-tabs`（`utils/rovingTabs.ts`）后：
  - `←`/`→` 切换到相邻标签并选中；
  - `Home`/`End` 跳到第一个或最后一个标签；
  - 只有选中的标签留在 Tab 顺序中。

  新写的标签栏请复用这个指令。
- **手机端表格排序**：`DataTable` 在手机卡片视图上方显示排序栏。
  - 字段来自 `sortable` 列，右侧按钮切换升序或降序。
  - 行为与点击桌面表头一致，包括服务端排序。
- **账号“可调度”开关**：点击后立即翻转并提交请求；失败时恢复原状态并提示。
  - 请求期间刷新回来的列表沿用正在提交的值，开关不会来回跳。
  - 批量操作仍以服务端返回的结果为准。

## 验证

常规检查：

```bash
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run test:run
pnpm --dir frontend run build
```

涉及表格、弹窗、动效或大依赖的改动，建议对比改动前后的生产构建（`pnpm run build` 后由内嵌前端的后端提供页面）。用 Chrome DevTools 的 Performance 面板，在 CPU 降速 4 倍的条件下测量：冷启动、路由切换、账号页打开与滚动、弹窗打开与滚动、设置页输入。
