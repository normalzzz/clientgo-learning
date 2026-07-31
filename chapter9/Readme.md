# KB：使用 OwnerReference 管理 Kubernetes 资源生命周期

本文以 chapter9 中的 Deployment 和 ConfigMap 为例，说明 Kubernetes 如何通过
`metadata.ownerReferences` 表达资源的所有权关系，以及 Garbage Collector（GC）
如何根据这种关系完成级联删除。文末还会说明 OwnerReference 在 Controller
调谐中的作用、适用边界，以及它与 Finalizer 的区别。

官方文档：

- [Owners and Dependents](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/)
- [Use Cascading Deletion in a Cluster](https://kubernetes.io/docs/tasks/administer-cluster/use-cascading-deletion/)
- [metav1.OwnerReference](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1#OwnerReference)

## 1. OwnerReference 解决什么问题

Kubernetes 对象可以在自己的 `metadata.ownerReferences` 中记录一个或多个 Owner。
记录引用的对象称为 **Dependent（从属资源）**，被引用的对象称为
**Owner（所有者）**。

OwnerReference 写在从属资源上，方向不要写反：

```text
Deployment/netshoot
    │
    ├── owns ──> ReplicaSet/netshoot-xxxxx
    │                 └── owns ──> Pod/netshoot-xxxxx-yyyyy
    │
    └── owns ──> ConfigMap/netshoot-config

实际字段位置：

ReplicaSet.metadata.ownerReferences ──> Deployment
Pod.metadata.ownerReferences        ──> ReplicaSet
ConfigMap.metadata.ownerReferences  ──> Deployment
```

当 Owner 被删除时，运行在 `kube-controller-manager` 中的
`generic-garbage-collector` 根据所有权图处理它的 Dependents。Controller
因此不必为每个集群内子资源都编写一套简单的删除逻辑。

OwnerReference 表达的是**所有权和生命周期关系**，不是通用依赖关系：

- 删除 Owner 可以触发从属资源回收；
- 删除 Dependent 不会影响 Owner；
- 它不会保证创建顺序、启动顺序或就绪顺序；
- 它不会自动把从属资源事件发送给自定义 Controller；
- 它也不适合直接清理云负载均衡器、DNS 记录等集群外资源。

## 2. OwnerReference 字段语义

chapter9 创建出的 ConfigMap 包含如下元数据：

```yaml
metadata:
  name: netshoot-config
  namespace: default
  ownerReferences:
    - apiVersion: apps/v1
      kind: Deployment
      name: netshoot
      uid: 68d10066-4caa-4b77-94d4-b64f7382cc0f
      controller: false
      blockOwnerDeletion: true
```

各字段含义如下：

| 字段 | 是否必需 | 含义 |
| --- | --- | --- |
| `apiVersion` | 是 | Owner 所属 API group 和 version，例如 `apps/v1` |
| `kind` | 是 | Owner 的资源类型，例如 `Deployment` |
| `name` | 是 | Owner 的名称 |
| `uid` | 是 | Owner 实例的唯一身份 |
| `controller` | 否 | 是否把该 Owner 标记为管理此对象的 Controller |
| `blockOwnerDeletion` | 否 | 前台级联删除时，该 Dependent 是否阻止 Owner 最终消失 |

### UID 为什么不可省略

GC 识别的是一个具体的对象实例，而不是一个可重复使用的名字。即使删除
`Deployment/netshoot` 后又创建了同名 Deployment，新对象也会获得不同的 UID。
旧 ConfigMap 的 OwnerReference 不会因此错误地指向新对象。

因此不要只拼接 Owner 的名称，也不要自行生成 UID。应先从 API Server 获取
Owner，再使用它的真实 `metadata.uid` 构造引用。

### controller 的含义

一个对象可以有多个 OwnerReference，但最多只能有一个引用设置
`controller=true`。该字段表示“这个 Owner 是管理当前对象的 Controller”，常用于
Controller：

- 判断现有资源是否由自己管理；
- 从从属资源的事件反查并重新调谐 Owner；
- 避免接管已经被其他 Controller 控制的资源。

`controller` 不是启用 GC 的开关。即使 chapter9 设置的是
`controller=false`，OwnerReference 仍会被 GC 识别，ConfigMap 仍可随 Deployment
级联删除。

### blockOwnerDeletion 的含义

`blockOwnerDeletion=true` 主要影响 **Foreground（前台）删除**。当 GC 已观察到
这个 Dependent 时，Owner 会保持在 terminating 状态，直到该 Dependent 被处理
完成。它不会让 Dependent 永远不可删除，也不影响普通的单独删除操作。

设置或修改该字段要求调用方对 Owner 具有删除权限，否则 API Server 会返回
`422 Unprocessable Entity`。这样普通用户不能随意创建一个引用并阻止无权管理的
Owner 被删除。

## 3. Owner 与 Dependent 的作用域约束

OwnerReference 不包含 namespace 字段，因此引用必须遵守 Kubernetes 的作用域规则：

- namespace-scoped Dependent 可以引用**同一 namespace** 中的
  namespace-scoped Owner；
- namespace-scoped Dependent 可以引用 cluster-scoped Owner；
- cluster-scoped Dependent 只能引用 cluster-scoped Owner；
- 不允许跨 namespace 引用 namespace-scoped Owner。

例如，`default/netshoot-config` 可以引用 `default/netshoot`，但不能引用
`production/netshoot`。非法引用会被视为无法解析的 Owner，并可能导致 Dependent
被 GC 回收；排障时可从事件中看到 `OwnerRefInvalidNamespace`。

## 4. chapter9 的 client-go 实现

chapter9 的程序先读取已存在的 `default/netshoot` Deployment，再创建以它为 Owner
的 ConfigMap：

```go
deployment, err := clientset.AppsV1().
    Deployments("default").
    Get(ctx, "netshoot", metav1.GetOptions{})
if err != nil {
    panic(err)
}

ownerRef := metav1.OwnerReference{
    APIVersion:         "apps/v1",
    Kind:               "Deployment",
    Name:               deployment.Name,
    UID:                deployment.UID,
    Controller:         boolPtr(false),
    BlockOwnerDeletion: boolPtr(true),
}

configMap := &corev1.ConfigMap{
    ObjectMeta: metav1.ObjectMeta{
        Name:            "netshoot-config",
        Namespace:       "default",
        OwnerReferences: []metav1.OwnerReference{ownerRef},
    },
    Data: map[string]string{
        "config.yaml": "key: value",
    },
}

created, err := clientset.CoreV1().
    ConfigMaps("default").
    Create(ctx, configMap, metav1.CreateOptions{})
if err != nil {
    panic(err)
}
```

这里先执行 `Get` 有两个目的：

1. 确认 Owner 确实存在；
2. 读取 API Server 分配的真实 UID。

示例把 `Controller` 显式设置为 `false`，用于演示“普通 OwnerReference 同样参与
GC”。如果 ConfigMap 是某个自定义 Controller 唯一管理的从属资源，通常应使用
controller OwnerReference。

### 使用 NewControllerRef

已知 Owner 的 GVK 时，可以使用 `metav1.NewControllerRef` 减少手工填写字段：

```go
ownerRef := *metav1.NewControllerRef(
    deployment,
    appsv1.SchemeGroupVersion.WithKind("Deployment"),
)
```

该函数创建的引用默认包含：

```yaml
controller: true
blockOwnerDeletion: true
```

chapter4 的 Website Controller 就使用了这种方式：

```go
func websiteControllerRef(
    website *appsv1alpha1.Website,
) metav1.OwnerReference {
    return *metav1.NewControllerRef(
        website,
        appsv1alpha1.SchemeGroupVersion.WithKind("Website"),
    )
}
```

### 使用 controllerutil

使用 controller-runtime 的项目也可以通过以下辅助函数设置所有权：

```go
err := controllerutil.SetControllerReference(owner, dependent, scheme)
```

它会根据注册到 `scheme` 中的类型推导 GVK，并检查 Dependent 是否已经由另一个
Controller 管理。如果只需要添加一个非 Controller Owner，可使用
`controllerutil.SetOwnerReference`。

chapter9 当前只依赖 client-go，没有引入 controller-runtime，因此示例采用
`metav1.OwnerReference`。不应仅为了设置一个字段而强制增加额外依赖。

## 5. 三种级联删除策略

客户端通过 `DeleteOptions.propagationPolicy` 指定删除传播策略。`kubectl` 对应
`--cascade=background|foreground|orphan`。

| 策略 | Owner 何时消失 | Dependent 如何处理 | 典型场景 |
| --- | --- | --- | --- |
| Background | API Server 先删除 Owner | GC 随后异步删除 Dependents | 常规快速删除 |
| Foreground | 阻塞项清理完成后 | GC 先处理设置了 `blockOwnerDeletion=true` 的 Dependents | 需要观察完整级联过程 |
| Orphan | Owner 被删除 | Dependents 保留，并移除对该 Owner 的引用 | 希望保留或迁移子资源 |

一个 Dependent 可以有多个 Owner。在非 Orphan 删除中，只要仍有至少一个 Owner
存在，GC 就不会因为其他 Owner 消失而删除它；只有所有 Owner 都不存在时，它才成为
待回收对象。Orphan 策略会先移除相应引用，因此保留下来的对象不会因为该 Owner
消失而被回收。

### 5.1 Background：后台级联删除

```bash
kubectl delete deployment netshoot \
  --namespace default \
  --cascade=background
```

过程如下：

```text
DELETE Deployment（PropagationPolicy=Background）
    │
    ├── API Server 删除 Deployment
    │
    └── GC 异步发现 Dependents
            ├── 删除 ReplicaSet，进而删除 Pod
            └── 删除 ConfigMap
```

删除命令返回时，Owner 通常已经不可见，但 ConfigMap、ReplicaSet 或 Pod 可能暂时
仍能查询到。这是后台异步回收的正常现象，不代表 OwnerReference 失效。

chapter9 的审计日志验证了这一点：用户先提交 Deployment 的 Background 删除，
随后 `system:serviceaccount:kube-system:generic-garbage-collector` 分别删除
ReplicaSet、Pod 和 `netshoot-config`。

### 5.2 Foreground：前台级联删除

为了有机会观察中间状态，可以让 kubectl 提交请求后立即返回：

```bash
kubectl delete deployment netshoot \
  --namespace default \
  --cascade=foreground \
  --wait=false
```

过程如下：

```text
DELETE Deployment（PropagationPolicy=Foreground）
    │
    ▼
Deployment.metadata.deletionTimestamp = <time>
Deployment.metadata.finalizers += ["foregroundDeletion"]
    │
    ▼
GC 删除需要阻塞 Owner 删除的 Dependents
    │
    ▼
GC 移除 foregroundDeletion
    │
    ▼
API Server 完成 Deployment 删除
```

可以尝试在 GC 完成前查看：

```bash
kubectl get deployment netshoot \
  --namespace default \
  -o jsonpath='{.metadata.deletionTimestamp}{"\n"}{.metadata.finalizers}{"\n"}'
```

如果 Dependents 很快被删除，Deployment 可能已经查询不到。此时应结合
`kubectl get --watch` 或 API Server audit 日志观察过程。

chapter9 的审计日志中，Deployment 删除响应带有 `deletionTimestamp` 和
`foregroundDeletion`。GC 删除 ConfigMap、ReplicaSet 和 Pod 后，通过更新
Deployment 移除该 Finalizer；对象仍有 `deletionTimestamp` 且 Finalizer 已清空
时，API Server 自动完成删除。

这里的 `foregroundDeletion` 是 Kubernetes GC 为前台级联删除维护的系统
Finalizer，不是业务 Controller 自定义的清理逻辑。

### 5.3 Orphan：孤儿删除

```bash
kubectl delete deployment netshoot \
  --namespace default \
  --cascade=orphan
```

该策略只删除 Owner，保留直接 Dependents。GC 会移除 Dependents 中指向已删除
Owner 的引用：

```text
Deployment/netshoot 被删除
    ├── ReplicaSet 保留，移除指向 Deployment 的 OwnerReference
    │       └── Pod 仍由 ReplicaSet 所有
    └── ConfigMap 保留，移除指向 Deployment 的 OwnerReference
```

验证资源仍然存在：

```bash
kubectl get configmap netshoot-config --namespace default
kubectl get replicaset --namespace default
```

再检查 ConfigMap 的引用：

```bash
kubectl get configmap netshoot-config \
  --namespace default \
  -o jsonpath='{.metadata.ownerReferences}{"\n"}'
```

孤儿处理完成后，这个 ConfigMap 不再随原 Deployment 的生命周期变化。以后创建
同名 Deployment 也不会自动重新获得它的所有权。

### 使用 client-go 指定删除策略

client-go 中的三种策略分别对应以下常量：

```go
policy := metav1.DeletePropagationBackground
// policy := metav1.DeletePropagationForeground
// policy := metav1.DeletePropagationOrphan

err := clientset.AppsV1().
    Deployments("default").
    Delete(ctx, "netshoot", metav1.DeleteOptions{
        PropagationPolicy: &policy,
    })
```

显式指定 `PropagationPolicy` 可以让代码意图更清晰，也避免依赖客户端或资源的
默认行为。

## 6. 完整实验步骤

以下命令均在 `chapter9` 目录执行，并假设当前 kubeconfig 有权在 `default`
namespace 创建、读取和删除 Deployment 与 ConfigMap，并能读取实验产生的
ReplicaSet 和 Pod。

### 6.1 创建 Owner

```bash
kubectl create deployment netshoot \
  --namespace default \
  --image=nicolaka/netshoot \
  -- sleep infinity
```

确认 Deployment 已获得 UID：

```bash
kubectl get deployment netshoot \
  --namespace default \
  -o custom-columns='NAME:.metadata.name,UID:.metadata.uid'
```

### 6.2 创建 Dependent

```bash
go run .
```

预期输出：

```text
Created ConfigMap netshoot-config with owner reference
```

查看 OwnerReference：

```bash
kubectl get configmap netshoot-config \
  --namespace default \
  -o yaml
```

也可以只检查关键字段：

```bash
kubectl get configmap netshoot-config \
  --namespace default \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.apiVersion}{" "}{.kind}{" "}{.name}{" "}{.uid}{"\n"}{end}'
```

将输出的 UID 与 Deployment UID 对比，两者应完全一致。

> `main.go` 使用 `Create` 而不是幂等的 create-or-update。重复执行前应删除已有
> ConfigMap，否则 API Server 会返回 `AlreadyExists`。

### 6.3 分别验证删除策略

每轮实验都需要重新创建 Deployment，再执行 `go run .` 创建新的 ConfigMap，以便
写入新 Deployment 的 UID。然后选择一种策略：

```bash
kubectl delete deployment netshoot --namespace default --cascade=background
kubectl delete deployment netshoot --namespace default --cascade=foreground
kubectl delete deployment netshoot --namespace default --cascade=orphan
```

使用 watch 观察资源变化：

```bash
kubectl get deployment,replicaset,pod,configmap \
  --namespace default \
  --watch
```

如果上一轮使用 Orphan 策略，残留的 ConfigMap 和 ReplicaSet 需要单独处理，否则会
与下一轮同名资源冲突。

## 7. chapter4 中的实际应用

chapter4 的 Website Controller 创建 Deployment 和 Service 时，将 Website 设置为
controller Owner：

```go
ObjectMeta: metav1.ObjectMeta{
    Name:            website.Name,
    Namespace:       website.Namespace,
    Labels:          labels,
    OwnerReferences: []metav1.OwnerReference{
        websiteControllerRef(website),
    },
},
```

因此 Website 删除后，Deployment 和 Service 会由 GC 级联删除，Controller 无需在
`Website NotFound` 分支里再次手动删除它们。

OwnerReference 在这里还有第二个作用：Deployment 或 Service 的 Informer 收到事件
后，事件处理器可以通过 `metav1.GetControllerOf` 找到 Website，并将 Website 的
`namespace/name` 放回 workqueue：

```go
owner := metav1.GetControllerOf(object)
if owner == nil ||
    owner.APIVersion != appsv1alpha1.SchemeGroupVersion.String() ||
    owner.Kind != "Website" {
    return
}
```

这样就形成双向协作：

```text
Website 调谐 ──创建/更新──> Deployment、Service
      ▲                            │
      └──── 从属资源事件重新入队 ────┘

Website 删除 ──OwnerReference/GC──> 回收 Deployment、Service
```

需要注意，OwnerReference 只是提供了反查信息。要让从属资源变化触发调谐，
Controller 仍然必须显式注册 Deployment 和 Service 的 Informer/Watch 事件处理器。

## 8. OwnerReference 与 Finalizer 如何选择

两者都参与资源生命周期，但解决的问题不同：

| 机制 | 字段位置 | 谁执行动作 | 适合处理 |
| --- | --- | --- | --- |
| OwnerReference | Dependent 的 `metadata.ownerReferences` | Kubernetes GC | Owner 删除后回收集群内从属对象 |
| Finalizer | 被删除对象的 `metadata.finalizers` | 负责该键的 Controller | 删除前的业务清理、外部资源释放、等待异步操作 |

选择原则：

- 子资源完全由 Kubernetes API 对象构成，随 Owner 一起删除即可：优先使用
  OwnerReference；
- 删除前需要备份数据、调用外部 API 或确认异步清理完成：使用 Finalizer；
- 既要释放外部资源，又要级联删除集群内子资源：两者可以同时使用。

不要把 OwnerReference 当作 Finalizer。它不能运行自定义清理代码，也不能表达“先
执行某个业务步骤，再允许 Owner 消失”。

## 9. 常见问题与排障

### Owner 删除后 Dependent 没有立刻消失

Background 删除是异步的，短暂残留是正常现象。先等待并观察：

```bash
kubectl get configmap netshoot-config --namespace default --watch
```

如果持续存在，再检查 OwnerReference 的 `apiVersion`、`kind`、`name` 和 `uid`
是否与 Owner 完全一致。

### Foreground 删除一直卡在 Terminating

检查 Owner 的 Finalizer 和所有 Dependents：

```bash
kubectl get deployment netshoot --namespace default -o yaml
kubectl get configmap netshoot-config --namespace default -o yaml
```

常见原因包括：

- Dependent 自己还有无法完成的 Finalizer；
- GC 缺少发现或删除某类资源所需的权限；
- API discovery 或聚合 API 异常；
- 无效的跨 namespace OwnerReference；
- Controller 持续重建正在被 GC 删除的资源。

不要把“手工移除 Finalizer”作为第一选择。应先定位负责清理的一方为何没有完成
工作，否则可能留下泄漏资源。

### 设置了 OwnerReference，但从属资源事件没有触发调谐

这是预期行为。OwnerReference 不会自动建立 Watch。需要为从属资源注册 Informer
事件处理器，并从 `controller=true` 的引用构造 Owner key。

### ConfigMap 已经由另一个 Controller 管理

不要直接覆盖现有的 controller OwnerReference。一个对象最多只能有一个
`controller=true` 的 Owner。应返回冲突错误、使用不同名称创建资源，或设计明确的
资源接管流程。

### OwnerReference 设置正确，但 Owner 被重建过

比较 UID，而不只是名称：

```bash
kubectl get deployment netshoot \
  --namespace default \
  -o jsonpath='{.metadata.uid}{"\n"}'

kubectl get configmap netshoot-config \
  --namespace default \
  -o jsonpath='{.metadata.ownerReferences[0].uid}{"\n"}'
```

同名 Owner 被删除并重建后 UID 会变化。此时应由 Controller 在确认接管安全后更新
Dependent，而不是假定同名对象就是原 Owner。

## 10. 实践建议

在自定义 Controller 中使用 OwnerReference 时，建议遵循以下规则：

1. 从 API Server 或 Informer 缓存中的真实 Owner 对象读取 UID。
2. controller Owner 优先使用 `metav1.NewControllerRef` 或
   `controllerutil.SetControllerReference` 构造，减少字段拼写错误。
3. 创建或接管已有资源前，检查它是否已经由其他 Controller 管理。
4. 严格遵守 namespace 与 cluster scope 的引用规则。
5. 在删除调用中显式指定 `PropagationPolicy`，使行为可读、可测试。
6. 从属资源调谐和清理逻辑保持幂等，能够安全处理重复事件和异步 GC。
7. 集群外资源使用 Finalizer 清理，不要期待 GC 处理 Kubernetes API 之外的对象。

OwnerReference 的核心价值可以概括为：**把资源之间的所有权关系声明在对象元数据
中，让 Controller 专注于期望状态调谐，让 Kubernetes GC 负责通用的级联回收。**
