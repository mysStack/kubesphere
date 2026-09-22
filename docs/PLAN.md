# KubeSphere 定制项目路线图

> 本文件是当前 KubeSphere 定制项目的唯一路线图。每个阶段在独立分支完成，前后端使用同名分支；阶段完成后，代码、设计和本文件一起合并回主分支。

## 管理规则

1. `master` 只保留已验证、可回滚的阶段成果。
2. 每个阶段从最新 `master` 创建同名分支，例如 `feature/oci-observability`。
3. 阶段开始时，将对应事项标记为 `进行中`，并在 `docs/designs/` 创建技术设计文档。
4. 阶段开发、测试和测试环境验证完成后，更新本文件：完成项、未完成项、已知限制和新构想。
5. 前后端仓库分别提交，但使用同一个阶段名；不得直接合并未验证的半成品。
6. 设计文档描述本阶段实现细节；本文件只维护路线、状态和验收结果。

状态标记：`[x] 已完成`、`[~] 进行中`、`[ ] 待完成`、`[!] 受阻/需决策`。

## 当前基线

- 当前后端分支：`fix/oci-repo-sync-status`
- 当前前端分支：`fix/oci-repo-sync-status`
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
- [~] P0：私有 Helm/OCI 仓库凭据管理。Console 支持创建或选择受 Workspace 边界保护的仓库凭据；Repo 仅保存 Secret 引用，验证、同步和部署复用该引用，凭据不回传、不出现在 URL、Status、Event 或日志中。
- [ ] 扩展 Repo 状态：同步开始时间、结束时间、耗时、版本数量、缓存命中数、最近错误。
- [ ] 为 OCI 失败场景补充可读的 Status Reason、Kubernetes Event 和 Console 错误展示。

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
- [ ] 增加仓库健康信息：远端版本数、有效 Chart 数、跳过数、失败数、请求数、最近同步耗时。
- [x] 增加离线 Registry Mock 测试：77 个版本、metadata Artifact、429、慢响应、重复 digest、部分失败。

当前默认值：metadata 请求并发上限为 4、tag 分页大小为 100、单次请求超时 30 秒；429/5xx/超时最多尝试 3 次，退避为 200ms、500ms，并优先遵循 `Retry-After`。`applicationRepository.oci.cacheTTL` 默认为 `0s`，不会额外触发全量请求；设置为正值后，过期的 Repo 在下一次正常或手动同步时做一次全量 metadata 校验，只有无 warning 的完整结果才更新 `application.kubesphere.io/oci-cache-validated-at`。默认同步假定 OCI tag 不可变，已缓存 tag 不会重新拉取 manifest；需要重新校验旧 tag 时，也可使用 OCI 仓库的“全量校验”动作一次性绕过缓存，下一次默认同步恢复缓存。

验收标准：重复同步主要命中缓存；同一 Registry 多仓库不会无限并发；429 不导致整个 Repo 进入不可恢复状态；同步结果可解释、可重试、可观测。

阶段二显式全量校验验证记录（2026-09-19）：

- 后端源码 commit `9eaab0fbd`，测试镜像 `ghcr.io/mysstack/ks-apiserver:oci-repo-full-refresh-20260919-9eaab0f`、`ghcr.io/mysstack/ks-controller-manager:oci-repo-full-refresh-20260919-9eaab0f`；构建记录：[Build Personal Images](https://github.com/mysStack/kubesphere/actions/runs/35430253770)。
- Console commit `ad56b3c66`，测试镜像 `ghcr.io/mysstack/ks-console:oci-repo-full-refresh-20260919-ad56b3c`；构建记录：[Build Personal Console Image](https://github.com/mysStack/console/actions/runs/35430254040)。
- 测试环境的 `ks-apiserver`、`ks-controller-manager`、`ks-console` 均完成滚动更新并保持 `1/1` Ready。
- 已有缓存版本的 OCI Repo `backend-app` 依次完成默认同步、全量校验、再次默认同步，均收敛到 `successful`；全量标记已被消费，既有 `0.1.0` 版本 digest 保持不变。聚焦 Controller/OCI loader 测试同时确认默认同步不请求缓存 tag 的 manifest/config、全量校验重新读取一次、后续默认同步不增加 metadata 请求。
- HTTPS Repo `argo-helm` 默认同步收敛到 `successful`；聚焦测试确认 HTTPS 路径不调用 OCI loader。
- 全量校验会对每个有效 OCI tag 重新请求 manifest/config，公共 Registry 上会增加配额消耗、限流概率和同步耗时；日常同步应继续使用默认缓存路径，仅在旧 tag 可能被覆盖或缓存需要重建时执行全量校验。

触发器并发回归修正（2026-09-22）：

- `RepoReconciler` 在状态写入后使用 API Reader 重新读取触发器，并只将该强一致读到的 annotations、resourceVersion 同步回本轮对象；避免把 `UpdateStatus` 的 metadata 回写重新引入丢失并发触发器的风险。
- 新增回归覆盖：状态写入后的既有手动/全量触发器可在同一轮消费；缓存读取后新增的触发器不会被上游预检查遗漏；消费期间写入新触发器仍返回 Conflict 并显式 requeue，重试后保留并执行新触发器。

## 阶段三：应用商店和同步体验

目标：让 Console 能区分“仓库可用”“正在同步”“发现新版本”“上次同步失败”。

- [ ] Repo 状态统一为 `Ready`、`Syncing`、`NewVersionDetected`、`Failed`、`Stale` 等可读状态。
- [ ] 手动同步 API 返回快速响应，重复点击具有幂等行为；正在执行时返回当前任务信息。
- [ ] Console 显示开始时间、进度、最近成功时间、耗时、版本统计和失败原因。
- [ ] 定时 Job 和“立即同步”复用同一个后台同步入口，不复制同步逻辑。
- [ ] 支持只新增、更新全量、重新验证三种明确动作；默认动作在 UI 中清晰标注。
- [ ] 商店版本列表在同步完成后自动刷新，不要求用户重新进入页面。
- [ ] 增加仓库诊断入口，显示最近事件和推荐处理方式，但不暴露密码或 Token。

验收标准：用户点击立即同步后页面不超时；状态最终可收敛到成功或失败；新版本能刷新到应用部署选择列表；错误不再只显示“同步中”。

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
- [ ] 前后端同名分支分别合并回各自 `master`。
- [ ] 下一阶段从最新 `master` 创建新分支，不在旧阶段分支上继续堆叠。
