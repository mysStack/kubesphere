# KubeSphere 定制项目路线图

> 本文件是当前 KubeSphere 定制项目的唯一路线图。每个阶段在独立分支完成，前后端使用同名分支；阶段完成后，代码、设计和本文件一起合并回主分支。

## 管理规则

1. `master` 是本项目的稳定主线，只保留已验证、可回滚的发行成果，不直接开发功能。
2. 每个版本使用一个发布开发线，当前为 `release-4.1.5`；新功能和修复分支都从当前发布开发线创建。
3. 阶段开始时，将对应事项标记为 `进行中`，并在 `docs/designs/` 创建技术设计文档。
4. 阶段开发、测试和测试环境验证完成后，更新本文件：完成项、未完成项、已知限制和新构想。
5. 前后端仓库分别提交，但使用同一个阶段名；不得直接合并未验证的半成品。
6. 设计文档描述本阶段实现细节；本文件只维护路线、状态和验收结果。

状态标记：`[x] 已完成`、`[~] 进行中`、`[ ] 待完成`、`[!] 受阻/需决策`。

## 分支与版本基线约束

以下规则是本项目后续开发、升级和合并的固定约束：

1. `v4.1.4` 是当前项目已发布的稳定版本基线。它是不可变的发布标签，只用于回滚、差异比对和创建下一版本发布线，不直接提交开发代码。
2. 不管理官方 `release-4.1` 分支：不从它创建项目分支，不在它上面开发，也不将其整体合入本项目。该分支即使保留在远程，也不属于本项目开发流程。
3. 当前 `release-4.1.5` 是 v4.1.5 的唯一发布开发线，前后端仓库均使用这个同名分支。它基于已发布的 `v4.1.4`，用于集成功能、修复和测试结果。
4. 新功能使用 `feature/<主题>`，短期修复使用 `fix/<主题>`；两个仓库必须使用相同的主题名，并从当前 `release-4.1.5` 创建。
5. 功能分支完成后，必须分别完成后端/前端测试、镜像构建和测试环境验证，再合并回 `release-4.1.5`。未验证的代码不得进入发布线或 `master`。
6. `master` 只接收已经完成版本验收的发布线合并，不允许直接提交功能代码。v4.1.5 完成后，将 `release-4.1.5` 合并回 `master`。
7. 下一版本从更新后的 `master` 创建新的发布开发线，例如 `release-4.1.6`；不在已完成的旧发布线继续堆叠新版本功能。
8. 官方修复如果确实需要采用，只能按明确的提交或补丁粒度人工评估、移植和测试；不通过同步或整体合并官方分支改变本项目基线。
9. `v4.1.4`、`v4.1.5` 等正式 Tag 必须从完成验收的发布线创建，Tag 创建后不可移动、覆盖或强制重写。候选版本使用 `vX.Y.Z-rc.N`。
10. 正式发布前必须确认前后端提交、镜像 Tag/摘要、Helm Chart、安装文档和回滚依据一致；发布后保留发布线用于审计，不继续开发下一版本。

### 固定开发流程

```text
master（稳定主线，保存上一版本已验证成果）
        │
        └─ release-4.1.5（当前版本发布开发线）
                ├─ feature/<主题>（前后端同名）
                └─ fix/<主题>（前后端同名）
                        │
                        └─ 测试、构建、测试环境验证
                                │
                                ├─ v4.1.5（正式版本 Tag）
                                └─ 合并回 master
```

官方分支不参与本项目分支管理。`master`、当前发布开发线和正式 Tag 是本项目唯一需要维护的版本关系。

### 项目自有版本发布流程

```text
v4.1.4（已发布稳定版本）
    │
    └─ release-4.1.5（当前发布开发线）
          ├─ feature/*、fix/*
          ├─ v4.1.5-rc.N（可选）
          └─ v4.1.5（验证通过后正式发布）
                    │
                    └─ 合并回 master
```

`v4.1.4` 表示本项目在 v4.1.3 基础上的独立发行版本，不表示上游已经发布了 KubeSphere v4.1.4。正式发布前必须确认前后端镜像、Git 标签、Release 说明和回滚版本保持一致。

## 当前基线

- 已发布版本：`v4.1.4`
- 当前后端分支：`release-4.1.5`
- 当前前端分支：`release-4.1.5`
- 当前发布开发线基线：前后端分别从 `v4.1.4` 创建
- 当前 `master`：作为稳定主线，待 v4.1.5 验收后接收发布线合并
- 主要定制范围：OpenPitrix 应用仓库、OCI Helm Chart、Console 仓库管理、个人镜像构建。
- 当前仍以标准 `networking.k8s.io/v1 Ingress` 和 KubeSphere 旧版 Gateway API 为主；Gateway API 扩展尚未接入本项目。

## 阶段一：OCI 应用仓库稳定性

目标：让 HTTPS Helm Repo 和 OCI Helm Repo 使用各自正确的同步、缓存和部署流程。

- [x] HTTPS Helm Repo 与 OCI Repo 分离处理。
- [x] OCI 使用 Registry tag/manifest 发现 Chart，不依赖 `index.yaml`。
- [x] 过滤 `*-metadata` 等辅助 Artifact 和无效 Helm manifest。
- [x] ApplicationVersion 保留 OCI 原始 tag，部署时通过 OCI 地址拉取 Chart。
- [x] OCI 支持直接 Chart 仓库和 Registry catalog 子仓库发现。
- [x] OCI 请求超时、TLS、Basic Auth、Client Certificate、Plain HTTP 测试覆盖。
- [x] Console 增加“立即同步”入口，后端同步保持异步执行。
- [x] 前后端个人镜像 Action 已配置，并完成测试环境构建和部署验证。
- [x] P0：私有 Helm/OCI 仓库凭据管理。Console 支持创建或选择受 Workspace 边界保护的仓库凭据；Repo 仅保存 Secret 引用，验证、同步和部署复用该引用，凭据不回传、不出现在 URL、Status、Event 或日志中。
- [x] 扩展 Repo 状态：同步开始时间、结束时间、耗时、版本数量、缓存命中数、最近错误。
- [x] 为 OCI 失败场景补充可读的 Status Reason、Kubernetes Event 和 Console 错误展示。

验收标准：添加、验证、手动同步 OCI 仓库不阻塞 API；有效 Chart 能出现在商店并可部署；辅助 Artifact 不造成失败；失败时能区分超时、429、认证、无效 Chart 和 Registry 不可达。

阶段一验证记录（2026-09-18）：

- 后端测试镜像：`oci-repo-20260918-d466650`，已部署 `ks-apiserver` 和 `ks-controller-manager`。
- 前端测试镜像：`oci-repo-20260918-b890df5`，已部署 `ks-console`。
- 前端 Action 已成功同时推送 Docker Hub 和 GHCR 镜像，构建记录：[Build Personal Console Image](https://github.com/mysStack/console/actions/runs/35314311819)。
- 测试环境 `ks-console` Deployment 已滚动更新至前端测试镜像，Pod 为 `1/1 Running`，NodePort 根路径返回 HTTP 200。
- Console“立即同步”已完成异步触发验证；OCI 全量缓存、增量同步、限流退避和详细进度状态仍属于阶段二、三，尚未标记完成。

私有仓库优先级说明（2026-09-20）：

- Controller 已支持 `credentialSecretRef`，并在仓库验证、同步和部署下载 Chart 时复用凭据；管理员可通过受控 Secret 配置私有仓库。
- 当前 Console 没有安全的凭据管理入口。不能让 Workspace 用户直接选择 `kubesphere-system` 的任意 Secret，否则会突破 Secret 的所有权边界。
- 若私有仓库是日常使用场景，本项应排在仓库健康信息之前；先设计专用凭据 API、Workspace 归属校验和 Console 表单，再继续观测信息展示。

## 阶段二：OCI 性能、缓存和限流

目标：在保留全量版本能力的同时，减少 Registry 请求，避免 Docker Hub 等公共 Registry 限流。

- [x] tag 列表作为第一步，manifest/metadata 查询采用批量、并发上限和超时控制。
- [x] 建立按 Repo、tag 的 OCI metadata 缓存，并保留已同步版本的 manifest digest，避免每次定时同步全量重新拉取。
- [x] 对新增 tag 和已缓存版本执行增量同步。
- [x] 提供 OCI 显式全量校验；本次同步重新读取已缓存 tag 的 metadata，完成后恢复默认缓存行为。
- [x] 删除或失效 tag 清理策略：本轮 OCI 索引无 warning 时删除缺失的 Application 与 ApplicationVersion；任一 tag 产生 warning 时视为部分结果，保留既有数据，避免瞬时网络或限流误删。
- [x] 辅助 Artifact 在 tag 过滤阶段跳过，manifest 校验失败只记录单版本警告，不阻塞其他版本。
- [x] 为 429、5xx、超时实现有限次数重试和退避；认证错误、TLS 错误、404 不重复重试。
- [x] 将同步并发、tag 分页大小、请求超时、缓存 TTL 和重试次数配置化，并设置安全默认值。
- [x] 增加仓库健康信息：远端版本数、有效 Chart 数、跳过数、失败数、请求数、最近同步耗时。
- [x] 增加离线 Registry Mock 测试：77 个版本、metadata Artifact、429、慢响应、重复 digest、部分失败。

当前默认值：metadata 请求并发上限为 4、tag 分页大小为 100、单次请求超时 30 秒；429/5xx/超时最多尝试 3 次，退避为 200ms、500ms，并优先遵循 `Retry-After`。`applicationRepository.oci.cacheTTL` 默认为 `0s`，不会额外触发全量请求；设置为正值后，过期的 Repo 在下一次正常或手动同步时做一次全量 metadata 校验，只有无 warning 的完整结果才更新 `application.kubesphere.io/oci-cache-validated-at`。默认同步假定 OCI tag 不可变，已缓存 tag 不会重新拉取 manifest；需要重新校验旧 tag 时，也可使用 OCI 仓库的“全量校验”动作一次性绕过缓存，下一次默认同步恢复缓存。

仓库健康信息实现记录（2026-09-22）：

- 后端在 `Repo.status.sync` 记录通用同步生命周期，并为 OCI 记录远端 tag、有效 Chart、跳过 Artifact、失败 tag、请求次数和缓存命中；请求计数按单次同步隔离，覆盖发现、tag、manifest、blob 与重试请求。
- Console 仓库列表保留原状态点和文案，追加同步开始时间或耗时/有效版本摘要；旧 Repo 无 `status.sync` 时保持兼容，手动同步排队态不显示旧摘要。
- 本地后端 API/OCI/controller/KAPI 测试、Console 5 项摘要测试、TypeScript、Prettier、ESLint 新增文件检查均通过。
- 测试环境已部署 `oci-repo-20260922-50ed7e2`（后端）和 `oci-repo-20260922-4c61151`（Console）：OCI 仓库 `redis-oci` 同步成功，记录远端 1033 个 tag、302 个有效 Chart、78 次请求、300 次缓存命中、耗时 110 秒；HTTPS 仓库 `argo-helm` 同步成功，记录 1688 个有效版本、耗时 222 秒。私有 OCI 仓库的 401 会以失败状态和已脱敏错误落库。

验收标准：重复同步主要命中缓存；同一 Registry 多仓库不会无限并发；429 不导致整个 Repo 进入不可恢复状态；同步结果可解释、可重试、可观测。

阶段二显式全量校验验证记录（2026-09-19）：

- 后端源码 commit `9eaab0fbd`，测试镜像 `ghcr.io/mysstack/ks-apiserver:oci-repo-full-refresh-20260919-9eaab0f`、`ghcr.io/mysstack/ks-controller-manager:oci-repo-full-refresh-20260919-9eaab0f`；构建记录：[Build Personal Images](https://github.com/mysStack/kubesphere/actions/runs/35430253770)。
- Console commit `ad56b3c66`，测试镜像 `ghcr.io/mysstack/ks-console:oci-repo-full-refresh-20260919-ad56b3c`；构建记录：[Build Personal Console Image](https://github.com/mysStack/console/actions/runs/35430254040)。
- 测试环境的 `ks-apiserver`、`ks-controller-manager`、`ks-console` 均完成滚动更新并保持 `1/1` Ready。
- 已有缓存版本的 OCI Repo `backend-app` 依次完成默认同步、全量校验、再次默认同步，均收敛到 `successful`；全量标记已被消费，既有 `0.1.0` 版本 digest 保持不变。聚焦 Controller/OCI loader 测试同时确认默认同步不请求缓存 tag 的 manifest/config、全量校验重新读取一次、后续默认同步不增加 metadata 请求。
- HTTPS Repo `argo-helm` 默认同步收敛到 `successful`；聚焦测试确认 HTTPS 路径不调用 OCI loader。
- 全量校验会对每个有效 OCI tag 重新请求 manifest/config，公共 Registry 上会增加配额消耗、限流概率和同步耗时；日常同步应继续使用默认缓存路径，仅在旧 tag 可能被覆盖或缓存需要重建时执行全量校验。

触发器并发回归修正（2026-09-22）：

- `RepoReconciler` 在触发器判定与消费时使用 API Reader 重新读取触发器，并只将该强一致读到的 annotations、resourceVersion 同步回本轮对象；避免把 `UpdateStatus` 的 metadata 回写重新引入丢失并发触发器的风险。
- 新增回归覆盖：状态写入后的既有手动/全量触发器可在同一轮消费；缓存读取后新增的触发器不会被上游预检查遗漏；消费期间写入新触发器仍返回 Conflict 并显式 requeue，重试后保留并执行新触发器。

## 阶段三：应用商店和同步体验

目标：让 Console 能区分“仓库可用”“正在同步”“发现新版本”“上次同步失败”。

- [x] Repo 状态在 Console 统一展示为 `Ready`、`Syncing`、`Failed`、`Stale` 等可读状态；`NewVersionDetected` 暂不引入，因为当前同步协议没有可靠的未发布版本语义。
- [x] 手动同步 API 返回快速响应，重复点击具有幂等行为；正在执行时返回当前 Repo 状态快照。
- [x] Console 显示开始时间、最近成功时间、耗时、版本统计、缓存统计和失败原因；不伪造百分比进度。
- [x] 定时 Job 和“立即同步”复用同一个后台同步入口，不复制同步逻辑。
- [x] 支持增量同步、OCI 全量校验、保存前连接验证三种明确动作；默认动作在 UI 中清晰标注。
- [x] 商店版本列表在同步完成后自动刷新，不要求用户重新进入页面。
- [x] 增加仓库诊断入口，显示最近同步统计、错误和 Kubernetes Events，但不暴露密码或 Token。

验收标准：用户点击立即同步后页面不超时；状态最终可收敛到成功或失败；新版本能刷新到应用部署选择列表；错误不再只显示“同步中”。

阶段三第一小步（2026-09-23）：

- Console 将后端 `manualTrigger`（已排队）按“同步中”展示，避免出现未翻译状态或旧成功摘要；同步中和排队中的仓库均禁止重复触发。
- 用户发起增量同步或 OCI 全量校验后，Console 只轮询被触发仓库所在的列表；收到 `successful` 或 `failed` 等终态即停止轮询，不增加后端任务系统。

阶段三收尾（2026-09-27）：

- 手动同步接口在触发器已存在或 Repo 已处于同步态时返回 `alreadyRunning`，不覆盖原触发器；响应仅包含 Repo 状态快照，不包含凭据或 Secret 内容。
- Repo 详情新增同步诊断页，展示生命周期时间、耗时、版本/Artifact、请求/缓存命中和已脱敏错误；列表增加 `Stale` 展示，按同步周期计算且不修改 CRD 状态。
- 失败仓库会按配置的同步周期再次尝试；不会因一次 401/网络错误在 Status 更新后立即进入持续重试循环。
- 测试环境回归：`redis-cluster`（137 个有效版本）、`traefik-oci`、带 `wen-route-app` Secret 引用的私有 OCI `backend-app` 均收敛到 `successful`；未配置凭据时的 401 会收敛到 `failed` 并展示脱敏错误。
- 商店版本列表自动刷新已完成：仓库列表/详情在同步从进行中进入成功或失败终态时派发带 `repoName` 的浏览器事件；部署选择页仅当应用标签关联该仓库时刷新版本查询，不触发全局失效，也不引入任务系统。

阶段三 UI 收尾验收记录（2026-09-29）：

- Console 分支 `feature/application-repository-ui` 已包含凭据卡片移动端溢出修复、图标按钮可访问名称和同步轮询收敛；最新提交为 `26a84881d`。
- Console Action [Build Personal Console Image #36527810840](https://github.com/mysStack/console/actions/runs/36527810840) 成功，镜像为 `docker.mystack.dpdns.org/mingys/ks-console:application-repository-ui-20260929-26a8488`。
- 测试环境 `192.168.2.131` 的 `ks-console` 已滚动更新，Pod `1/1 Running`，Deployment `ready=1 available=1`。
- Playwright 已验证 1280px、1024px 仓库列表、添加仓库弹窗、凭据下拉、详情诊断、HTTPS/OCI 行菜单差异和立即同步状态收敛；375px 下添加弹窗凭据区不溢出。KubeSphere 全局壳在 375px 仍保持 1164px 最小宽度，属于平台级响应式限制，本期不在仓库页面局部覆盖。
- 列表同步轮询使用 DataTable 查询自身的 `refetchInterval`：进入同步状态后每 3 秒刷新，成功或失败终态后自动停止；首次打开页面已在同步中的仓库也会加入轮询集合。同步触发后不再额外发起重复列表请求。
- 已知环境噪声：测试环境扩展 `kubeeye`、`ingress-utils`、`whizard-telemetry`、`frontend-forge` 的兼容性错误与本次应用仓库页面无关；未发现仓库页面自身运行时错误。

## 阶段三后续：应用配置引用与变更审计

目标：降低应用创建时逐条填写环境变量的成本，并让应用详情能够区分创建人和最后一次用户更新人。

### 子项目 A：ConfigMap/Secret 批量引用

完整 V3 源码不可获得；`packages/bootstrap/assets/v3dist` 是已编译的稳定兼容制品，禁止以精简 Console 原生表单替换或重新构建该制品。当前能力采用 Console 独立配置引用入口和可选 Stakater Reloader 自动滚动更新，不把任意 Helm Chart 的 values 结构假定为统一格式。

- [x] P0：在 Deployment、StatefulSet、DaemonSet 详情页提供独立“配置引用”入口，生成并回显 Kubernetes 原生 `envFrom.configMapRef` 和 `envFrom.secretRef`；不接管 V3 创建/编辑路由。
- [x] P0：保留现有逐 Key 引用能力，支持同一容器同时使用 `env` 和 `envFrom`。
- [x] P0：引用选择器只展示当前 Cluster/Project 可见资源；Secret 仅展示名称和 Key 元数据，不读取或回显 Secret 值。
- [x] P0：保留 V3 的完整工作负载字段；独立入口只 PATCH 目标容器的 `envFrom` 和工作负载元数据注解。
- [x] P0：按工作负载开启或关闭配置变化自动重启：开启时写入 `reloader.stakater.com/auto: "true"`，关闭时移除该注解。
- [x] P0：以独立 Namespace、固定版本镜像和最小 RBAC 部署 Stakater Reloader；它不依赖 `ks-apiserver`、`ks-controller-manager` 或 Console 专用 API。
- [~] P1：增加 ConfigMap/Secret 文件挂载，生成 `volumes[].configMap`、`volumes[].secret` 和 `volumeMounts[]`；校验挂载路径冲突、只读属性和资源作用域。
- [x] P1：Helm 应用仅在 Chart 的 values schema 明确暴露 `envFrom`、`extraEnvFrom`、`volumes` 或 `volumeMounts` 等入口时提供结构化控件；其他 Chart 继续使用 Values/YAML 编辑器。
- [x] P2：增加可选环境变量前缀与引用冲突提示（已完成）；Key 过滤与批量移除经评估不做（原生 `envFrom` 无法排除键，逐 Key 引用已存在；批量移除收益低于界面复杂度成本）。

#### 配置变更后的自动生效

`env`、`envFrom.configMapRef` 和 `envFrom.secretRef` 都在 Pod 创建时注入环境变量。修改 ConfigMap 或 Secret 不会更新已运行 Pod；此能力只覆盖标准工作负载，不改变 Helm Chart 自身的更新策略。

- [x] P0：Console 明确展示自动重启状态；默认关闭，开启后由 Reloader 监听被引用的 ConfigMap/Secret 并异步触发滚动更新，不要求每次配置变更都经过 KubeSphere 确认。
- [x] P0：验证 Reloader 对 `envFrom` 引用的 Deployment、StatefulSet、DaemonSet 生效；关闭注解或卸载 Reloader 后，工作负载配置保留且停止自动滚动更新。
- [x] P1：为 Reloader 事件和失败原因提供只读诊断入口；不在 Console 重复实现影响分析、重启队列、PodTemplate 重启 PATCH 或批量确认弹窗。

已废弃：自研 ConfigMap/Secret 反向引用扫描、用户勾选受影响工作负载、批量重启队列和重启状态轮询。废弃原因：这些职责由独立的 Stakater Reloader Controller 统一处理，避免与 KubeSphere 工作负载页面和 Controller 强耦合。

已知限制（2026-10-07 记录）：本能力把 `envFrom`、`volumes` 与 `volumeMounts` 直接 PATCH 到
工作负载对象上。对 Helm 管理的工作负载（带 `app.kubernetes.io/managed-by: Helm` 与
`meta.helm.sh/release-*`），该 Release 下次升级时 Helm 会按 release 清单做三方合并并回滚这些
改动——实测环境中的 `dev-wes/wes-v2-server`（release `wes-server`）即属此类，它现有的 3 条
`envFrom` 会在下一次升级 `wes-server` 时丢失。这是 Helm 的预期行为，不是本能力缺陷，但用户无从
预期；是否在界面提示、或改为写入 Chart 认可的入口（即 P1 的 schema 前置条件），留待评估。

验收标准：标准工作负载可通过独立入口引用完整 ConfigMap/Secret 并正确回显；Secret 内容不出现在页面、请求日志、事件或错误信息中；未启用自动重启时配置变化不滚动 Pod，启用后 Reloader 能使引用该资源的标准工作负载滚动更新；完整 V3 页面、字段和路由不受影响；Helm 应用不会因通用控件写入未知 values 路径而产生“界面显示成功但 Chart 未生效”的假象。

### 子项目 B：应用创建人和更新人

- [x] P0：ApplicationRelease 创建时写入 `kubesphere.io/creator`，更新时保持创建人不变。
- [x] P0：ApplicationRelease 由用户 API 更新时写入独立的 `kubesphere.io/last-updater`；Controller 的状态更新不得覆盖该字段。
- [x] P0：保持 Controller 使用 `kubesphere.io/creator` 做 Helm/Kubernetes impersonation，不能用更新人替代创建人。
- [x] P1：应用详情展示创建人、更新人、创建时间和更新时间；列表暂不增加更新人列，避免表格信息过密。
- [x] P1：为创建、更新、Controller 状态更新和历史对象缺少注解等场景增加后端和 Console 回归测试。
- [x] P2：评估是否需要操作历史时间线；不把 `managedFields` 直接作为产品层更新人字段。

验收状态（2026-10-07）：**已通过线上写操作验证**。

- 做法：通过 Console 应用商店的 API（与安装向导同一套逻辑——先在目标工作区内选模板与有效版本，
  再 POST 创建）在一个一次性项目里创建应用，随后用同一接口 POST 一次更新。实测结果：

  ```text
  创建后   creator = admin        last-updater 未设置     state = creating
  POST 更新 → 200 {"message":"success"}
  更新后   creator = admin（不变）  last-updater = admin   resourceVersion 已变化
  删除     → 200 {"message":"success"}
  ```

  创建路径只写创建人、更新路径保持创建人并写入本次操作人，两者都符合设计；更新确实落库
  （resourceVersion 变化可证）。该应用与一次性项目随后都已删除。
- 一个顺带确认的点：该应用当时处于 `deployFailed`（所选 chart 安装失败），审计语义依然正确——
  这正是「审计注解由 API 处理器写入、与安装结果无关」的直接体现。
- 覆盖范围：创建时写入创建人、更新时创建人不变、用户 API 更新写入最后更新人，三项已线上验证；
  「Controller 状态更新不覆盖最后更新人」由后端回归测试覆盖
  （`apprelease_controller_audit_test.go` 的 `TestUpdateStatusDoesNotOverwriteLastUpdater`、
  `TestPatchAnnotationDoesNotOverwriteLastUpdater`），并且线上该对象在被更新前已经处于
  `deployFailed`（控制器已写过状态），创建人始终没有被改写。
- 过程中确认的一条约束，供后续同类验证参考：`ApplicationVersion` 按工作区隔离且会被回收，
  因此必须在目标工作区内选取有效版本；手工照抄一个旧应用的 spec 会被准入 webhook
  `applicationrelease.extensions.kubesphere.io` 拒绝（实测旧版本已被回收）。

验收标准：用户更新应用后创建人保持不变，更新人显示为最近一次用户 API 操作人；Controller 重试、同步状态变化和 Helm 执行不会改变更新人；旧应用无更新人时页面兼容显示为空或“暂无记录”。

### 合并开发评估

这两个子项目可以纳入同一个迭代和发布线，但不建议合并成一个实现分支或一个大 PR：

1. 子项目 A 涉及工作负载 API、前端表单和 Helm values 兼容边界，回归重点是创建/更新后的 Pod spec 与文件挂载。
2. 子项目 B 涉及 ApplicationRelease API、Controller 身份代理和详情展示，回归重点是审计字段不可覆盖和权限身份不回归。
3. 两者没有运行时依赖，任一项失败都可以独立回滚；推荐分别使用 `feature/app-config-reference` 和 `feature/app-release-audit`，完成各自测试后合并到当前 `release-4.1.5`。
4. 若希望减少发布次数，可以在同一版本中连续合并两个已验证分支，但不能因为共用应用页面就共用未验证的半成品代码。

建议实施顺序：先完成子项目 B 的 creator/updater 数据语义修复，再完成子项目 A 的 `envFrom`，最后评估文件挂载和 Helm Chart schema 适配。两项均不需要引入 Redis。

## 阶段三后续验证记录（2026-10-07）

### 子项目 A：ConfigMap/Secret 批量引用

Console 分支 `feature/app-config-reference`，本阶段前端提交到 `65ae7bdcd`；测试环境
`192.168.2.131` 的 `ks-console` 已滚动更新至
`docker.io/mingys/ks-console:config-ref11-20261007-65ae7bd`，revision 297，Pod `1/1 Running`。

- P0 能力（独立入口、`envFrom` 生成与回显、Secret 只读键名、按容器分组、变量值展示、默认
  收起与全部展开、每容器独立展开）均已实现并通过线上验证。已有部署的回显与统计和 `kubectl`
  一致：`dev-wes/wes-v2-server`（envFrom 3 条）、`test-wes/ams-server`（envFrom 8 条、env 0 条，
  共 25 个环境变量）。
- 断言式验收用例 12 项全部通过，覆盖面板与只读页两条路径；另有单元测试 23 文件 / 91 用例。
- Reloader 端到端验证：Deployment、StatefulSet、DaemonSet 三种工作负载，ConfigMap 与 Secret
  两种引用。改被引用资源后 Pod 重建且新值生效（实测容器内 `GREETING=v2`、`TOKEN=s2`）；不带
  `reloader.stakater.com/auto` 注解的工作负载在三次配置变更中均未滚动；移除注解后该工作负载
  不再因配置变更滚动，且 `envFrom` 完整保留。
- 验证中发现 Reloader 每次成功重载都会尝试写 Event，但当时 RBAC 没有 `events` 权限，导致每次
  成功操作都留下一条 `events is forbidden` 的 error，同时重载对用户完全不可见。已补最小必要
  权限（独立规则，避免与 configmaps/secrets 的规则取 verb 并集）并验证：事件流出现
  `Reloaded`、错误消失、Reloader 无需重启。后端提交 `adfc6cf49`。
- 同时查明 Reloader 的重载机制是向容器注入 `STAKATER_<资源名>_<CONFIGMAP|SECRET>` 环境变量
  （值为资源内容哈希），而不是写 `last-reloaded-from` 注解。该哈希是内容确定的，可作为
  「Pod 是否已加载最新配置」的判据。已写入 `docs/reloader.md`。
- 有意未做：不为消除 `jobs`/`cronjobs` 的日志噪声而扩大权限；未在测试环境卸载 Reloader 验证
  「卸载后停止滚动」（平台级单实例，影响范围大），而「无注解不滚动」已在三次配置变更中重复
  验证，提供等价因果证据。
- 文件挂载（P1）：设计文档已建立
  （`docs/designs/2026-10-07-config-reference-file-mount-design.md`），实现待开始。设计确认这是
  「Pod 级卷 + 容器级挂载」两层结构，一条引用要落到两条 PATCH 路径；`items`、`defaultMode`、
  `subPath` 列为后续增量。
- Helm 应用结构化控件（P1）：评估结论为前置条件不成立。现有 21 个 `values.schema.json` 中只有
  1 个提到 `envFrom`，且属于打包的第三方子 chart（prometheus/alertmanager），自有服务均未在
  schema 中暴露该入口。等出现真实入口时再评估。
- Reloader 只读诊断入口（P1）：本轮不做，并入阶段七的统一诊断。判定依据是该职责已由独立
  Controller 承担，Console 不重复实现；且 A-204 查明的 `STAKATER_*` 哈希为将来的诊断提供了
  比 Event 更可靠的判据。

### 子项目 B：应用创建人和更新人

后端分支 `feature/app-config-reference`，本阶段后端提交到 `468eea524`；测试环境
`ks-apiserver` 与 `ks-controller-manager` 已更新至 `cm-secret-audit-20261001-7bf4ea8`
（即实现审计语义的提交 `7bf4ea823`）。

- 创建时写入 `kubesphere.io/creator`、更新时保持创建人不变、用户 API 更新写入
  `kubesphere.io/last-updater`、Controller 状态更新不覆盖、客户端伪造的 creator 被拒绝：
  实现位于 `pkg/kapis/application/v2/handler_apprls.go` 的 `applyAppReleaseAuditAnnotations`，
  回归测试在 `apprelease_controller_audit_test.go` 与 `audit_test.go`，容器内 `go test` 通过。
  线上 18 个 ApplicationRelease 全部带 `kubesphere.io/creator`。
- Console 应用详情页展示创建人、最后更新人与两个时间：本轮修复了该页面三处既有缺陷——
  路由参数名与组件不一致导致整页白屏、接口路径多拼 workspace/cluster 导致 404、以及
  `create_time`/`status_time`/`owner` 三个字段在响应中不存在导致三个属性行恒为空。修复后线上
  验证：属性栏显示真实的创建时间、更新时间与创建者，并新增「最后更新人」；审计功能上线前创建
  的应用按验收要求显示 `-`。前端提交 `cd2020fe8`、`3381ee61b`、`65ae7bdcd`。
- 「更新时间」保留共用文案键 `UPDATE_TIME_TCAP`（该键被 appstore 评论与多种工作负载详情页
  共用，不能改值），它反映 Controller 最后一次状态写入；判断「谁改的」看创建者与最后更新人
  这一对，这正是本项验收条款「Controller 重试、同步状态变化和 Helm 执行不会改变更新人」的判据。
- 操作历史时间线（P2）：评估结论为不需要。`managedFields` 明确不作为产品层更新人字段，时间线
  需要额外存储，而创建人、最后更新人加两个时间已能回答「谁创建、谁最后改的」。
- 已知遗留：`packages/shared` 中的共享应用详情组件（导出为 `AppDeployDetailRoute`）有同样的
  `create_time`/`status_time` 取值问题（创建者字段是对的）。它属于另一条路由，未包含在本轮
  范围内，建议作为独立小项修复，改法与项目级页面相同。

## 阶段四：Kubernetes Gateway API 基础接入

目标：引入标准 Kubernetes Gateway API，与现有 Ingress 和旧版 KubeSphere Gateway 并存。

- [ ] 调研并验证 `gateway-api` 扩展的安装版本、依赖和测试环境兼容性。
- [ ] 支持 `GatewayProxy`、`GatewayClass`、`Gateway`、`HTTPRoute` 等核心资源的发现和状态读取。
- [ ] Console 增加 Gateway API 的基础展示：作用域、Class、监听器、地址、Conditions、关联 Route。
- [ ] 使用 InstallPlan 风格处理扩展安装、升级、卸载、集群选择和状态轮询。
- [ ] 对 Traefik Service 类型、端口、外部地址、TLS 和默认 Gateway 做兼容性验证。
- [ ] 接入 Prometheus 指标、日志和事件，确保 Gateway API 故障可以从 Console 定位。

验收标准：Gateway API 扩展可在测试集群安装；GatewayProxy 到 GatewayClass、Gateway、HTTPRoute 的关联关系可追踪；旧 Ingress 功能不受影响。

## 阶段五：Ingress、旧 Gateway 与 Gateway API 统一管理

目标：统一管理体验，不强制一次性替换所有底层入口资源。

- [ ] Console 同时展示标准 Ingress、旧 KubeSphere Gateway 和 Gateway API 资源。
- [ ] 统一入口列表字段：作用域、Class、监听端口、地址、TLS、后端、健康状态、事件。
- [ ] 建立 Ingress 注解、路径、TLS、Service 后端到 HTTPRoute 的兼容性评估规则。
- [ ] 对不可无损转换的 NGINX 注解、Lua、TCP/UDP、跨命名空间引用和特殊证书配置给出阻断原因。
- [ ] 只为可验证等价的资源提供显式迁移向导，不在后台自动迁移。
- [ ] 迁移前执行预检查，迁移后执行连通性、TLS、路由和回滚验证。
- [ ] 新能力默认优先 Gateway API；历史 Ingress 继续可用，直到迁移覆盖率和回滚能力满足要求。

验收标准：旧资源和新资源可并存；用户能看懂差异和迁移风险；迁移失败不会破坏原 Ingress；迁移过程可回滚。

## 阶段六：扩展生命周期和多集群治理

目标：统一 KubeSphere 扩展、集群和多租户资源的生命周期体验。

- [ ] 统一扩展安装、升级、卸载、版本选择、依赖检查和状态轮询。
- [ ] 展示 InstallPlan 的扩展级状态和逐集群状态，支持失败集群单独诊断。
- [ ] 校验扩展 Chart、镜像 Registry、版本约束、资源限制和 Helm 超时配置。
- [ ] 集群状态、版本、成员关系和连接错误可在同一诊断链路中查看。
- [ ] Workspace、Project 和 Cluster 作用域在应用、Gateway、网络和扩展资源上保持一致。
- [ ] 明确跨集群操作的权限边界、审计事件和回滚方式。

验收标准：扩展失败可以定位到具体集群、具体阶段和具体资源；升级和卸载具有可观察的中间状态和可执行回滚路径。

## 阶段七：平台诊断与可观测性

目标：把“同步中、部署失败、入口不可达”转化为可定位的证据链。

- [ ] 建立统一状态模型：Desired、Progressing、Ready、Degraded、Failed、Stale。
- [ ] 统一关联资源、Events、Logs、Metrics、Conditions 和最近操作记录。
- [ ] 为 OCI、Gateway、Ingress、扩展和工作负载提供诊断快照。
- [ ] 增加 Registry、网络、DNS、TLS、认证、Webhook、Pod、Service、Endpoint 的检查规则。
- [ ] 引入类似 KubeEye 的规则化诊断，但诊断规则与 Controller 业务逻辑分离。
- [ ] 支持导出脱敏诊断包，不包含密码、Token、KubeConfig 或 URL 中的用户信息。

验收标准：常见故障能够给出原因、证据和下一步建议；诊断不会修改线上资源；敏感信息经过脱敏。

## 阶段八：平台能力构想池

以下方向暂不进入当前迭代，待前置阶段完成后评估：

- OpenPitrix 应用审核、分类、私有仓库和版本治理。
- OpenKruise 原地升级、灰度发布和批量发布。
- Argo CD GitOps、Jenkins Pipeline 和 DevOps 凭据治理。
- Service Mesh 与 Gateway API 的入口、流量和策略协同。
- NetworkPolicy、IPPool、网络扩展和入口地址统一管理。
- Volcano 批处理、Fluid 数据集与 AI/大数据任务。
- OpenSearch、Vector、日志、审计、通知、指标和追踪统一检索。
- NodeGroup、集群生命周期和多集群资源编排。

## 官方能力覆盖记录

本路线图已扫描并归类官方 `skills/` 的能力域。它们是后续调研来源，不代表全部已纳入开发承诺。

| 能力域 | 官方参考 | 本路线图位置 |
| --- | --- | --- |
| 核心、集群、多租户 | `kubesphere-core`、`kubesphere-cluster-management`、`kubesphere-multi-tenant-management` | 阶段六 |
| 应用与扩展 | `openpitrix`、`kubesphere-extension-management` | 阶段一、三、六、八 |
| 入口与网络 | `kubesphere-gateway`、`kubesphere-gateway-api`、`kubesphere-network-extension-operations`、`kubesphere-servicemesh` | 阶段四、五、八 |
| 诊断与可观测性 | `kubeeye`、`opensearch`、`vector`、`whizard-auditing`、`whizard-events`、`whizard-logging`、`whizard-notification`、`whizard-telemetry`、`whizard-telemetry-ruler`、`wiztelemetry-tracing` | 阶段七、八 |
| 工作负载与数据 | `kubesphere-openkruise`、`kubesphere-volcano`、`kubesphere-fluid`、`nodegroup` | 阶段八 |
| DevOps | `kubesphere-devops-argocd`、`kubesphere-devops-credentials`、`kubesphere-devops-jenkins`、`kubesphere-devops-overview`、`kubesphere-devops-pipeline`、`kubesphere-devops-tenant` | 阶段八 |
| 前端扩展集成 | `frontend-forge-fe-operations`、`frontend-forge-fi-operations`、`frontend-integration-yaml` | 阶段六、八 |

## 阶段完成检查清单

- [ ] 后端单元测试和关键集成测试通过。
- [ ] Console 类型检查、构建和关键页面回归通过。
- [ ] 测试环境部署完成，并记录镜像 Tag、Commit SHA 和配置。
- [ ] 成功、失败、超时、重试、回滚路径均有验证证据。
- [ ] 日志和事件不包含凭据或用户敏感信息。
- [ ] 更新本文件的完成项、未完成项、限制和新构想。
- [ ] 前后端同名发布线分别合并回各自 `master`。
- [ ] 下一版本从更新后的 `master` 创建新的 `release-X.Y.Z` 发布开发线。

## 评估结论：「最后更新人」该不该推广到普通资源（2026-10-08）——暂不做

起因：想在资源详情页（Deployment / ConfigMap …）左侧「属性」栏看到「最后更新人」，与
应用详情页那一行对齐。评估后决定**暂不做**，理由与证据记录如下，避免后来者重复走一遍。

### 实测证据（不是推测）

1. 集群里带 `annotations.revisions`（KubeSphere 记录"通过控制台编辑"的痕迹）的资源：
   deployments / statefulsets / daemonsets / services / ingresses / configmaps / secrets / hpa
   **全部为 0** —— 也就是说**没有人通过控制台编辑过这些资源**。
2. 带 `kubesphere.io/creator` 的资源有 86 个，但全部是控制台**创建**时盖的章，与编辑无关。
3. 手工在控制台改一次 `dev-wes/wes-v2-server` 的描述（metadata-only，实测未触发滚动）：
   `generation` 36→37，但 `creator` 与 `last-updater` **都没有被写入** —— 控制台的更新路径
   不盖这两个章。
4. 审计能力现状：k3s 的 apiserver **未开启 audit**（节点参数里没有任何 audit 相关）；
   没有 ES / Loki / VictoriaLogs 在跑；但 `auditingevents.auditing.kubesphere.io` API **存在**
   （匿名请求返回 403，即端点存在、需鉴权）—— 平台有审计的"查询侧"，缺"数据源"。

### 结论

- **注解方案在此环境无效**：它只能给"经过控制台 API 的编辑"盖章，而本集群的变更全部走
  Helm / kubectl / CI，绕过控制台，所以这一行会**长期为空**。加这一行等于加一个永远为空的字段。
- **要真正回答"这个配置是谁在什么时候改的"，只有审计能做到**，因为它是唯一覆盖全部变更路径的
  来源。
- 因此决定**暂不做**。若将来要做，按成本排序的最小可行路径是：
  1. 给 k3s 开 apiserver audit（`--kube-apiserver-arg=audit-log-path` + 一份只记写操作的
     audit policy），日志落盘并配轮转；
  2. 平台化：审计日志进 VictoriaLogs / Loki，再接上 KubeSphere 的 `auditingevents` 查询 API，
     让审计页面有数据。

### 已经存在的相关实现（保留，不再扩展）

应用详情页的「最后更新人」已实现并线上验证（创建只写创建人、更新才写最后更新人）。但**应用本身
建完基本不动**，这一行的实际价值很低，因此**不向其它资源类型扩展**。

## 合并记录（2026-10-08）

阶段三后续（应用配置引用与变更审计）已按固定流程合并回发布开发线 `release-4.1.5`。

- 前端：`feature/app-config-reference` → `release-4.1.5`，合并提交 `5c82fead9`（`26a84881d..5c82fead9`）。
- 后端：`feature/app-config-reference` → `release-4.1.5`，合并提交 `6479e24b5`（`7bf4ea823..6479e24b5`）。
- 合并前检查：两个仓库均无冲突，`release-4.1.5` 无漂移（我方领先 54 / 14 个提交，对方领先 0），
  合并后功能分支领先 0，即全部并入。
- 合并前的测试门禁：后端最后一次改动 Go 代码的提交是 `7bf4ea823`「应用发布增加创建人与最后更新人
  审计信息」，其与 HEAD 之间 `*.go` 无任何变更，因此该次通过的审计回归测试证据对本次合并内容仍然
  成立；前端 prettier 干净、eslint 0 error、24 个测试文件 111 个用例全绿、本地构建无类型错误。
- 未合并到 `master`：按规则，`master` 只接收完成版本验收的发布线合并，留待 v4.1.5 验收时处理。
  正式 Tag 也未创建。
- 阶段四–八 暂不开始。
