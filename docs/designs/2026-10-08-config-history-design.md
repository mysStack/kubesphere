# 配置对象修改记录 —— 设计文档

## 需求

为 ConfigMap / Secret 提供与工作负载「修改记录」等价的能力：列出每次变更、显示变更时间、
并与上一条记录做内容对比。**不需要知道是谁改的**，目标是"看清改了什么、什么时候改的"。
本条是 release-4.1.5 的最后一个需求。

## 现状与实测证据

1. 工作负载的「修改记录」页签（`REVISION_RECORDS` → `.../revision-control`）之所以能工作，是因为
   K8s 为工作负载原生保留 **ReplicaSet / ControllerRevision**，每次滚动留一份 pod 模板，
   平台只是把它列出来并做 diff。ConfigMap / Secret **没有任何等价的 K8s 原生机制**，
   `resourceVersion` 只能反映"当前"。
2. diff 视图**平台已有**：`@kubed/diff-viewer@^0.2.31`，`RevisionControl/index.tsx` 直接使用
   `<DiffViewer>`；也就是说 UI 侧主要是复用，不是新写。
3. 记录体积必须考虑：全集群 ConfigMap 最大 138KB（中位 189B）；**Secret 最大 1.28MB**（Helm
   release secret），P95 160KB，78 个超过 20KB。K8s 单对象上限 1.5MB，注解也在同一预算内。
   → **把 10 份快照存进对象自身的注解不可行**，必须存到独立对象。
4. 控制器骨架齐备：`pkg/controller/` 下已有约 40 个 controller（含 `secret`），统一以
   `SetupWithManager(mgr *kscontroller.Manager)` 注册，新增一个 watch 型 controller 是既有路径。
5. **变更来源不可得**：唯一可能承载调用者身份的 `metadata.managedFields` 在本集群**为空** ——
   实测 393 个 ConfigMap 与 309 个 Secret，无一存在 managedFields 条目（k3s 行为）。因此任何
   "Helm / kubectl / 控制台"式的来源标注都不可实现。可可靠推导的只有下面三类"管理方式"：
   对象上有 `meta.helm.sh/release-name` → Helm 管理；有 `replicator.v1.mittwald.de/*` → replicator
   同步；其余 → 直接管理。要精确定位某一次是谁改的，只有 apiserver 审计日志能做到，而本需求
   明确不需要该信息。
6. 关键取舍依据：**记录必须由 watch 产生，而不是在写入时打标**。因为本集群的变更几乎全部走
   Helm / kubectl / CI，绕过控制台 API；watch 监听的是 API server 的变更通知，因此覆盖所有写入
   路径。这也是本条需求能成立、而"最后更新人"那类写入时打标方案不成立的根本原因。

## 方案

### 后端：一个 watch 型 controller

- 位置：`pkg/controller/` 下新增（与现有 controller 一致），由 `ks-controller-manager` 注册。
- 监听：`ConfigMap` 与 `Secret` 的 create / update。
- 行为：内容（`data` / `binaryData` / `stringData` 落库后的 `data`）发生变化时，追加一条记录；
  内容未变（例如只改了无关注解）不记录。
- 保留：最近 10 条，超出丢弃最旧。
- **必须忽略自己写记录引起的变更**，否则自触发死循环 —— 这是本实现的首要风险点。
- 排除 Helm release secret（`sh.helm.release.v1.*`）：它们是 Helm 的内部状态，体积最大且每次
  Helm 操作都变，记录它们既无意义又会迅速撑大存储。
- 记录"**管理方式**"从对象注解可靠推导，不使用 `managedFields`（见证据第 5 条，本集群它为空）：
  有 `meta.helm.sh/release-name` → 记为「Helm 管理」并带上 release 名；有
  `replicator.v1.mittwald.de/*` → 记为「replicator 同步」并带上源版本；其余记为「直接管理」。
  该字段只描述"这个对象由谁在管"，**不声称知道某一次是谁改的**。

### 存储：同命名空间的独立 Secret

- 源对象是 ConfigMap 时，记录存为 `<name>-history` 的 **Secret**；源对象是 Secret 时同样存为
  Secret。**一律用 Secret**，避免把 Secret 内容以明文写进 ConfigMap。
- 放在**源对象所在的命名空间**，从而自然沿用项目级 RBAC 边界。
- 记录内容为 gzip + base64 的 JSON 数组，元素形如
  `{revision, createdAt, managedBy, managedByRef, size, data}`；并对单条记录设置体积上限（超限时只存摘要标记，
  不存内容），避免个别超大对象把存储撑爆。

### 前端：复用现有视图

- 在 ConfigMap / Secret 详情页新增「修改记录」页签，与该页现有页签并列。
- 列表与对比视图**复用 `@kubed/diff-viewer`**，观感与工作负载的「修改记录」一致。
- 效果图见 `docs/designs/assets/config-history-mockup.png`（本次一并产出）。

### 非目标

- **不做回退**。本集群大量配置对象由 Helm 管理，把历史快照写回去会让对象与 Helm 状态分叉，
  下次升级即被覆盖。回退需要单独评估。
- 不做"谁改的"。该问题的结论已在 `docs/PLAN.md` 记录：写入时打标不覆盖真实变更路径。

### 后续可选增强（不在本条范围）

Helm 的 release secret（`sh.helm.release.v1.<release>.v<N>`）里存有该次发布的完整 manifest 与
时间。把对象内容与各次 release 的 manifest 做比对，可以判定"这次变更对应哪一次 Helm 发布"，
从而把「Helm 管理」细化为「Helm release wes-server v12」。这需要额外的比对逻辑，本轮不做。

## 作用域与写入时机（实现后补充，含三次部署事故）

这一节是设计定稿之后的修正。原文只写了"排除 Helm release secret"，既没有限定作用域，也没有规定
何时写入第一条记录 —— 这两处遗漏导致本功能在真机部署时连续失败三次，每次都往集群里创建了几百个
对象。以下是实测数据与最终规则。

### 作用域：项目命名空间，且工作区不是 system-workspace

**依据来自测量，不是推断。** 我们把集群里 61 个命名空间的工作区归属与全部标签拉出来，对四个候选
规则当场做筛选对比，只有一条能同时满足"系统命名空间全排除、项目命名空间全保留"：

| 候选规则 | 结果 |
|---|---|
| 有 `kubesphere.io/workspace` 标签 | ✗ 混入 `kube-system`、`kubesphere-system`、`kubesphere-monitoring-system`、`default` |
| **有标签 且 工作区 != `system-workspace`** | **✓ 采用** |
| 有标签 且 再排除 `public-workspace` | ✓ 但会连带排掉 `devops`、`monitoring`、`alerting` 等真实命名空间 |
| 有标签 且 命名空间名不以 `kube-`/`kubesphere-`/`extension-` 开头 | ✗ 混入 `default` |

**为什么"有工作区标签"完全没有区分力**：在 KubeSphere 里**系统命名空间也属于某个工作区**，它们
统一落在 `system-workspace`。本集群的真实分布是：

```
system-workspace   25 个：default · extension-*（十余个）· kruise-system · kube-system · kubesphere-* …
test-workspace     15 个：ewms-config-dev · external-secrets · hertzbeat · jobpilot · kyverno · openbao-system · test-* …
dev-workspace      13 个：dev-bms · dev-db · … · dev-wes · dev-wms
public-workspace    6 个：aaaa · alerting · devops · monitoring · nfs · watchalert
（无工作区标签）      2 个：argo-events · kubesphere-reloader —— 扩展托管，正确排除
```

按"有标签"筛选会选中 59/61 个命名空间，包括 `kube-system`；按最终规则筛选是 34 个，系统命名空间
一个不留。

### 写入时机：不播种

**第一条记录只在内容真正发生变化时写入，绝不在启动枚举时写入。**

机制：进程内维护一个基线表，按 `命名空间/名称` 记住内容哈希（**只存哈希，不存内容**）。

- 首次看到某个对象 → 只记基线，**不写任何对象**；
- 再次看到且内容不同 → 写入第一条记录（它的序号是 `#1`，且**没有可对比的前一条**）；
- 内容未变 → 不写，也不重写历史 Secret。

**代价（明确记录）**：一个对象能对比的最早版本是它的**第一次变更**，而不是上线时刻的状态；控制器
停机期间发生的变更也会丢失。这是"不播种"必须付的代价，换来的是**空闲集群里零对象**。

### 三次部署事故与教训

| 部署 | 当时的作用域 | 空闲集群里凭空产生的对象 |
|---|---|---|
| 第一次 | 无（只排除 Helm secret） | **516 个 / 15 分钟** |
| 第二次 | 有 `kubesphere.io/workspace` 标签 | **513 个 / 一轮** |
| 第三次 | 有标签 且 工作区 != system-workspace | **376 个 / 90 秒** |
| 第四次（不播种后） | 同第三次 | **0 个 ✓** |

前三次我都在**缩作用域**（61 → 59 → 34 个命名空间），数字确实在降，但**我一次都没问"为什么什么都
没干也会涨"**。真正定案的是**一次测量**：

```
3 分钟内真正发生变更的 Secret：0 个
但部署后 90 秒却多出 376 个对象
```

**0 个变更解释不了 376 个对象** —— 于是才看清：controller-runtime 在启动时会把范围内**所有已存在
对象**都 enqueue 一遍，而"对空历史一律写第一条"的规则就等于**为范围内每个对象都建一条记录**。
所以 516 / 513 / 376 分别是那三次"范围内的现有对象总数"，与变更无关。

**教训**：这三次都不是"作用域没调对"，而是**我把因果关系搞反了** —— 先写规则、先部署、再看事实。
正确的顺序是**先测量、再定规则、最后部署**；而且部署后必须有一条**静置检查**（什么都不做，看对象
数是否自己增长），它能在 90 秒内抓住"枚举即写入"这类问题。

**附带的流程教训**：这三次里每一次的失败都被"历史对象总数"这一条计数抓住，且每次都用功能自己的
归属标签（`config-history.kubesphere.io/for=true`）精确清理，因此**集群从未被留在脏状态**，也从未
误删功能之外的对象。

### 验收标准（增补两条）

- [ ] 静置检查：部署后什么都不做，等待 2 分钟，全集群历史对象数保持 **0**；
- [ ] 作用域：在项目命名空间修改对象才产生记录；在 `system-workspace` 的命名空间、以及无工作区
      标签的命名空间中修改对象，均**不产生**记录。

### 前端读数注意（双重 base64）

历史 Secret 里 `data.records` 的值经过**两层编码**：Kubernetes 对 Secret 的 `data` 编一次
base64，我们自己的负载又是 `base64(gzip(json))`。前端解码必须**连解两层**再 gunzip。这一点在
联调时曾导致"Not a gzipped file (b'H4')"的误判 —— 当时怀疑是后端写错，实际是测试脚本少解了一层。

## 风险

1. **自触发循环**：controller 写记录 → 触发自身 watch。必须用来源过滤或资源版本比对阻断。
2. **体积**：大 Secret 的快照会显著放大存储。已用"单条上限 + 排除 Helm release secret"缓解，
   仍需在实现时用真实数据回归验证。
3. **噪声**：高频变更的配置对象（例如被 replicator 持续同步的对象）会产生大量记录。10 条上限
   可以兜住，但要考虑是否需要按时间窗口节流。

## 验收标准

1. 通过控制台、kubectl、Helm 三种途径修改同一个 ConfigMap，三种途径产生的变更都能被记录；
2. 每条记录可查看变更时间，并能与上一条做内容对比，对比结果与实际改动一致；
3. Secret 的修改记录可用，且记录本身以 Secret 存储（不出现明文）；
4. 连续变更超过 10 次后只保留最近 10 条；
5. 记录功能自身不产生循环写入，不改动源对象的 spec / data；
6. Helm release secret 不产生记录。
