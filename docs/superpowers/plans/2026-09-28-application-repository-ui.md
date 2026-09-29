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
- 本期不新增后端同步接口、CRD 字段、WebSocket、SSE 或 Redis；版本自动刷新通过现有版本查询缓存失效完成。

## 优先级

- **P0（本轮必须完成）**：保留核心入口和现有同步语义；收敛标题、帮助提示、工具栏和列表默认层级；确保添加、刷新、行菜单和异步同步不回归。
- **P1（P0 后完成）**：详情诊断入口、同步统计展示、响应式布局和可访问性回归。
- **P2（后续优化）**：同步完成后，部署选择页自动刷新应用版本列表；不新增后端同步机制。

### Task 1（P0）: 建立前端功能分支并确认基线

**Files:**
- Modify: none
- Test: existing Console repository page and current TypeScript/lint commands

- [x] 从 `release-4.1.5` 创建 `feature/application-repository-ui`。
- [x] 在浏览器确认应用仓库列表、详情、添加表单和行菜单当前行为。
- [x] 记录基线截图到 `output/playwright/`，覆盖桌面 1280px、1024px 和小屏视口。
- [x] 运行现有 Console 相关 lint/type/test 命令，确认基线问题与本次变更区分开；全项目 `tsc` 的既有 `type-fest`/TypeScript 版本不兼容已单独记录。
- [x] 对照设计文档复核后端 Repo 列表、详情、Events、手动同步和全量校验 API；确认本期不需要后端改动。

### Task 2（P0）: 收敛仓库列表页面层级

**Files:**
- Modify: `console/packages/console/src/pages/workspaces/containers/Repos/index.tsx` 或其实际复用的 shared RepoManage 页面
- Modify: related repository page styles/components discovered during Task 1
- Test: repository page component tests

- [x] 先添加列表动作可见性测试，确认核心行菜单入口不回归。
- [x] 将帮助内容改为可折叠单行提示，避免默认占据大块首屏空间。
- [x] 保留添加为页面唯一主按钮，刷新保留为工具栏操作。
- [x] 将设置和批量低频入口收纳，不删除功能。
- [x] 将列表行压缩为名称、状态摘要、URL、类型和更多菜单。
- [x] 为 URL、状态摘要和长错误文本添加安全换行/截断策略。
- [x] 运行组件测试、ESLint 和格式化检查。

### Task 3（P1）: 统一行菜单与详情入口

**Files:**
- Modify: `console/packages/console/src/pages/workspaces/containers/Repos/RepoDetail/index.tsx`
- Modify: repository row action/menu component identified in Task 1
- Test: row action and detail page tests

- [x] 添加测试覆盖：编辑、删除、查看详情、立即同步的入口仍可访问。
- [x] 行菜单保留立即同步、编辑、删除；名称链接作为详情入口，不增加重复菜单项。
- [x] 仅对 OCI 仓库显示全量校验，并复用现有 `mode=full` 请求。
- [x] 详情页使用现有同步诊断和 Events 数据，不复制后端请求逻辑。
- [x] 对异步同步禁用重复触发，并在同步中每 3 秒刷新详情状态。
- [x] 不新增后端同步接口；应用版本自动刷新仍留在 P2，以独立查询失效实现。
- [x] 运行相关单元测试、ESLint、Prettier 和 TSX 语法编译；全项目 `tsc` 仍被已有 `type-fest` 与 TypeScript 版本不兼容阻断。

### Task 4（P1）: 响应式和可访问性回归

**Files:**
- Modify: repository page styles and shared action components
- Test: `output/playwright/` browser regression captures

- [x] 在 1280px、1024px 和移动视口打开列表页、详情页、添加弹窗。
- [x] URL 和同步摘要已支持安全换行；主要操作仍保留在工具栏或详情按钮中。
- [x] 检查图标按钮有可访问名称，键盘可以关闭添加弹窗；列表菜单通过可访问按钮名称可操作。
- [x] 加载、成功、失败状态已有文本反馈，不能只依赖颜色。
- [x] Impeccable detector 已扫描本期变更页面且无机械问题；Console 单元测试、ESLint 和格式化检查已通过，浏览器与全项目类型检查留待测试环境。

### Task 5（P2）: 同步完成后的应用版本列表刷新

**Files:**
- Modify: existing application version query/cache consumer identified from the deployment page
- Test: application version query invalidation/refresh test

- [x] 确认部署选择页 `AppVersionSelector` 使用 `useAppVersionList`，应用通过 `application.kubesphere.io/repo-name` 关联仓库。
- [x] 添加测试：仓库从同步中进入成功终态后，仅匹配仓库的版本选择器重新获取版本。
- [x] 通过浏览器自定义事件携带仓库名，只刷新受影响应用，不触发全局查询失效。
- [x] HTTPS 和 OCI 使用同一刷新机制；失败终态不刷新，OCI 全量校验成功后同样触发一次刷新。

### Task 6（P1）: 测试环境验证与交付

**Files:**
- Modify: `kubesphere/docs/PLAN.md` after acceptance
- Modify: design/plan records if acceptance changes scope

- [x] 提交前后端同名分支的 Console 改动，提交信息使用中文。
- [x] 触发 Console Action 构建测试镜像并部署测试环境。
- [x] 用 Playwright 验证仓库列表、添加、编辑、立即同步、详情和 OCI 全量校验入口。
- [x] 确认 HTTPS 仓库不显示 OCI 全量校验，凭据不出现在页面、URL、Status 或日志。
- [x] 通过验收后更新 `docs/PLAN.md`，再决定是否合并回 `release-4.1.5`。
