# Pod Crash Event Controller

这个目录是一个简单的 client-go informer/controller 教程项目。应用会监听 Kubernetes 集群里的 Pod crash 相关 Event，并通过 Amazon SNS 发送邮件通知。

整体流程如下：

1. `main.go` 初始化 Kubernetes client 和 AWS SNS client。
2. `main.go` 创建 `SharedInformerFactory`，并拿到 `core/v1 Event` informer。
3. `handler.go` 中的 informer 回调函数筛选 Pod crash Event，并把 Event key 放入 workqueue。
4. `controller.go` 中的 controller worker 从 workqueue 取 key。
5. controller 通过 lister 从 informer cache 读取 Event。
6. controller 再次确认这是 Pod crash Event，然后调用 SNS `Publish`。
7. SNS topic 的 email subscription 收到邮件。

## 目录结构

```text
chapter2/
  controller.go       # controller、workqueue、syncHandler、SNS publish
  controller_test.go  # Pod crash Event 判断逻辑测试
  handler.go          # informer Add/Update/Delete 回调
  main.go             # AWS client 和 Kubernetes client 初始化
  podcrash.yaml       # 触发 CrashLoopBackOff 的测试 Pod
  Dockerfile          # 容器镜像构建文件
  deploy/
    pod-crash-controller.yaml # Deployment、RBAC、ServiceAccount 模板
```

## main.go

`main.go` 是程序入口，主要负责初始化外部依赖。

### AWS SNS client

```go
awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
snsClient := sns.NewFromConfig(awsConfig)
```

这里使用 AWS SDK for Go v2 的默认凭证链。程序在本地运行时可以使用环境变量、AWS profile、SSO 等方式获取凭证；在 EKS 中推荐使用 IRSA，把 IAM role 绑定到 ServiceAccount。

程序要求 `SNS_TOPIC_ARN` 环境变量存在：

```go
topicARN := os.Getenv("SNS_TOPIC_ARN")
if topicARN == "" {
    log.Fatal("SNS_TOPIC_ARN is required")
}
```

SNS topic 本身负责邮件订阅。代码只向 topic publish 消息，不直接管理 email subscription。

### Kubernetes client

项目按要求使用如下初始化逻辑：

```go
config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
if err != nil {
    restConfig, err := rest.InClusterConfig()
    if err != nil {
        log.Fatal(err)
    }
    config = restConfig
}

clientset, err := kubernetes.NewForConfig(config)
```

含义是：

- 本地运行时优先读取默认 kubeconfig：`~/.kube/config`
- 在集群内运行时，如果读取 kubeconfig 失败，则使用 Pod 内挂载的 ServiceAccount token

### 创建 Event informer

```go
informerFactory := informers.NewSharedInformerFactoryWithOptions(
    clientset,
    30*time.Second,
    informers.WithNamespace(namespace),
)
eventInformer := informerFactory.Core().V1().Events()
```

这个 informer 监听 Kubernetes `core/v1 Event`。如果 `WATCH_NAMESPACE` 为空，则监听所有 namespace；如果设置为某个 namespace，则只监听该 namespace。

## handler.go

`handler.go` 里单独定义了 informer 回调函数，没有使用匿名函数：

```go
func (h *EventHandler) OnAdd(obj interface{})
func (h *EventHandler) OnUpdate(oldObj, newObj interface{})
func (h *EventHandler) OnDelete(obj interface{})
```

回调函数的职责很克制：只判断这个 Event 是否值得处理，然后把 key 加入队列。

```go
key, err := cache.MetaNamespaceKeyFunc(event)
h.queue.Add(key)
```

这里的 key 通常长这样：

```text
default/crash-demo.184f4f7fb8f9d0b1
```

这一步不要直接调用 AWS SNS。informer 回调应该尽量快，复杂逻辑交给 controller worker 异步处理。这样当 Kubernetes event 很多时，informer 不会被慢操作卡住。

## controller.go

`controller.go` 是这个教程里最核心的部分。

### Controller 结构

```go
type Controller struct {
    kubeclientset kubernetes.Interface
    snsClient     snsAPI
    topicARN      string

    eventLister corelisters.EventLister
    eventSynced cache.InformerSynced
    queue       workqueue.TypedRateLimitingInterface[string]
}
```

重要字段：

- `eventLister`：从 informer cache 读取 Event，不直接请求 apiserver
- `eventSynced`：确认 informer cache 已经完成初始同步
- `queue`：workqueue，用来削峰、重试和解耦 informer 回调与实际处理逻辑
- `snsClient`：SNS publish 客户端

### 注册回调

```go
func (c *Controller) AddEventHandlers(eventInformer cache.SharedIndexInformer) error {
    handler := NewEventHandler(c.queue)
    _, err := eventInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
        AddFunc:    handler.OnAdd,
        UpdateFunc: handler.OnUpdate,
        DeleteFunc: handler.OnDelete,
    })
    return err
}
```

这里把 `handler.go` 中的具名方法注册给 informer。Event 命中条件后进入 queue，controller 再处理。

### controller 运行流程

```go
if ok := cache.WaitForCacheSync(ctx.Done(), c.eventSynced); !ok {
    return fmt.Errorf("failed to wait for caches to sync")
}

for i := 0; i < workers; i++ {
    go wait.UntilWithContext(ctx, c.runWorker, time.Second)
}
```

controller 启动时先等待 informer cache 同步。同步完成之后再启动 worker，避免 worker 从 lister 中读不到刚刚 list 到的对象。

### workqueue 处理逻辑

```go
key, shutdown := c.queue.Get()
defer c.queue.Done(key)

if err := c.syncHandler(ctx, key); err != nil {
    c.queue.AddRateLimited(key)
    return true
}

c.queue.Forget(key)
```

这就是 Kubernetes controller 常见模式：

- `Get` 从队列取一个 key
- `Done` 标记本次处理结束
- 出错时 `AddRateLimited`，让队列按退避策略重试
- 成功时 `Forget`，清除这个 key 的重试状态

### syncHandler

```go
namespace, name, err := cache.SplitMetaNamespaceKey(key)
event, err := c.eventLister.Events(namespace).Get(name)
```

`syncHandler` 通过 key 找到 Event，然后再次调用 `isPodCrashEvent` 确认。二次确认很有必要，因为对象在进入队列后可能已经变化或被删除。

### Pod crash 判断

```go
return reason == "backoff" ||
    strings.Contains(reason, "crash") ||
    strings.Contains(message, "crashloopbackoff") ||
    strings.Contains(message, "back-off restarting failed container")
```

当前项目把下面几类 Event 视为 Pod crash：

- `Reason=BackOff`
- reason 中包含 `crash`
- message 中包含 `CrashLoopBackOff`
- message 中包含 `Back-off restarting failed container`

同时还要求：

- `InvolvedObject.Kind == "Pod"`
- `Type == "Warning"`

## 本地运行

先准备 SNS topic，并确认 topic 中已经有 email subscription。

```bash
export SNS_TOPIC_ARN=arn:aws:sns:us-east-1:123456789012:pod-crash-alerts
export AWS_REGION=us-east-1

# 可选：只监听 default namespace
export WATCH_NAMESPACE=default

go run .
```

然后在另一个终端创建测试 Pod：

```bash
kubectl apply -f podcrash.yaml
kubectl get events --sort-by=.lastTimestamp
```

`podcrash.yaml` 中的容器会启动后立刻退出，随后 Kubernetes 会生成 BackOff/CrashLoopBackOff 相关 Event，controller 会把通知发到 SNS topic。

清理测试 Pod：

```bash
kubectl delete -f podcrash.yaml
```

## 构建镜像

在 `chapter2` 目录下构建：

```bash
docker build -t your-registry/pod-crash-controller:latest .
docker push your-registry/pod-crash-controller:latest
```

然后把 `deploy/pod-crash-controller.yaml` 里的镜像地址替换成你自己的镜像。

## 部署到 Kubernetes

模板文件在：

```text
deploy/pod-crash-controller.yaml
```

应用前需要修改：

- `eks.amazonaws.com/role-arn`：替换为允许 publish SNS 的 IAM role ARN
- `SNS_TOPIC_ARN`：替换为你的 SNS topic ARN
- `AWS_REGION`：替换为 topic 所在 region
- `image`：替换为你构建并推送的镜像
- `WATCH_NAMESPACE`：为空表示监听所有 namespace；设置具体 namespace 表示只监听该 namespace

应用：

```bash
kubectl apply -f deploy/pod-crash-controller.yaml
```

查看日志：

```bash
kubectl -n pod-crash-controller logs deploy/pod-crash-controller
```

## IAM 权限示例

如果使用 EKS IRSA，绑定给 ServiceAccount 的 IAM role 至少需要允许 publish 到目标 SNS topic：

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "sns:Publish",
      "Resource": "arn:aws:sns:us-east-1:123456789012:pod-crash-alerts"
    }
  ]
}
```

## RBAC 说明

controller 当前监听 `core/v1 events`，所以 RBAC 只需要：

```yaml
resources:
  - events
verbs:
  - get
  - list
  - watch
```

模板使用 `ClusterRole` 和 `ClusterRoleBinding`，因为默认支持监听所有 namespace。如果只监听一个 namespace，可以改成 namespaced `Role` 和 `RoleBinding`。

## 测试

运行单元测试：

```bash
go test ./...
```

当前测试覆盖 `isPodCrashEvent`，用来确认只会处理 Pod 的 Warning crash/backoff Event，不会误处理普通 Event 或其他资源的 Event。
