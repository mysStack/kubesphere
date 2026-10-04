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

## 升级、卸载和回滚

```bash
kubectl apply -k deploy/reloader
kubectl -n kubesphere-reloader rollout undo deploy/reloader
kubectl delete -k deploy/reloader
```

卸载只移除 Reloader Controller 及其 RBAC，不修改业务工作负载、ConfigMap、Secret 或其引用。
重新安装后，仍保留 `reloader.stakater.com/auto: "true"` 的工作负载会继续被监听。

## 权限边界

Controller 只获取 ConfigMap/Secret 的变更事件，并更新 Deployment、StatefulSet、DaemonSet 的
滚动更新相关字段。Console 不读取 Secret 的 `data`，页面和错误提示也不展示 Secret 值。
