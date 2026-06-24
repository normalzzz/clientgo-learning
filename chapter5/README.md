# Chapter 5: CRD 多版本与 Conversion Webhook

本章基于 chapter3 的 `Website` 自定义资源，演示如何把 API 从 `v1alpha1` 演进到 `v1`，并通过 conversion webhook 完成两个 served 版本之间的双向转换。

目标状态：

- `apps.clientgo-learning.io/v1alpha1`：served 版本，保留旧字段 `spec.port`。
- `apps.clientgo-learning.io/v1`：served + storage 版本，使用新字段 `spec.servicePort`。
- conversion webhook：负责 `v1alpha1 <-> v1` 双向转换。

## 目录结构

```bash
chapter5/
├── cmd/conversion-webhook/          # ConversionReview HTTPS 服务
├── config/
│   ├── crd/bases/                   # 生成后的 CRD，包含 webhook conversion 配置
│   ├── samples/                     # v1 和 v1alpha1 示例资源
│   └── webhook/                     # webhook Service/Deployment 示例
├── hack/
│   ├── patch-crd-conversion.go      # 给生成后的 CRD 写入 spec.conversion
│   └── update-codegen.sh            # 生成 clientset/informer/lister
├── pkg/apis/apps/
│   ├── conversion/                  # Website 版本转换函数
│   ├── v1/                          # storage API 版本
│   └── v1alpha1/                    # alpha API 版本
└── pkg/generated/                   # 生成后的 clientset/informer/lister
```

## API 差异

`v1alpha1` 的规格字段：

```yaml
spec:
  image: nginx:1.27
  replicas: 2
  port: 80
```

`v1` 的规格字段：

```yaml
spec:
  image: nginx:1.27
  replicas: 2
  servicePort: 80
```

转换规则位于 `pkg/apis/apps/conversion/website.go`：

- `v1alpha1.spec.port` 转为 `v1.spec.servicePort`
- `v1.spec.servicePort` 转为 `v1alpha1.spec.port`
- `metadata`、`spec.image`、`spec.replicas`、`status.readyReplicas`、`status.phase` 保持一致

## 生成代码

```bash
make generate
```

该命令会执行：

1. `controller-gen` 生成 CRD 和 DeepCopy。
2. `hack/patch-crd-conversion.go` 给 CRD 写入 conversion webhook 配置。
3. `hack/update-codegen.sh` 生成 clientset、informer、lister。

生成后的 CRD 满足：

- `v1alpha1.served: true`
- `v1.served: true`
- `v1.storage: true`
- `spec.conversion.strategy: Webhook`

## Webhook 部署说明

webhook 服务入口在 `cmd/conversion-webhook/main.go`，默认监听：

```bash
/convert
```

部署清单在：

```bash
config/webhook/website-conversion-webhook.yaml
```

Kubernetes conversion webhook 必须使用 HTTPS。示例 Deployment 默认从 Secret `website-conversion-webhook-tls` 挂载：

```bash
/tls/tls.crt
/tls/tls.key
```

CRD 中的 `caBundle` 目前是占位值 `Cg==`。实际部署时需要替换成签发 webhook serving certificate 的 CA bundle。

## 验证

```bash
go test ./...
```

当前测试覆盖了 `v1alpha1 <-> v1` 的核心字段转换。
