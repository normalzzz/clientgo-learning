# Chapter 4: Website Controller

chapter3 定义了 `Website` 自定义资源，并生成了 Clientset、Informer 和 Lister。chapter6 的重点是使用这些代码实现一个标准的 client-go Controller。

创建下面的 Website：

```yaml
apiVersion: apps.clientgo-learning.io/v1alpha1
kind: Website
metadata:
  name: demo-website
  namespace: default
spec:
  image: nginx:1.27
  replicas: 2
  port: 80
```

Controller 会维护：

- 一个同名 Deployment，用于运行 Website Pod。
- 一个同名 ClusterIP Service，用于暴露 Website 端口。
- `Website.status.readyReplicas` 和 `Website.status.phase`。

## Controller 的代码结构

```text
chapter6/
├── main.go
├── handler.go
├── controller.go
├── controller_test.go
├── pkg/
│   ├── apis/                         # Website API 定义
│   └── generated/                    # Clientset、Informer、Lister
├── config/
│   ├── crd/
│   └── samples/
├── deploy/website-controller.yaml
└── Dockerfile
```

各文件职责：

- `main.go`：创建 Clientset 和 InformerFactory，启动 Informer 和 Controller。
- `handler.go`：实现 create、update、delete 事件回调，把资源 key 放入 workqueue。
- `controller.go`：从 workqueue 取 key，执行 Deployment、Service 和 Website status 的调谐。
- `pkg/generated`：由代码生成器生成的 Website Clientset、Informer 和 Lister。

Controller 的核心链路如下：

```text
API Server
    │ List / Watch
    ▼
Informer ──事件回调──> Workqueue ──worker──> syncHandler
    │                                           │
    └──本地缓存 <────────────── Lister 读取───────┘
                                                │
                                                ├── 调谐 Deployment
                                                ├── 调谐 Service
                                                └── 更新 Website status
```

事件回调不直接创建 Deployment 或 Service。回调只负责将 `namespace/name` 放入队列，耗时操作由 worker 异步执行。这样可以避免阻塞 Informer 的事件分发，同时利用 rate-limited workqueue 实现失败重试。

## 1. Informer 监听什么事件

本项目创建了三个 Informer：

```go
websiteInformer := websiteInformerFactory.Apps().V1alpha1().Websites()
deploymentInformer := kubeInformerFactory.Apps().V1().Deployments()
serviceInformer := kubeInformerFactory.Core().V1().Services()
```

它们分别监听：

| Informer | 监听资源 | 作用 |
| --- | --- | --- |
| Website Informer | `apps.clientgo-learning.io/v1alpha1` Website | Website 创建、规格修改或删除后触发调谐 |
| Deployment Informer | `apps/v1` Deployment | Pod 就绪数变化或 Deployment 被修改、删除后重新调谐其所属 Website |
| Service Informer | `core/v1` Service | Service 被修改或删除后重新调谐其所属 Website |

`WATCH_NAMESPACE` 用于限定监听范围。环境变量为空时监听所有 namespace，否则只监听指定 namespace：

```go
namespace := os.Getenv("WATCH_NAMESPACE")
if namespace == "" {
    namespace = metav1.NamespaceAll
}

kubeInformerFactory := informers.NewSharedInformerFactoryWithOptions(
    kubeClient,
    30*time.Second,
    informers.WithNamespace(namespace),
)
websiteInformerFactory := externalversions.NewSharedInformerFactoryWithOptions(
    websiteClient,
    30*time.Second,
    externalversions.WithNamespace(namespace),
)
```

### Website Informer 如何监听 API Server

chapter3 生成的 Website Informer 位于：

```text
pkg/generated/informers/externalversions/apps/v1alpha1/website.go
```

其核心是使用生成的 Website Client 创建 `ListWatch`：

```go
return cache.NewSharedIndexInformer(
    cache.ToListWatcherWithWatchListSemantics(&cache.ListWatch{
        ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
            return client.AppsV1alpha1().Websites(namespace).List(ctx, options)
        },
        WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
            return client.AppsV1alpha1().Websites(namespace).Watch(ctx, options)
        },
    }, client),
    &appsv1alpha1.Website{},
    resyncPeriod,
    indexers,
)
```

Informer 首先通过 `List` 获取已有对象，建立本地缓存；随后通过 `Watch` 持续接收资源变化。对 Controller 来说，这些变化最终表现为三类回调：

- `AddFunc`：对象被创建，或者 Informer 初次 List 时发现已有对象。
- `UpdateFunc`：对象发生修改；定时 resync 也可能产生新旧对象内容相同的 update 回调。
- `DeleteFunc`：对象被删除。

### 注册 Website 的 create、update、delete 回调

`controller.go` 中通过 `AddEventHandler` 注册三个回调：

```go
websiteHandler := NewWebsiteHandler(c.queue)
if _, err := websiteInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
    AddFunc:    websiteHandler.OnAdd,
    UpdateFunc: websiteHandler.OnUpdate,
    DeleteFunc: websiteHandler.OnDelete,
}); err != nil {
    return err
}
```

这里的对应关系是：

| Kubernetes 资源事件 | ResourceEventHandlerFuncs | 本项目方法 |
| --- | --- | --- |
| Create | `AddFunc` | `WebsiteHandler.OnAdd` |
| Update | `UpdateFunc` | `WebsiteHandler.OnUpdate` |
| Delete | `DeleteFunc` | `WebsiteHandler.OnDelete` |

### 注册 Deployment 和 Service 的事件回调

Controller 创建的 Deployment 和 Service 也是调谐输入。例如 Deployment 的 `readyReplicas` 改变后，需要重新计算 Website status；Service 被删除后，需要重新创建。

两个从属资源共用 `OwnedResourceHandler`：

```go
ownedHandler := NewOwnedResourceHandler(c.queue)
childHandlers := cache.ResourceEventHandlerFuncs{
    AddFunc:    ownedHandler.OnAdd,
    UpdateFunc: ownedHandler.OnUpdate,
    DeleteFunc: ownedHandler.OnDelete,
}

if _, err := deploymentInformer.AddEventHandler(childHandlers); err != nil {
    return err
}
_, err := serviceInformer.AddEventHandler(childHandlers)
return err
```

从属资源回调不会把 Deployment 或 Service 自己的 key 放入队列，而是读取其 `OwnerReference`，把所属 Website 的 key 放入队列。

### 启动 Informer

完成事件注册后，`main.go` 启动两个 InformerFactory：

```go
kubeInformerFactory.Start(ctx.Done())
websiteInformerFactory.Start(ctx.Done())

if err := controller.Run(ctx, 2); err != nil {
    log.Fatalf("controller stopped with error: %v", err)
}
```

Controller 在启动 worker 前等待三个 Informer 的缓存完成首次同步：

```go
if ok := cache.WaitForCacheSync(
    ctx.Done(),
    c.websiteSynced,
    c.deploymentSynced,
    c.serviceSynced,
); !ok {
    return fmt.Errorf("failed to wait for caches to sync")
}
```

等待缓存同步可以避免 worker 已经开始处理，但 Lister 的本地缓存中还没有初始对象。

## 2. Create、Update、Delete 回调函数的实现

事件回调定义在 `handler.go` 中，分为 `WebsiteHandler` 和 `OwnedResourceHandler`。

### Website 事件回调

#### Create：OnAdd

```go
func (h *WebsiteHandler) OnAdd(obj interface{}) {
    h.enqueue(obj)
}
```

创建 Website 时，Informer 将新对象传给 `OnAdd`。回调不执行具体业务逻辑，只调用 `enqueue` 生成 key 并放入 workqueue。

Informer 首次 List 已存在的 Website 时，也会调用 `OnAdd`。因此 Controller 重启后，即使 Website 没有发生新的修改，也会重新调谐所有已有 Website。

#### Update：OnUpdate

```go
func (h *WebsiteHandler) OnUpdate(oldObj, newObj interface{}) {
    oldWebsite, oldOK := oldObj.(*appsv1alpha1.Website)
    newWebsite, newOK := newObj.(*appsv1alpha1.Website)
    if !oldOK || !newOK {
        log.Printf("received update objects that are not Websites")
        return
    }
    if oldWebsite.ResourceVersion == newWebsite.ResourceVersion {
        return
    }
    h.enqueue(newWebsite)
}
```

`OnUpdate` 会收到修改前后的两个对象：

1. 先进行类型断言，确保对象是 Website。
2. 比较 `ResourceVersion`。如果版本未变化，说明可能只是 Informer resync，不需要重复调谐。
3. 将新对象加入队列。

当 `spec.image`、`spec.replicas` 或 `spec.port` 改变时，Website 的 `ResourceVersion` 会变化，worker 随后把新规格同步到 Deployment 和 Service。

Website status 更新同样会触发 update 事件，因此 key 可能再次入队。下一次调谐发现期望状态和实际状态一致后不会重复写入，从而结束本轮调谐。

#### Delete：OnDelete

```go
func (h *WebsiteHandler) OnDelete(obj interface{}) {
    h.enqueue(obj)
}
```

删除事件仍然将 Website key 放入队列。worker 通过 Lister 查询时会得到 `NotFound`，然后结束处理：

```go
website, err := c.websiteLister.Websites(namespace).Get(name)
if apierrors.IsNotFound(err) {
    return nil
}
```

Deployment 和 Service 设置了指向 Website 的 controller OwnerReference，所以 Website 删除后由 Kubernetes Garbage Collector 清理从属资源，Controller 不需要手动删除。

#### enqueue：将对象转换为队列 key

三个回调最终都使用同一个 `enqueue`：

```go
func (h *WebsiteHandler) enqueue(obj interface{}) {
    key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
    if err != nil {
        log.Printf("failed to build Website key: %v", err)
        return
    }
    h.queue.Add(key)
}
```

`DeletionHandlingMetaNamespaceKeyFunc` 将对象转换成 `namespace/name`，例如：

```text
default/demo-website
```

它同时支持普通对象和删除事件中的 `DeletedFinalStateUnknown` tombstone，因此 Website 删除回调可以安全地复用该方法。

队列中只保存 key，而不是保存完整对象。worker 真正处理时会通过 Lister 获取缓存中的最新版本，从而合并短时间内对同一对象的多次修改。

### Deployment 和 Service 事件回调

#### Create：OnAdd

```go
func (h *OwnedResourceHandler) OnAdd(obj interface{}) {
    h.enqueueOwner(obj)
}
```

Deployment 或 Service 被创建后，回调找到其 OwnerReference 对应的 Website 并重新入队。这样 Controller 可以在从属资源创建后继续检查状态。

#### Update：OnUpdate

```go
func (h *OwnedResourceHandler) OnUpdate(oldObj, newObj interface{}) {
    oldMeta, oldErr := meta.Accessor(oldObj)
    newMeta, newErr := meta.Accessor(newObj)
    if oldErr != nil || newErr != nil {
        log.Printf("received child update objects without metadata")
        return
    }
    if oldMeta.GetResourceVersion() == newMeta.GetResourceVersion() {
        return
    }
    h.enqueueOwner(newObj)
}
```

该方法不依赖具体资源类型，而是通过 `meta.Accessor` 读取 Kubernetes 通用元数据，因此 Deployment 和 Service 可以共用一个 handler。

Deployment 状态发生变化时，例如 `readyReplicas` 增加，其 `ResourceVersion` 会变化，所属 Website 被重新调谐并更新 status。用户手动修改 Deployment 或 Service 时，同样会触发调谐，Controller 会把它们恢复到 Website spec 描述的期望状态。

#### Delete：OnDelete

```go
func (h *OwnedResourceHandler) OnDelete(obj interface{}) {
    if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
        obj = tombstone.Obj
    }
    h.enqueueOwner(obj)
}
```

Informer 收到删除通知时，对象可能已经不在本地缓存中。此时传入的不是原对象，而是 `DeletedFinalStateUnknown` tombstone。代码先从 tombstone 中取出最后一次观察到的对象，再读取 OwnerReference。

只要 Website 仍然存在，删除其 Deployment 或 Service 就会触发重新调谐，缺失的资源会被重新创建。

#### enqueueOwner：从从属资源定位 Website

```go
func (h *OwnedResourceHandler) enqueueOwner(obj interface{}) {
    object, err := meta.Accessor(obj)
    if err != nil {
        log.Printf("failed to read child object metadata: %v", err)
        return
    }

    owner := metav1.GetControllerOf(object)
    if owner == nil ||
        owner.APIVersion != appsv1alpha1.SchemeGroupVersion.String() ||
        owner.Kind != "Website" {
        return
    }

    key, err := cache.MetaNamespaceKeyFunc(&metav1.PartialObjectMetadata{
        ObjectMeta: metav1.ObjectMeta{
            Namespace: object.GetNamespace(),
            Name:      owner.Name,
        },
    })
    if err != nil {
        log.Printf("failed to build owner Website key: %v", err)
        return
    }
    h.queue.Add(key)
}
```

处理步骤：

1. 使用 `meta.Accessor` 读取对象元数据。
2. 使用 `metav1.GetControllerOf` 获取 `controller=true` 的 OwnerReference。
3. 检查 owner 的 API Version 和 Kind，避免处理不属于 Website 的资源。
4. 使用从属资源的 namespace 和 owner name 构造 Website key。
5. 将 Website key 放入同一个 workqueue。

因此，无论事件源是 Website、Deployment 还是 Service，队列中的 key 始终表示一个 Website。`syncHandler` 只需要处理一种 key 格式和一种顶层资源。

## 事件与调谐行为总结

| 事件 | 入队对象 | 调谐结果 |
| --- | --- | --- |
| Website create | Website | 创建 Deployment 和 Service，初始化 status |
| Website update | Website | 同步镜像、副本数和端口，更新 status |
| Website delete | Website | Lister 返回 NotFound；从属资源由 GC 清理 |
| Deployment create/update | 所属 Website | 根据 ready replicas 更新 Website status，并修正配置漂移 |
| Deployment delete | 所属 Website | 重新创建 Deployment |
| Service create/update | 所属 Website | 检查并修正 Service 配置 |
| Service delete | 所属 Website | 重新创建 Service |

## 运行和测试

运行测试：

```bash
go test ./...
```

安装 CRD：

```bash
kubectl apply -f config/crd/bases/apps.clientgo-learning.io_websites.yaml
```

部署 Controller 前，需要把 `deploy/website-controller.yaml` 中的镜像替换成实际镜像，然后执行：

```bash
kubectl apply -f deploy/website-controller.yaml
kubectl apply -f config/samples/apps_v1alpha1_website.yaml
```

查看资源和状态：

```bash
kubectl get websites,deployments,services -n default
kubectl get website demo-website -n default -o yaml
```
