# 前端交互与动效约定

本文说明管理端与用户端前端的性能约定、动效系统和通用交互，是后续改界面时的对照清单。

适用版本：0.4.1 之后的前端（任务 `20261019-frontend-ux`）。代码以 `frontend/src` 为准。

## 一、性能约定

这些约定都来自实测时找到的卡顿原因。改动相关代码前先看这里。

### 表格

- **`<table>`、`<tbody>` 及其祖先元素上不要用 Tailwind 的 `divide-*` 或 `space-*`。**
  - 这两类工具类编译成 `X > :not([hidden]) ~ :not([hidden])` 这样的兄弟选择器。
  - 虚拟滚动每插入一行，Chromium 都会让整个 `tbody` 重新计算样式。账号页滚动帧率因此掉到个位数。
  - 行分隔线改用 `table-row-border`，样式写在 `DataTable.vue` 里。
- **桌面表格每行由 `DataTableRow.ts` 单独渲染。**
  - 滚动时只有新进入窗口的行会执行单元格插槽，已有的行直接跳过。
  - 插槽里读到的响应式数据由这一行自己追踪，数据变化时只更新这一行。
- **虚拟滚动测量行高只读 ResizeObserver 给出的尺寸**（`measureRowFromObserver`），不读 `offsetHeight`，避免强制同步布局。

### 弹窗与按需加载

- **不常用的大弹窗用 `defineLazyComponent` 加 `useLazyMount`**（见 `composables/useLazyMount.ts`）。
  - 弹窗第一次打开时才下载、才挂载，之后一直保留。
  - 账号页的新建、编辑弹窗会在页面空闲时用 `prefetchWhenIdle` 预取。
  - **弹窗总是先以关闭状态挂载，下一帧再打开。** `defineLazyComponent` 在内层组件第一次渲染时把 `show` 传成 false，挂载完成后再传真实值。
    - 原因：这些弹窗大多只在 `show` 从 false 变为 true 时加载数据（watch 不带 `immediate`）。如果一挂载就是打开状态，第一次打开就是空的，关掉再开才正常。
    - 0.4.2、0.4.3 有这个问题：测试连接没有模型、临时不可调度原因为空、账号统计没有数据。
    - 打开状态的属性名不是 `show` 时，用 `defineLazyComponent(loader, 'visible')` 这样的第二个参数指定。
    - 给弹窗写测试时，要覆盖“挂载时就是打开状态”的情况，不能只测先关后开。
- **合规确认弹窗（AdminComplianceDialog）保持静态引入，不做按需加载。**
- **部署新版本后，旧页面可能加载不到已被删除的分块文件。**
  - 这时 `utils/chunkLoadError.ts` 会自动刷新一次，路由跳转失败时直接打开目标页。
  - 10 秒内不会重复刷新。
  - 接入点有三处：`router.onError`、`vite:preloadError` 和 `defineLazyComponent`。
- **`vite.config.ts` 把以下依赖拆成独立分块，首屏不再下载它们：**
  - 支付 SDK
  - 二维码
  - 拖拽排序
  - 新手引导
  - Markdown
  - 虚拟滚动
  - 图表

### 毛玻璃与重绘

- **大面积区域不用 `backdrop-filter`。**
  - 涉及的区域包括：页面顶栏、表头、弹窗遮罩、公告和设置页标签栏。
  - 这些区域改成 95% 不透明的底色，看上去几乎一样。
  - 原因：遮罩模糊整页背景时，弹窗内滚动只有 30 多帧。
- **不用 `transition-all`。**
  - 按钮、输入框、卡片、标签都只过渡实际会变的属性。
  - 卡片悬停的阴影画在 `::after` 上，只过渡它的透明度（见 `styles/motion.css`）。
- **Tailwind 开启了 `hoverOnlyWhenSupported`。** 触屏上不会出现点完后 hover 样式残留。

### 数据请求

- **页面在后台标签页时，以下轮询会跳过这一轮：**
  - `useAutoRefresh`
  - 订阅进度
  - 渠道状态
  - 风控状态
  - 运维看板倒计时

  回到前台后，下一轮照常执行。
- **`adminSettings.fetch()` 的并发调用共用同一个请求。** 请求进行中如果有强制刷新，会在当前请求结束后再取一次。
- **`utils/format.ts` 缓存 `Intl.DateTimeFormat` 和 `Intl.NumberFormat` 实例。** 不要在单元格格式化里每次都新建。

## 二、动效系统

动效的时长和缓动统一定义在 `styles/motion.css`：

| 变量 | 值 | 用途 |
|---|---|---|
| `--motion-fast` | 140ms | 悬停、按压、开关 |
| `--motion-base` | 220ms | 遮罩、下拉、选中指示 |
| `--motion-slow` | 420ms | 弹窗面板、区块入场 |
| `--motion-exit` | 160ms | 退场 |
| `--motion-stagger` | 45ms | 同组错峰，最多 6 个 |

### 规则

- **只对 `transform` 和 `opacity` 做动画。** 不要对宽高、位置、外边距做动画。
- **主要内容的入场不要从透明度 0 开始。** 当前从 0.4 起步，内容第一帧就看得清。
  - 从 0 淡入时，Chromium 要等动画结束才记录 LCP（最大内容绘制）。
  - 实测仪表盘冷启动因此晚了约 500ms。
- **退场比进场快。** 同一组错峰的总时长不超过 270ms。
- **表格行、轮询刷新的数字、加载转圈以外的持续动画，一律不做。**

### 已有的动效

- **弹窗进出场**（`style.css` 的 `.modal-*`）
  - 面板从下方 12px、缩放 0.97 处浮入。
  - 遮罩的时长不短于面板，否则 Vue 会提前结束过渡，截断面板动画。
  - `BaseDialog` 的 `<Transition>` 加了 `appear`，按需挂载的弹窗第一次打开时也有进场动画。
- **页面区块入场**（`usePageEntrance`）
  - 只作用于页面挂载时已经存在的前 6 个区块：自下方 8px 上移，同时由淡转实，依次错开 45ms。
- **数据加载完成后插入的区块**用 `.motion-enter`，通过 `--enter-index` 控制错峰。
  - 只用在插入一次后不会反复挂载的内容上，例如仪表盘首次加载完成后的统计卡片。
  - 用户仪表盘刷新时不再整页显示加载转圈，现有内容保留，不会重播入场。
- **侧栏当前项指示条**（`useSidebarActiveIndicator`）
  - 切换页面时，指示条从上一个位置滑到新位置。
  - 布局随路由重建，所以上一个位置保存在模块级变量里。
- **仪表盘核心数字**（`AnimatedNumber.vue`）
  - 首次显示时从 0 滚动到目标值，用时 640ms。
  - 之后数值变化直接显示，不再滚动。

### 减少动画

两条途径都会关闭动画和过渡，但加载转圈 `.animate-spin` 保留：

1. **系统设置开启“减少动画”**（`prefers-reduced-motion: reduce`）。
2. **用户在右上角头像菜单里打开“减少动画”。**
   - 选择保存在浏览器本地（`motion_preference`），并写到 `<html data-motion="reduced">`。
   - 系统已经开启时，这个开关显示为开且不可改。

脚本里的动画要自己检查：用 `isReducedMotion()` 或 `useMotionPreference()`。`AnimatedNumber` 和 `usePageEntrance` 已经处理了。

## 三、通用交互

- **命令面板**：在任意后台页面按 `Ctrl+K`（macOS 为 `⌘K`），或点顶栏的搜索按钮。
  - 输入页面名称，用方向键或 `Tab` 选择，回车跳转，`Esc` 关闭。
  - 页面清单直接取侧栏当前可见的菜单，权限、功能开关、简洁模式只在侧栏维护一处。
  - 其他弹窗打开时，快捷键不生效。
  - 相关代码：`components/layout/CommandPalette.vue`、`composables/useCommandPalette.ts`。
- **标签栏键盘操作**：容器上加 `v-roving-tabs`（`utils/rovingTabs.ts`）后：
  - `←`/`→` 切换到相邻标签并选中；
  - `Home`/`End` 跳到第一个或最后一个标签；
  - 只有选中的标签留在 Tab 顺序里。

  已接入的页面：渠道状态、渠道监控、份额调度、提示词审计及其详情、监控模板。设置页标签栏原本就有同样的键盘操作。
- **手机端表格排序**：`DataTable` 在手机卡片视图上方显示排序栏。
  - 字段来自 `sortable` 列，右侧按钮切换升序或降序。
  - 排序行为与点击桌面表头一致，包括服务端排序。
- **账号“可调度”开关**：点击后立即翻转，同时提交请求；失败时恢复原状态并提示。
  - 请求期间返回的列表，沿用正在提交的值，开关不会来回跳。
  - 批量操作仍以服务端返回的结果为准。

## 四、上游同步注意

`frontend/**` 在 `tools/upstream-sync/policy.json` 中归为 `xy_owned`，`BaseDialog.vue` 归为 `manual_merge`。同步上游时需要注意以下几点：

- **`BaseDialog.vue` 只在 `<Transition name="modal">` 上加了 `appear`。** 合并上游版本时保留这个属性。缺了它，按需挂载的弹窗第一次打开时没有进场动画。
- **上游 `DataTable.vue` 如果重新加回 `divide-y`，或改回内联行模板：**
  - 按本文第一节的约定处理；
  - 再跑 `DataTable.spec.ts` 和账号页测试确认。
- **上游新增的大弹窗**，按需改成 `defineLazyComponent` 加 `useLazyMount`。
- **上游样式里新出现的 `backdrop-blur` 或 `transition-all`**，按第一节的约定替换。

## 五、验证

**常规检查：**

```bash
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run test:run
pnpm --dir frontend run build
```

**性能对比：** 用工作机上的模拟数据基准脚本（不在仓库内），在本地服务上对比改动前后的构建产物。

- 测试条件：CPU 降速 4 倍。
- 测量项目：冷启动、路由切换、账号页打开和滚动、弹窗打开和滚动、设置页输入。

每次结论都记录在 `docs/PROJECT_MEMORY.md` 的操作日志里。
