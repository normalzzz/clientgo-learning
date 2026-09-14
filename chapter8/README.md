# Chapter 8: 使用 Kubebuilder 开发 Website Controller

本章使用 Kubebuilder 和 controller-runtime 实现 Website Controller，演示从 API 定义、代码生成、调谐逻辑到集群部署的完整流程。内容根据 [kb.md](./kb.md) 整理。

创建一个 Website 后，Controller 会维护同名 Deployment 和 ClusterIP Service，并根据 Deployment 的就绪副本数更新 Website status。Website、Deployment 或 Service 发生变化时，Controller 会重新调谐，使实际状态与 Website spec 保持一致。

## 1. Kubebuilder 是什么

Kubebuilder 是用于扩展 Kubernetes API 的脚手架工具，可以生成 API 类型、Controller 入口、CRD、RBAC、测试和部署配置。各组件在本章中的分工如下：

| 组件 | 职责 |
| --- | --- |
| Kubebuilder | 初始化项目，生成 API 和 Controller 的工程结构 |
| controller-gen | 根据 Go 类型和 marker 生成 DeepCopy、CRD、RBAC 等产物 |
| controller-runtime | 提供 Manager、Client、缓存、事件监听和调谐队列 |
| WebsiteReconciler | 实现 Website 对应的 Deployment、Service 和 status 调谐 |
| Kustomize | 组合 CRD、RBAC、Manager Deployment 等部署清单 |

前面章节手动连接的 Informer、workqueue 和 worker，在本章由 controller-runtime 管理。开发者通过 `SetupWithManager` 声明监听关系，在 `Reconcile` 中编写业务逻辑。

## 2. 目录结构

```text
chapter8/
  cmd/main.go                         # Manager 入口和 Controller 注册
  api/v1alpha1/
    website_types.go                  # Website Spec、Status 和 marker
    groupversion_info.go              # API Group、Version 和 Scheme
    zz_generated.deepcopy.go          # 生成的 DeepCopy 方法
  internal/controller/
    website_controller.go             # Reconcile 和监听关系
    website_controller_test.go         # Controller 测试
    suite_test.go                      # envtest 环境初始化
  config/
    crd/                              # CRD 和 Kustomize 配置
    rbac/                             # 角色、ServiceAccount 和绑定
    manager/                          # Manager Deployment
    default/                          # 默认部署组合
    samples/apps_v1alpha1_website.yaml
  test/e2e/                           # 集群端到端测试
  Makefile
  Dockerfile
  PROJECT                             # Kubebuilder 项目元数据
  go.mod
  kb.md
  README.md
```

## 3. 创建 Website API

### 安装和初始化

`PROJECT` 记录本章使用 Kubebuilder `4.15.0` 创建。安装方法可参照 [kb.md 中的安装步骤](./kb.md#安装-kubebuilder)，安装后检查：

```bash
kubebuilder version
```

下面的命令用于说明本章的脚手架创建过程；仓库中的 `chapter8` 已完成初始化。如需练习从头创建，应在新的空目录中执行：

```bash
kubebuilder init \
  --domain clientgo-learning.io \
  --repo github.com/normalzzz/clientgo-learning/chapter8

kubebuilder create api \
  --group apps \
  --version v1alpha1 \
  --kind Website \
  --resource=true \
  --controller=true
```

参数对应关系：

| 项目 | 值 |
| --- | --- |
| Domain | `clientgo-learning.io` |
| Group | `apps` |
| 完整 API Group | `apps.clientgo-learning.io` |
| Version | `v1alpha1` |
| Kind | `Website` |
| Resource | `websites` |
| apiVersion | `apps.clientgo-learning.io/v1alpha1` |

### 定义 Spec 和 Status

[api/v1alpha1/website_types.go](./api/v1alpha1/website_types.go) 定义了本章 API：

| 字段 | 含义 | 默认值或约束 |
| --- | --- | --- |
| `spec.image` | 工作负载镜像 | 必填，长度至少为 1 |
| `spec.replicas` | 期望副本数 | 默认 1，最小为 0 |
| `spec.port` | 容器和 Service 端口 | 默认 80，范围 1～65535 |
| `status.readyReplicas` | Deployment 的就绪副本数 | 由 Controller 更新 |
| `status.phase` | 当前状态 | Pending、Available、Degraded |

这些字段沿用前面章节的 Website 模型，通过 Kubebuilder marker 声明默认值、校验规则、status 子资源和 `kubectl get` 的展示列。

本章示例资源为：

```yaml
apiVersion: apps.clientgo-learning.io/v1alpha1
kind: Website
metadata:
  name: demo-website
  namespace: default
spec:
  image: public.ecr.aws/nginx/nginx:1.27
  replicas: 2
  port: 80
```

### 生成代码和清单

在 `chapter8` 目录执行：

```bash
make generate
make manifests
```

`make generate` 调用 controller-gen 的 object 生成器，生成 DeepCopy 等辅助方法。`make manifests` 调用 CRD、RBAC 和 Webhook 配置生成器，根据项目中的类型和 marker 生成对应清单。

修改 API 类型后执行这两个目标；修改 RBAC marker 后执行 `make manifests`。生成的 DeepCopy、CRD 和 RBAC 文件应通过生成命令更新。

## 4. Manager 和 Reconcile

### 注册 Controller

[cmd/main.go](./cmd/main.go) 将 Kubernetes 内置类型和 Website 类型注册到 Scheme，创建 Manager，然后注册 Reconciler：

```go
if err := (&controller.WebsiteReconciler{
    Client: mgr.GetClient(),
    Scheme: mgr.GetScheme(),
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "Unable to create controller", "controller", "Website")
    os.Exit(1)
}
```

Manager 负责运行 Controller、管理缓存和客户端，并提供健康检查、指标和可选的 Leader Election。本地默认不开启 Leader Election，集群 Deployment 通过 `--leader-elect` 开启。

### 实现调谐逻辑

核心代码位于 [internal/controller/website_controller.go](./internal/controller/website_controller.go)：

```go
func (r *WebsiteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := logf.FromContext(ctx)

    website := &appsv1alpha1.Website{}
    if err := r.Get(ctx, req.NamespacedName, website); err != nil {
        if apierrors.IsNotFound(err) {
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, fmt.Errorf("get Website %s: %w", req.NamespacedName, err)
    }

    deployment, err := r.reconcileDeployment(ctx, website)
    if err != nil {
        log.Error(err, "unable to reconcile Deployment")
        return ctrl.Result{}, err
    }
    if err := r.reconcileService(ctx, website); err != nil {
        log.Error(err, "unable to reconcile Service")
        return ctrl.Result{}, err
    }
    if err := r.updateStatus(ctx, website, deployment); err != nil {
        log.Error(err, "unable to update Website status")
        return ctrl.Result{}, err
    }

    return ctrl.Result{}, nil
}
```

`req.NamespacedName` 包含 Website 的 namespace 和 name，相当于手写 Controller 时放入 workqueue 的 key。每次调谐都根据这个 key 获取对象，再检查当前状态。

`WebsiteReconciler` 嵌入 `client.Client`，因此可以直接调用 `Get`、`List`、`Create`、`Update`、`Patch` 和 `Delete`。本章的调谐过程分为三步：

1. `reconcileDeployment`：创建同名 Deployment，或同步镜像、副本数、容器端口等配置。
2. `reconcileService`：创建同名 Service，或同步 selector 和端口。
3. `updateStatus`：通过 `r.Status().Update` 更新 readyReplicas 和 phase。

更新已有从属资源前，代码会检查它是否由当前 Website 控制；配置一致时不重复写入。修改对象使用 `DeepCopy()`，避免直接修改读取到的对象。

phase 的计算顺序为：就绪副本数达到期望值时为 `Available`；否则，有就绪副本时为 `Degraded`，没有时为 `Pending`。

### 控制重试

| 返回值 | 后续行为 |
| --- | --- |
| `ctrl.Result{}, err`，err 为普通非空错误 | 按限速策略重新入队 |
| `ctrl.Result{RequeueAfter: duration}, nil` | 在指定时间后重新入队 |
| `ctrl.Result{}, nil` | 本轮完成，等待后续资源事件 |

本章在资源读写失败时返回错误，由 controller-runtime 安排重试；正常调谐完成后返回空 Result。

## 5. 监听关联资源

### 使用 Owns 监听从属资源

本章实际使用的监听声明为：

```go
func (r *WebsiteReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&appsv1alpha1.Website{}).
        Owns(&appsv1.Deployment{}).
        Owns(&corev1.Service{}).
        Named("website").
        Complete(r)
}
```

`For` 指定 Website 为主资源。`Owns` 监听 Deployment 和 Service，并通过 controller OwnerReference 找到需要调谐的 Website。

创建从属资源时，代码设置归属关系：

```go
if err := controllerutil.SetControllerReference(website, desired, r.Scheme); err != nil {
    return err
}
```

事件处理关系如下：

```text
Website 变化 --------------------------> Website namespace/name
                                               |
Deployment / Service 变化                      |
    |                                          |
    +-- controller OwnerReference ------------>+
                                               |
                                               v
                                           Reconcile
                                               |
                                               v
                              同步 Deployment、Service 和 Website status
```

例如，Deployment 就绪副本数变化后会触发 Website status 更新；Service 被删除后，会触发重新创建。Website 删除后，从属资源由 Kubernetes 垃圾回收器根据 OwnerReference 清理。

### 使用 Watches 映射非从属资源

如果监听对象与 Website 之间通过 label 等方式关联，可以使用 `Watches` 和映射函数。以下为扩展示例：

```go
func mapToWebsite(ctx context.Context, obj client.Object) []reconcile.Request {
    websiteName := obj.GetLabels()[websiteLabel]
    if websiteName == "" {
        return nil
    }

    return []reconcile.Request{
        {
            NamespacedName: client.ObjectKey{
                Namespace: obj.GetNamespace(),
                Name:      websiteName,
            },
        },
    }
}

func (r *WebsiteReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&appsv1alpha1.Website{}).
        Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(mapToWebsite)).
        Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(mapToWebsite)).
        Named("website").
        Complete(r)
}
```

该示例需要导入 controller-runtime 的 `pkg/handler` 和 `pkg/reconcile`。其中 `websiteLabel` 为本章定义的 `apps.clientgo-learning.io/website`，label 值是同 namespace 下的 Website 名称。

本章依赖 controller-runtime `v0.24.1`，映射函数签名为：

```go
func(context.Context, client.Object) []reconcile.Request
```

映射结果表示需要调谐的 Website。存在一对多关联时，可以查询并返回多个请求。`Watches` 只建立事件映射；若用于管理没有 OwnerReference 的资源，还需要同步调整调谐函数中的归属检查和资源生命周期逻辑。

## 6. RBAC 权限

Controller 通过以下 marker 声明访问权限：

```go
// +kubebuilder:rbac:groups=apps.clientgo-learning.io,resources=websites,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.clientgo-learning.io,resources=websites/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.clientgo-learning.io,resources=websites/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
```

执行 `make manifests` 后，权限会生成到 `config/rbac/role.yaml`。部署时通过角色绑定授予 Manager 的 ServiceAccount；本地运行则使用 kubeconfig 对应身份的权限。

## 7. 安装与运行

### 环境准备

- Go 1.26.0 或更高版本，与本章 `go.mod` 一致。
- 可访问的 Kubernetes 测试集群，以及配置好的 kubeconfig。
- `kubectl` 和 `make`。
- Docker 和可推送的镜像仓库，用于构建和部署 Controller 镜像。
- Kind，用于运行隔离的 E2E 测试。

已有工程可直接使用 Makefile；Kubebuilder CLI 用于初始化或扩展脚手架。Makefile 会按配置版本下载 controller-gen、Kustomize 等所需工具到本章的 `bin` 目录。

以下命令从仓库根目录进入本章后执行：

```bash
cd chapter8
kubectl config current-context
make install
make run
```

`make install` 生成并安装 CRD。`make run` 依次生成清单和代码、运行格式化与 vet 检查，最后执行 `go run ./cmd/main.go`。

启动日志中的 Website、Deployment 和 Service EventSource 对应 `For` 和两个 `Owns` 声明。

### 构建镜像并部署

将 `IMG` 设置为实际可推送、且集群可拉取的镜像地址：

```bash
export IMG=your-registry/website-controller:chapter8
make docker-build docker-push IMG="$IMG"
make deploy IMG="$IMG"
```

`make deploy` 会修改 `config/manager` 中的镜像配置，再通过 Kustomize 构建 `config/default`，使用 `kubectl apply` 部署。

默认 namespace 为 `chapter8-system`，Deployment 为 `chapter8-controller-manager`：

```bash
kubectl rollout status deployment/chapter8-controller-manager -n chapter8-system --timeout=120s
kubectl logs -n chapter8-system deployment/chapter8-controller-manager -c manager -f
```

从本地运行切换到集群部署时，先结束本地 `make run` 进程，避免两个运行方式同时调谐相同资源。

## 8. 功能验证

### 创建 Website

在另一个终端的 `chapter8` 目录执行：

```bash
kubectl apply -f config/samples/apps_v1alpha1_website.yaml
kubectl get website demo-website -n default -w
```

待镜像拉取、Pod 启动并就绪后，预期状态为：

```text
NAME           IMAGE                             REPLICAS   READY   PHASE
demo-website   public.ecr.aws/nginx/nginx:1.27     2          2       Available
```

### 查看从属资源

```bash
kubectl get deployment demo-website -n default
kubectl get service demo-website -n default
kubectl get deployment demo-website -n default -o jsonpath='{.metadata.ownerReferences}{"\n"}'
```

Deployment 应有 2 个就绪副本，Service 类型为 ClusterIP、端口为 80，OwnerReference 指向 `demo-website`。

### 验证更新与恢复

修改 Website 的副本数：

```bash
kubectl patch website demo-website -n default --type=merge -p '{"spec":{"replicas":3}}'
kubectl get website,deployment -n default demo-website
```

Controller 会将 Deployment 副本数同步为 3，并在 Pod 就绪后更新 Website status。

删除 Service，观察 `Owns` 触发重新创建：

```bash
kubectl delete service demo-website -n default
kubectl get service demo-website -n default -w
```

### 测试和清理

运行基于 envtest 的测试：

```bash
make test
```

该目标会准备 API Server 和 etcd 测试二进制。E2E 测试需使用专用 Kind 集群：

```bash
make test-e2e KIND_CLUSTER=chapter8-test-e2e
```

目标会创建或复用指定的测试集群，并在测试成功后删除它，因此该名称应专用于本章测试。

完成手动实验后删除 Website：

```bash
kubectl delete -f config/samples/apps_v1alpha1_website.yaml
```

如需移除本章部署及 API，可继续执行：

```bash
make undeploy
make uninstall
```

删除 CRD 会一并删除集群内该类型的所有 Website 实例，应在本章测试集群中执行。
