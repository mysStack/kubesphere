# KubeSphere 配置变更自动滚动更新（可选）

KubeSphere Console 的“配置引用”页面只负责把工作负载的 `envFrom` 和
`reloader.stakater.com/auto` 注解写入 Kubernetes API。配置变更后的滚动更新由
[Stakater Reloader](https://github.com/stakater/Reloader) 独立完成，和 KubeSphere
核心 Controller、API 没有专用耦合。

## 安装

在目标集群执行：

```bash
kubectl apply -k deploy/reloader
kubectl -n kubesphere-reloader rollout status deploy/reloader --timeout=120s
```

当前清单固定使用 `ghcr.io/stakater/reloader:v1.4.14`，不使用 `latest`。

## 使用

在 Console 的 Deployment、StatefulSet 或 DaemonSet 配置引用页面打开“配置变化自动滚动更新”，
Console 会在工作负载 metadata 上写入：

```yaml
reloader.stakater.com/auto: "true"
```

关闭开关会移除该注解，但不会删除 `envFrom`、ConfigMap、Secret 或 Pod。未安装 Reloader 时，
配置引用仍然可以保存；只有自动滚动更新不可用。

Reloader 触发滚动更新的方式，是在容器上追加一个
`STAKATER_<资源名>_<CONFIGMAP|SECRET>` 环境变量，其值为被引用资源的哈希。因此：

- 开启开关后，工作负载的环境变量列表里会多出这类变量。它不是用户配置的，也不应手工修改。
- 该哈希可用于判断「Pod 是否已经加载最新配置」：它与当前 ConfigMap/Secret 的哈希一致，
  即表示该 Pod 已同步到最新内容。这比比对时间戳更可靠。

## 升级、卸载和回滚

```bash
kubectl apply -k deploy/reloader
kubectl -n kubesphere-reloader rollout undo deploy/reloader
kubectl delete -k deploy/reloader
```

卸载只移除 Reloader Controller 及其 RBAC，不修改业务工作负载、ConfigMap、Secret 或其引用。
重新安装后，仍保留 `reloader.stakater.com/auto: "true"` 的工作负载会继续被监听。

## 权限边界

Controller 读取 ConfigMap/Secret 的变更，更新 Deployment、StatefulSet、DaemonSet 的滚动更新
相关字段，并写入 Event 说明重载原因；除此之外不持有任何权限。

- `events` 的写权限只影响信息记录，不控制任何资源，是控制器的常规权限。
- 写 Event 的目的是让重载可见：用户在执行 `kubectl describe`、查看集群事件，或在 Console 的
  「事件」页时，能看到某次重启是由配置变更触发的，而不是一次无缘无故的重启。缺少该权限时
  重载不会失败，但会变成静默行为，同时每次成功重载都会在 Reloader 日志里留下一条
  `events is forbidden` 的 error。
- Console 不读取 Secret 的 `data`，页面和错误提示也不展示 Secret 值。
