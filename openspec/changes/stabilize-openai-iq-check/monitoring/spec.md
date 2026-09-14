# 监测改进验收规格

以下是本轮实现的验收条件，实际执行结果以tasks.md及固定交付目录中的monitor-impl-*日志为准，不以规格文字代替测试证据。当前仍以candy-v2/答案21为唯一评分标准。

### Requirement: 固定评分标准

#### Scenario: 简洁正确答案
- GIVEN 完整终态为JSON `{"answer":21}`、纯数字21或明确结论为21的解释
- THEN 判smart；不因格式差异改变答案结果。

#### Scenario: 明确错误答案
- GIVEN 完整终态明确答案29
- THEN 单次判degraded并限制新请求分配，不通过重试求得smart。

#### Scenario: 歧义和故障
- GIVEN 冲突/重复JSON字段、无明确答案、拒答、未完成流、超时
- THEN unknown并解除IQ自身限制，保留原有人工/额度限制。

### Requirement: 请求与端点匹配

#### Scenario: 普通API key
- GIVEN 普通API key端点
- THEN 不默认加Codex专用身份头，使用真实应用标识及端点规定字段。

#### Scenario: OAuth协议能力不成立
- GIVEN 所选模型/参数被该OAuth端点明确拒绝
- THEN unknown并暂停相应配置，不换账号、路径或降低深度重新回答。

#### Scenario: 独立会话与无答案泄漏
- WHEN 连续执行两轮
- THEN 单题、无previous_response_id/旧会话、store:false、同一评分版本，提示及Schema不包含标准答案。

#### Scenario: 推理预算不足
- GIVEN max_output_tokens耗尽或总超时到期
- THEN unknown，保存有界原因；短可见答案不能成为压低内部推理预算的依据。

### Requirement: 低负载排期

#### Scenario: 旧账号固定模式
- GIVEN 原有间隔15或1分钟，客户端不传新字段
- THEN 保留间隔及fixed模式，不偷偷开启adaptive或覆盖配置。

#### Scenario: 节约模式
- GIVEN I=15、上限60、连续3/6个smart
- THEN 后续间隔分别30/60分钟；不将连续通过门槛用于延迟本轮评分。

#### Scenario: 节约模式答错
- GIVEN 已进入60分钟间隔，下一轮明确29
- THEN 立即degraded、连续smart归零，恢复基础间隔排期。

#### Scenario: 账号忙
- GIVEN 真实业务占满账号名额
- THEN IQ延期，不额外发请求、不抢业务等待队列、不创建新轮记录或覆盖assessment。

#### Scenario: 多实例争抢
- GIVEN 多实例领取相同账号或共享配额
- THEN 原子预算、名额和IQ租约共同防止超额；DB/Redis异常延期，不放宽限制。

#### Scenario: 重复手动运行
- GIVEN 不同管理员连续点击立即检测
- THEN 合并为同一pending/running任务；上轮完成后仍守最小间隔、日预算和上游冷却。

#### Scenario: 开关及配置不能清零用量
- GIVEN 当日已达预算，再关闭/开启或换检测模型
- THEN 已尝试计数与Retry-After不清零；关闭仍立即解除IQ限制。

#### Scenario: 日期切换
- GIVEN UTC跨日预算重置
- THEN 剩余预算日期正确，同时执行最小间隔、上游限制和全局并发，不集中补发积压轮次。

### Requirement: 分类恢复

#### Scenario: 有效Retry-After
- GIVEN 429/503含秒数或HTTP日期，包括超出工作线程最长等待的值
- THEN 后续任务不早于该时点，长延迟采用延期/暂停，不截短或阻塞睡眠。

#### Scenario: 配额与权限错误
- GIVEN 有界JSON识别明确不足额度或权限错误
- THEN unknown并暂停，等待已知恢复事件/人工动作；不得当作普通可立即重试的429。

#### Scenario: 已消费输出后中断
- GIVEN 收到部分SSE后中断
- THEN unknown并按下一周期处理，不自动重放；插件尝试数不明时不能宣称链路仅一次。

#### Scenario: 连续协议故障
- GIVEN 相同配置连续3次协议解析失败
- THEN 暂停并显示最近诊断；不会轮换题干/身份来消除报错。

#### Scenario: 空闲任务进程重启
- GIVEN pending尚未发送，或已发送任务lease到期
- THEN 前者仅恢复排期；后者interrupted并保守记账，旧revision不能回写。

### Requirement: 结果新鲜度与留存

#### Scenario: 队列延期与旧degraded
- GIVEN 旧评分degraded，本轮因本地预算未发出
- THEN execution为deferred，保留旧assessment与IQ限制；不得伪造unknown解除限制。

#### Scenario: 新轮unknown
- GIVEN 原degraded且实际新请求返回unknown
- THEN 按原规则解除IQ限制，状态显示unknown及具体原因，不称已恢复聪明。

#### Scenario: 两轮正文
- WHEN 第三轮真正开始并产生记录
- THEN 同事务删除最旧正文；预算/连续计数只存有界数字，不另建无限对话日志。

#### Scenario: 诊断下载
- GIVEN 管理员下载最近两轮诊断
- THEN 只含有权访问账号的配置、时间、request_id、用量和白名单错误字段；不含凭据、推理或业务正文，不自动发送给第三方。

#### Scenario: 旧客户端与缓存
- GIVEN 旧客户端编辑单个字段或多实例读缓存
- THEN 省略字段保持、状态筛选分页一致、缓存/outbox同步；原人工停用始终有效。

#### Scenario: 模型身份与指标
- GIVEN 上游报告同一模型名但耗时/token发生变化
- THEN 仅记录诊断，不据此推断隐藏路由或蒸馏标签，也不代替糖果答案判分。
