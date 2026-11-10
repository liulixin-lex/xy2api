<!--
标题使用 Conventional Commits 格式（英文），例如：
  feat(gateway): retry on upstream 529 before first output
  fix(billing): count cache writes for Anthropic usage
合并时 PR 标题会成为 main 上的提交标题。详见 CONTRIBUTING.md。
-->

## 改动内容

<!-- 做了什么，为什么。关联的 Issue 写 "Closes #123"。 -->

## 对用户的影响

<!-- 行为、默认值、配置项、接口或界面有什么变化；没有就写“无”。不兼容的变化请明确说明。 -->

## 测试

<!-- 如何验证的：新增或修改的测试、手动验证步骤。 -->

## 检查清单

- [ ] PR 标题符合 Conventional Commits
- [ ] 已添加或更新测试
- [ ] 用户可见的变化已写入 `CHANGELOG.md` 的 `[Unreleased]` 一节
- [ ] 相关文档已更新（`docs/`、配置示例）
- [ ] 包含数据库迁移时，迁移只追加、不修改已发布的迁移
