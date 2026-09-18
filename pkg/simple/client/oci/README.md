# OCI 本地测试说明

目的
- 说明如何使用仓库中的本地测试程序 `hack/test_oci_local` 对 OCI registry 进行连通性与索引校验。

程序位置
- 测试程序：[hack/test_oci_local/main.go](hack/test_oci_local/main.go)

快速开始
1. 在项目根目录运行：

```bash
go run ./hack/test_oci_local -url oci://192.168.2.138/test/test -username admin -password Harbor12345 -insecure=true
```

如果 registry 只提供明文 HTTP，可强制启用 plain HTTP：

```bash
go run ./hack/test_oci_local -url oci://192.168.2.138/test/test -username admin -password Harbor12345 -plain-http=true
```

参数说明
- `-url`：OCI 仓库地址，示例 `oci://host/repo`。
- `-username`/`-password`：访问私有仓库的凭证（可选）。
- `-insecure`：是否跳过 TLS 校验（本地测试默认 `true`）。
- `-plain-http`：是否强制使用明文 HTTP 访问 OCI registry。

Secret 凭证示例

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: repo-credential
  namespace: kubesphere-system
type: Opaque
stringData:
  username: admin
  password: Harbor12345
  certFile: /etc/ssl/certs/client.crt
  keyFile: /etc/ssl/certs/client.key
  caFile: /etc/ssl/certs/ca.crt
  insecureSkipTLSVerify: "true"
  plainHTTP: "true"
---
apiVersion: application.kubesphere.io/v2
kind: Repo
metadata:
  name: local-oci
spec:
  url: oci://192.168.2.138/test/test
  credentialSecretRef:
    name: repo-credential
    namespace: kubesphere-system
  syncPeriod: 10
```

常见问题与提示
- 如果 registry 使用明文 HTTP，可设置 `spec.credential.plainHTTP: true`，或在 `credentialSecretRef` 指向的 Secret 中设置 `plainHTTP: "true"`。
- 如果在调用中出现 `401 Unauthorized` 或 token 请求被拒绝，请确认是否需要凭证并通过 `-username`/`-password` 提供；本仓库的 `LoadRepoIndex` 会使用传入的凭证去获取 token 并拉取 manifest。
- 成功运行后程序会打印索引条目（chart 名称、版本、digest）。

与平台集成建议
- 目前本地测试使用命令行凭证演示。线上/平台环境建议将凭证放入 Kubernetes `Secret` 并在 `Repo` 资源中通过引用加载（更安全）。

调试
- 若需要更详细的调试输出，可在本地临时修改 `hack/test_oci_local/main.go` 或直接使用 `helm pull --debug`：

```bash
helm pull oci://192.168.2.138/test/test --version 0.1.0 --plain-http --debug
```

如需我把这份说明合并到其它文档或在 API 层添加 Secret 引用读取示例，告诉我我会继续实现。
