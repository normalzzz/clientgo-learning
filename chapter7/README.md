# Chapter 7: 使用 Finalizer 管理资源删除流程

本章在 Website Controller 中引入 Finalizer：创建 Website 后先添加清理标记；删除 Website 时，Controller 向 API Server 提交同名 Deployment 和 Service 的删除请求，成功后再移除标记，让 Website 完成删除。

本章内容根据 [kb.md](./kb.md) 整理，重点是理解 Finalizer 与 `deletionTimestamp` 的配合，以及如何在 client-go 调谐循环中实现可重试的清理逻辑。

## 目录结构

```text
chapter7/
  main.go                          # 初始化客户端，获得 Leader 身份后启动 Controller
  handler.go                       # Website 和从属资源事件入队
  controller.go                    # Finalizer、资源清理、正常调谐和状态更新
  controller_test.go               # Finalizer 添加和资源调谐测试
  pkg/
    apis/apps/v1alpha1/             # Website API 定义
    generated/                     # Clientset、Informer、Lister
    leaderelection/                # 基于 Lease 的 Leader Election
  config/
    crd/bases/                     # Website CRD
    samples/apps_v1alpha1_website.yaml
  deploy/website-controller.yaml   # Controller 部署和 RBAC 模板
  Dockerfile
  Makefile
  go.mod
  go.sum
  kb.md
  README.md
```

## 1. 为什么需要 Finalizer

Controller 有时需要在资源消失前完成额外操作，例如释放云负载均衡器、删除 DNS 记录，或提交关联资源的删除请求。如果只在对象已经删除后处理，一旦清理失败，Controller 就难以继续通过原对象记录和重试这项工作。

Finalizer 是 `metadata.finalizers` 中的字符串键：

```yaml
metadata:
  finalizers:
    - apps.clientgo-learning.io/website-finalizer
```

它表示该对象仍有清理责任需要完成。Finalizer 本身不执行任何代码，具体操作由负责这个键的 Controller 实现。

带有 Finalizer 的对象收到删除请求后，会保留在 API Server 中。Controller 完成自己负责的清理后移除对应的键；所有 Finalizer 都移除后，对象才能完成删除。

### Finalizer 与 OwnerReference

本章创建的 Deployment 和 Service 仍然带有指向 Website 的 controller OwnerReference。

| 机制 | 作用 | 本章中的使用 |
| --- | --- | --- |
| OwnerReference | 描述资源归属，供 Kubernetes 垃圾回收器处理从属资源 | Deployment 和 Service 指向 Website |
| Finalizer | 在对象完成删除前保留清理机会 | Website 删除期间主动提交 Deployment 和 Service 的删除请求 |

普通 Kubernetes 从属资源通常可以交给垃圾回收器清理。本章通过显式删除 Deployment 和 Service 演示 Finalizer 的执行位置；外部资源清理也可以放在这一分支中。

## 2. Kubernetes 删除流程

```text
Website 正常存在，带有 Finalizer
    |
    | 用户提交 DELETE
    v
API Server 设置 metadata.deletionTimestamp，保留对象
    |
    | Informer 收到 Update 事件，Website key 入队
    v
syncHandler 进入删除分支
    |
    | 删除 Deployment，再删除 Service
    | 失败：保留 Finalizer，workqueue 退避重试
    v
两个删除请求成功，或资源已不存在
    |
    | Controller 移除自己的 Finalizer
    v
所有 Finalizer 清除后，API Server 完成 Website 删除
    |
    v
Informer 收到 Delete 事件，Lister 查询返回 NotFound
```

这里需要区分两个时间点：

- **收到删除请求**：API Server 设置 `deletionTimestamp`，通常返回 HTTP `202 Accepted`。对象仍存在，Informer 首先收到 Update 事件。
- **对象完成删除**：所有 Finalizer 清除、对象消失后，Informer 才收到 Delete 事件。

因此，清理逻辑放在 `syncHandler` 中，通过 `deletionTimestamp` 判断是否进入删除流程。仅在 `OnDelete` 中清理已经太晚。

对象进入删除流程后，不能再添加新的 Finalizer，所以需要在正常调谐阶段提前添加。

## 3. Controller 中的实现

核心代码位于 [controller.go](./controller.go)。

### Finalizer 名称和辅助函数

本章使用的键为：

```go
const websiteFinalizer = "apps.clientgo-learning.io/website-finalizer"
```

域名前缀用于区分不同 Controller 的清理责任。代码复用了 controller-runtime 的三个辅助函数：

```go
controllerutil.ContainsFinalizer(obj, finalizer)
controllerutil.AddFinalizer(obj, finalizer)
controllerutil.RemoveFinalizer(obj, finalizer)
```

它们来自 `sigs.k8s.io/controller-runtime/pkg/controller/controllerutil`。本章的事件接收、缓存和 workqueue 仍由 client-go 实现；这些辅助函数只操作内存中的对象，修改后还需要调用 Kubernetes API 持久化。

### 正常调谐前添加 Finalizer

```go
if website.GetDeletionTimestamp() == nil &&
    !controllerutil.ContainsFinalizer(website, websiteFinalizer) {
    updated := website.DeepCopy()
    controllerutil.AddFinalizer(updated, websiteFinalizer)

    if _, err := c.websiteClient.AppsV1alpha1().
        Websites(namespace).
        Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
        return fmt.Errorf("add finalizer to Website %s/%s: %w", namespace, name, err)
    }
    return nil
}
```

这段逻辑有三个要点：

1. 只对尚未进入删除流程的 Website 添加 Finalizer。
2. 通过 `DeepCopy()` 修改副本，避免直接修改 Lister 返回的 Informer 缓存对象。
3. 更新成功后立即返回，等待 Informer 推送新版本并再次入队，再创建 Deployment、Service 和更新 status。

提前返回可以避免后续步骤继续使用添加 Finalizer 前的旧 `resourceVersion` 更新 Website。

### 删除期间清理资源

```go
if website.GetDeletionTimestamp() != nil {
    if controllerutil.ContainsFinalizer(website, websiteFinalizer) {
        if err := c.kubeClient.AppsV1().Deployments(namespace).
            Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
            return err
        }
        if err := c.kubeClient.CoreV1().Services(namespace).
            Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
            return err
        }

        updated := website.DeepCopy()
        controllerutil.RemoveFinalizer(updated, websiteFinalizer)
        _, err := c.websiteClient.AppsV1alpha1().
            Websites(namespace).
            Update(ctx, updated, metav1.UpdateOptions{})
        return err
    }
    return nil
}
```

清理按 Deployment、Service 的顺序提交删除请求。资源已不存在时，将 `NotFound` 视为成功，因此清理可以重复执行。

任一步骤失败都会返回错误，保留 Finalizer。只有两次删除调用成功或资源已不存在时，才移除当前 Controller 负责的键，其他 Finalizer 会保留。

当前实现以删除请求成功提交为完成条件，**不会等待 Deployment、Service 或其 Pod 彻底消失**。如果清理的是异步释放的外部资源，需要继续检查释放状态，确认完成后再移除 Finalizer。

删除分支直接返回，因此处于删除流程中的 Website 不会继续创建从属资源或更新正常业务状态。当前清理代码按 namespace 和名称删除资源，没有在删除前检查 OwnerReference；实验时应使用本章 Controller 管理的同名资源。

### 错误重试

`processNextWorkItem` 负责处理调谐错误：

```go
if err := c.syncHandler(ctx, key); err != nil {
    runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
    c.queue.AddRateLimited(key)
    return true
}

c.queue.Forget(key)
```

例如，Deployment 已删除，但删除 Service 失败，下一次调谐会把 Deployment 的 `NotFound` 当作成功，再次尝试删除 Service。移除 Finalizer 时遇到更新冲突，也会通过同一条路径重试。

### Informer 如何触发清理

[handler.go](./handler.go) 中的 `WebsiteHandler.OnUpdate` 比较新旧对象的 `ResourceVersion`，版本变化时将 Website key 入队。添加 Finalizer、设置 `deletionTimestamp` 和移除 Finalizer 都会改变对象版本。

Website 真正删除后，`OnDelete` 仍会入队；此时 `syncHandler` 从 Lister 读取到 `NotFound`，直接返回成功。

## 4. 本地运行和验证

准备 Go 1.26.0 或更高版本、可访问的测试集群，以及配置好的 `~/.kube/config`。以下命令从仓库根目录进入 `chapter7` 后执行。

### 安装 CRD

```bash
cd chapter7
go mod download
kubectl apply -f config/crd/bases/apps.clientgo-learning.io_websites.yaml
kubectl wait --for=condition=Established crd/websites.apps.clientgo-learning.io --timeout=60s
```

### 启动 Controller

```bash
WATCH_NAMESPACE=default SELF_POD_NAMESPACE=default go run .
```

`go run .` 会编译当前包的全部非测试文件。程序优先读取默认 kubeconfig，失败后尝试集群内配置。

本章保留了 Leader Election：只有获得 `website-controller-leader` Lease 的实例才启动 Informer 和 worker。看到以下日志后再继续：

```text
became leader, starting Website controller
starting Website controller
```

`WATCH_NAMESPACE` 指定监听范围，空值表示所有 namespace；`SELF_POD_NAMESPACE` 指定 Lease 所在 namespace，默认是 `default`。

### 创建 Website 并检查 Finalizer

在另一个终端的 `chapter7` 目录执行：

```bash
kubectl apply -f config/samples/apps_v1alpha1_website.yaml
kubectl get website demo-website -n default -o yaml
kubectl get deployment,service -n default demo-website
kubectl get website demo-website -n default -o jsonpath='{.metadata.finalizers}{"\n"}'
```

等待 Controller 调谐后，Finalizer 输出应包含：

```text
["apps.clientgo-learning.io/website-finalizer"]
```

确认 Finalizer 已添加，再进行删除实验。

### 观察删除中的对象

为了观察对象保留期间的状态，先用 `Ctrl+C` 停止本地 Controller，并确保没有其他 Controller 实例接手处理这个 Website。

提交删除请求，但不等待对象消失：

```bash
kubectl delete website demo-website -n default --wait=false
kubectl get website demo-website -n default -o jsonpath='{.metadata.deletionTimestamp}{"\n"}{.metadata.finalizers}{"\n"}'
```

此时应能看到非空的 `deletionTimestamp` 和仍然存在的 Finalizer。对象处于删除流程中，等待 Controller 清理。

重新启动 Controller：

```bash
WATCH_NAMESPACE=default SELF_POD_NAMESPACE=default go run .
```

Controller 获得 Leader 身份后，初始同步会发现这个正在删除的 Website，继续执行清理。在另一个终端检查：

```bash
kubectl wait --for=delete website/demo-website -n default --timeout=60s
kubectl wait --for=delete deployment/demo-website -n default --timeout=60s
kubectl wait --for=delete service/demo-website -n default --timeout=60s
```

### 查看 HTTP 删除响应

可以另做一次实验观察 API Server 返回的 `202 Accepted`。先在 Controller 运行时重新创建 Website，确认 Finalizer 已添加，再停止所有处理它的 Controller 实例。

启动本地 API 代理：

```bash
kubectl proxy --port=8001
```

在另一个终端提交删除请求。本章 API 版本为 `v1alpha1`：

```bash
curl -i -X DELETE \
  'http://127.0.0.1:8001/apis/apps.clientgo-learning.io/v1alpha1/namespaces/default/websites/demo-website' \
  -H 'Content-Type: application/json' \
  --data '{"apiVersion":"v1","kind":"DeleteOptions"}'
```

代理通过 kubeconfig 访问 API Server。带有 Finalizer 的 Website 会保留在删除流程中；实验后重新启动 Controller 完成清理，并用 `Ctrl+C` 结束代理。

## 5. 权限与排障

### 部署到集群时的权限

当前 [部署模板](./deploy/website-controller.yaml) 需要替换镜像地址，并补充 Finalizer 流程所需权限：

| API Group | 资源 | 需要补充的 verb | 用途 |
| --- | --- | --- | --- |
| `apps.clientgo-learning.io` | `websites` | `update` | 持久化 Finalizer 的添加和移除 |
| `apps` | `deployments` | `delete` | 提交 Deployment 删除请求 |
| `""` | `services` | `delete` | 提交 Service 删除请求 |

这里通过 Website 主资源的 `Update` 修改 metadata，仅有 `websites/status` 的更新权限不足以保存 Finalizer。Leader Election 还依赖 Lease 权限，模板中已有对应规则。

完成上述调整后，可执行：

```bash
kubectl apply -f deploy/website-controller.yaml
kubectl logs -n website-controller deployment/website-controller -f
```

### Website 一直处于删除状态

先查看资源元数据和 Controller 日志：

```bash
kubectl get website demo-website -n default -o yaml
kubectl get lease website-controller-leader -n default -o yaml
```

Lease 的 namespace 应与实际 `SELF_POD_NAMESPACE` 一致；使用部署模板时为 `website-controller`。

| 现象 | 检查方向 |
| --- | --- |
| 有 deletionTimestamp，但 Finalizer 一直保留 | 是否有活跃 Leader，监听范围是否包含 Website 所在 namespace |
| 日志出现 Forbidden | Website 的 update、Deployment 和 Service 的 delete 权限是否齐全 |
| 清理请求反复失败 | 根据日志中的 API 错误检查权限、连接或资源状态，修复后 Controller 会重试 |
| 本章 Finalizer 已移除，对象仍未删除 | 是否还有其他 Controller 负责的 Finalizer |
| Website 已消失，子资源仍在终止 | 本章只等待删除请求成功，子资源自己的 Finalizer 和终止流程仍可能继续 |

应先修复清理失败的原因，再让 Controller 完成删除。手动清空 Finalizer 会跳过对应的清理责任，可能留下关联资源。

## 6. 测试

在 `chapter7` 目录运行现有单元测试：

```bash
go test ./...
```

[controller_test.go](./controller_test.go) 当前覆盖：

- 在创建从属资源之前添加 Finalizer，且不修改 Informer 缓存对象。
- 已有 Finalizer 时创建 Deployment、Service 并更新 Website status。
- 副本数和端口的默认值。

这些测试使用 fake client；删除分支的失败重试尚无专项单元测试，真实 API Server 的 Finalizer 保留和删除行为可按上面的集群实验验证。
