# 实施与验收

- [x] 迁移 287 与 checksum；`support_chat_enabled` 设置、公开有效值与 `support_ticket_config` 新字段（欢迎语、图片保留天数）。
- [x] 聊天 service/repository：`lockSupportChatRow` 顺序约束、等待队列、读游标、幂等、限流与每日上限、禁言、删除墓碑、整会话删除。
- [x] 图片校验与读取（magic bytes、尺寸与像素上限、410、安全响应头）和清理任务（leader 锁、分批、开关关闭照常、读配置失败跳过）。
- [x] 用户与管理路由、Wire、普通管理员权限目录与计数、审计（敏感读取、空轮询与读游标跳过、消息体不入库）。
- [x] 后端 unit、handler、审计与权限矩阵、本地 PostgreSQL 集成（并发首条、提交顺序、幂等、级联、清理）、api_contract 与 lint。
- [x] 前端气泡（拖动、吸边、记忆位置、显示规则）、面板（消息、工单、访客视图）、图片压缩与显示、聊天 store 与轮询。
- [x] 用户与管理端「客户服务」中心页、收件箱、客服提醒（只轮询 summary、按 id 去重、标题前缀、提示音与桌面通知）、设置卡片与中英文文案。
- [x] 既有工单与侧栏测试同步；新 spec 加入 `FRONTEND_CRITICAL_VITEST`；前端 lint、typecheck、i18n、全量测试与 build。
- [x] 现行文档、OpenSpec、目录、退役文案规则、`xy-support-chat` 具名补丁与 provenance 登记；文档一致性与干净树 audit。
- [x] 收件箱里客服发出的每条消息显示「已读／未读」（按用户读游标），用户侧仍只在自己最后一条消息下显示回执。
- [x] 本地栈端到端：气泡交互、访客登录跳回、压缩、收发与已读、提醒、标记已解决与重开、删除、禁言、只读普通管理员、开关关闭。
