# Application Repository UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 收敛应用仓库页面的默认视觉层级，同时保留现有同步、诊断和凭据能力。

**Architecture:** 仅调整 Console 的页面组合和信息展示。仓库列表负责摘要与高频入口，现有 Repo 详情页承载同步诊断和低频操作；后端 API 和同步 Controller 不变。

**Tech Stack:** React、TypeScript、@kubed/components、React Router、现有 openpitrix store、Playwright CLI。

**Spec:** `kubesphere/docs/designs/2026-09-28-application-repository-ui-design.md`

## Global Constraints

- 前后端功能分支使用同名主题：`feature/application-repository-ui`。
- 基于 `release-4.1.5` 开发，不合并官方 `release-4.1`。
- 不新增后端同步接口，不修改 HTTPS/OCI 同步语义。
- 不显示、记录或回传 Secret 内容、密码和 Token。
- 立即同步保持异步；OCI 全量校验只对 OCI 仓库展示。

### Task 1: 建立前端功能分支并确认基线

**Files:**
- Modify: none
- Test: existing Console repository page and current TypeScript/lint commands

- [ ] 从 `release-4.1.5` 创建 `feature/application-repository-ui`。
- [ ] 在浏览器确认应用仓库列表、详情、添加表单和行菜单当前行为。
- [ ] 记录基线截图到 `output/playwright/`，覆盖桌面 1280px 和小屏视口。
- [ ] 运行现有 Console 相关 lint/type/test 命令，确认基线问题与本次变更区分开。

### Task 2: 收敛仓库列表页面层级

**Files:**
- Modify: `console/packages/console/src/pages/workspaces/containers/Repos/index.tsx` 或其实际复用的 shared RepoManage 页面
- Modify: related repository page styles/components discovered during Task 1
- Test: repository page component tests

- [ ] 先添加列表结构测试，确认标题、搜索、刷新、添加和列表仍存在。
- [ ] 将帮助内容改为可折叠单行提示，避免默认占据大块首屏空间。
- [ ] 保留添加为页面唯一主按钮，刷新保留为工具栏操作。
- [ ] 将设置和批量低频入口收纳，不删除功能。
- [ ] 将列表行压缩为名称、状态摘要、URL、类型和更多菜单。
- [ ] 为 URL、状态摘要和长错误文本添加安全换行/截断策略。
- [ ] 运行组件测试和格式化检查。

### Task 3: 统一行菜单与详情入口

**Files:**
- Modify: `console/packages/console/src/pages/workspaces/containers/Repos/RepoDetail/index.tsx`
- Modify: repository row action/menu component identified in Task 1
- Test: row action and detail page tests

- [ ] 添加测试覆盖：编辑、删除、查看详情、立即同步的入口仍可访问。
- [ ] 在行菜单中放入立即同步、编辑、删除和详情入口。
- [ ] 仅对 OCI 仓库显示全量校验，并复用现有 `mode=full` 请求。
- [ ] 详情页使用现有同步诊断和 Events 数据，不复制后端请求逻辑。
- [ ] 对异步同步显示排队/同步中/成功/失败状态，防止重复触发。
- [ ] 运行相关测试和 TypeScript 检查。

### Task 4: 响应式和可访问性回归

**Files:**
- Modify: repository page styles and shared action components
- Test: `output/playwright/` browser regression captures

- [ ] 在 1280px、1024px 和移动视口打开列表页、详情页、添加弹窗。
- [ ] 检查无横向滚动，URL 可换行，主要操作可见或在明确菜单中。
- [ ] 检查图标按钮有可访问名称，键盘可以打开菜单和关闭详情抽屉。
- [ ] 检查加载、成功、失败状态有文本反馈，不能只依赖颜色。
- [ ] 运行 Impeccable detector 和 Console lint/type/test。

### Task 5: 测试环境验证与交付

**Files:**
- Modify: `kubesphere/docs/PLAN.md` after acceptance
- Modify: design/plan records if acceptance changes scope

- [ ] 提交前后端同名分支的 Console 改动，提交信息使用中文。
- [ ] 触发 Console Action 构建测试镜像并部署测试环境。
- [ ] 用 Playwright 验证仓库列表、添加、编辑、立即同步、详情和 OCI 全量校验入口。
- [ ] 确认 HTTPS 仓库不显示 OCI 全量校验，凭据不出现在页面、URL、Status 或日志。
- [ ] 通过验收后更新 `docs/PLAN.md`，再决定是否合并回 `release-4.1.5`。
