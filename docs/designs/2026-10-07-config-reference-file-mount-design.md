# 配置引用的文件挂载设计

## 背景

「配置引用」入口目前只生成 `envFrom`，把 ConfigMap/Secret 的键注入为环境变量。PLAN 阶段三
后续子项目 A 的 P1 要求补充文件挂载：生成 `volumes[].configMap` / `volumes[].secret` 与
`volumeMounts[]`，并校验挂载路径冲突、只读属性和资源作用域。

文件挂载与 envFrom 的对象层级不同，这是本次设计要解决的核心问题：

| | envFrom | 文件挂载 |
|---|---|---|
| 引用所在层级 | 容器级（`containers[i].envFrom`） | 卷是 **Pod 级**（`spec.volumes[]`），挂载是 **容器级**（`containers[i].volumeMounts[]`） |
| 归属 | 一个引用只属于一个容器 | 一个卷可被同一 Pod 的多个容器挂载 |
| PATCH 目标 | 一条路径 | 两条路径，且必须在同一次请求内保持一致 |

`packages/bootstrap/assets/v3dist` 是已编译的稳定兼容制品，禁止替换或重新构建，完整 V3 源码
不可获得。因此本能力继续由 Console 的独立配置引用入口实现，不依赖 V3 表单，也不把 V3 的
原生能力当作可依赖项。

## 目标

- 在 Deployment、StatefulSet、DaemonSet 的配置引用入口中增加文件挂载，生成并回显 Kubernetes
  原生 `volumes[].configMap` / `volumes[].secret` 与 `volumeMounts[]`。
- 复用现有入口的资源选择器、作用域规则、键名读取方式，以及「Secret 只保存名称、前缀与键名，
  不读取也不展示值」的约束。
- 保存时只 PATCH `spec.template.spec.volumes`、目标容器的 `volumeMounts` 和工作负载注解，
  不整对象 PUT、不触碰其它字段。
- 保留现有能力：`envFrom`、逐 Key 的环境变量引用，三种方式可在同一容器共存。
- 挂载前给出预览：该资源会以哪些键、以什么文件名出现在容器内。

## 非目标

- 本轮不支持 `items`（键到文件名的投影）、`defaultMode`（权限位）和 `subPath`。三者都需要
  独立的界面与校验，留作后续增量。
- 不为 Helm 应用提供结构化控件，与 envFrom 的边界保持一致；PLAN 196 的前置条件（Chart 在
  `values.schema.json` 中暴露入口）目前不成立。
- 不接管 V3 的创建/编辑路由，不修改 `v3dist` 制品。
- 不实现文件挂载的反向影响分析、批量重启或重启队列；变更生效继续由 Reloader 注解负责。

## 目标信息架构

配置引用入口内部按「消费方式」分为两组，共用同一个资源选择器和作用域规则：

```text
配置引用
├── 环境变量（envFrom）                        ← 现有能力，本轮行为不变
│     来自配置字典  ewms-rabbitmq-config
│     ✓ 将生成 3 个环境变量
└── 文件挂载（volumes + volumeMounts）          ← 本次新增
      来自配置字典  ewms-rabbitmq-config
      挂载路径  /etc/app/config     只读 ☑
      ✓ 将挂载 3 个文件：RABBITMQ_PORT、RABBITMQ_URL、RABBITMQ_USERNAME
```

两组都在「当前容器」的语境下编辑，与 envFrom 的交互一致，但保存时映射到不同层级：

- 一条文件挂载 = 一个 Pod 级卷 + 当前容器上的一条 `volumeMount`。
- 同一个资源被同一 Pod 的多个容器挂载时**复用同一个卷**（目标资源一致即复用），避免 Pod 内
  出现多个指向同一资源的卷。
- 卷名由资源名派生并做 DNS-1123 合法化；重名时追加短后缀。界面上只在必要时展示卷名，不要求
  用户为卷命名。
- 切换容器时，文件挂载列表显示的是**该容器的挂载**；卷本身属于 Pod，因此同一资源在另一个
  容器里会显示为复用同一卷的新挂载。

## 操作语义

| 操作 | 生成的对象 | PATCH 路径 |
|---|---|---|
| 添加文件挂载 | 必要时新增一个卷，并给当前容器加一条 `volumeMount` | `/spec/template/spec/volumes`、`/spec/template/spec/containers/<i>/volumeMounts` |
| 修改挂载路径或只读 | 只改 `volumeMount` | `.../containers/<i>/volumeMounts` |
| 修改引用的资源 | 改卷的资源引用；若有其它容器仍挂载该卷，则该卷不能改，改为新建卷 | 两条路径 |
| 删除一条文件挂载 | 删除该 `volumeMount`；卷不再被任何容器挂载时一并删除 | 两条路径 |
| 开启/关闭自动滚动更新 | 写入或移除 `reloader.stakater.com/auto` | 工作负载注解 |

一次保存把上述所有变更合并成**一个** JSON Patch 请求，保证卷与挂载不会出现只改一半的中间态。
保存前执行下面的校验；任一校验失败则不发送请求，并把问题定位到具体行。

## 校验规则

- **挂载路径**：必须是绝对路径（以 `/` 开头），同一容器内不允许出现重复的挂载路径。
- **只读**：默认勾选。配置类文件不应被容器写入；取消勾选时在行内提示这是可写挂载。
- **卷名**：必须符合 DNS-1123 label（小写字母、数字、`-`、`.`，以字母数字开头结尾，最长 63）。
  由资源名自动派生，用户一般不需要修改。
- **同一容器重复挂载同一卷**：不允许。
- **资源作用域**：选择器只列出当前 Cluster/Project 可见的 ConfigMap/Secret，与 envFrom 使用
  同一条规则。
- **键名预览**：配置字典的全部键都会成为文件名；Secret 只展示键名。以 `.` 开头或含非法字符
  的键（例如 `.dockerconfigjson`）会以原样成为文件名，这在文件挂载下是**合法且可用**的
  ——与 envFrom 不同，不需要提示「会被丢弃」，但需要在预览里原样列出，避免用户以为丢键。

## 实现边界

### Console

- `workload.ts` 增加卷与挂载的读取（`getWorkloadVolumes` / `getContainerVolumeMounts`）与
  Patch 构建（在现有的 `buildConfigReferencePatch` 上扩展，保持「只改目标字段」的性质）。
- `EnvFromReference` 之外新增文件挂载的数据结构；两者共用资源选择器与键名读取。
- 预览复用现有的资源键取值流程，输出「将挂载 N 个文件」与文件名清单。
- 保存沿用 `application/json-patch+json`，错误提示沿用现有的字段级定位（解析响应里的
  `volumes[<i>]` / `volumeMounts[<i>]`）。

### 后端

不需要改动。文件挂载是 Pod 模板内的原生字段，现有 API 已经支持；本能力不引入新的 CRD、
注解或控制器。

### 与 Reloader 的关系

自动滚动更新的开关语义不变，同一个 `reloader.stakater.com/auto` 注解同时覆盖 `envFrom` 和
文件挂载：Reloader 监听的是**被引用的 ConfigMap/Secret**，与消费方式无关。

需要说明的是，A-204 的端到端验证覆盖的是 `envFrom`（三种工作负载、ConfigMap 与 Secret、
开关开与关）。文件挂载路径尚未单独验证，实现完成后需要按同样的方法补一次验证，不能只凭
「机制相同」推断。

### 与原生 V3 的关系

V3 制品内存在卷挂载相关的实现，但源码不可获得，本项目不把它当作可依赖或可修改的能力。
本设计与 V3 原生路径不冲突：两者操作的是同一批 Kubernetes 字段，最终一致。

## 响应式与可访问性

- 挂载路径输入框在窄屏下换行显示，不与只读开关挤在同一行。
- 只读开关与删除按钮保留可访问名称。
- 行内校验提示出现在对应行下方，不遮挡其它字段。

## 验收标准

- 标准工作负载可以通过独立入口引用完整 ConfigMap/Secret 并作为文件挂载，保存后 `volumes`
  与 `volumeMounts` 正确生成，重新打开页面能正确回显。
- 同一个资源被同一 Pod 的多个容器挂载时只生成一个卷；删除最后一个挂载后卷被清理。
- 同一容器内重复挂载路径、非法卷名、重复挂载同一卷都会被拦截并定位到行。
- Secret 内容不出现在页面、请求、日志或事件中。
- 未启用自动重启时，修改被引用的 ConfigMap/Secret 不会滚动 Pod；启用后 Reloader 能使引用该
  资源的标准工作负载滚动更新（含文件挂载路径的独立验证）。
- 完整 V3 页面、字段与路由不受影响。

## 已知限制与待决

- `items`、`defaultMode`、`subPath` 不在本轮范围。若需要「只挂载某一个键」，当前只能用
  envFrom 或逐 Key 的环境变量引用绕开，或等后续增量。
- 卷与挂载由资源名派生，改名或在另一个容器里换成别的资源时可能新建卷；这是为了避免改到
  其它容器仍在使用的卷，属于有意取舍。
- `packages/shared/src/components/Apps/Applications/DetailInfo` 等共享组件是否也需要同类能力，
  取决于后续是否在应用层暴露文件挂载入口，本轮不涉及。
