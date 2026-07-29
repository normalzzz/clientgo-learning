# Chapter 10：用三个 Controller 对比 workqueue 的重试节奏

这个示例使用标准的 client-go Controller 链路：

```text
ConfigMap Informer
        │ 同一个 namespace/name key
        ├────────> immediate queue ──> worker ──> Reconcile
        ├────────> delaying queue  ──> worker ──> Reconcile
        └────────> rate-limit queue──> worker ──> Reconcile
```

三个 Controller 共享同一个 ConfigMap Informer、Lister、事件起始时间和
`FailureReconciler`。唯一变化是失败后把 key 重新放回队列的方式，因此日志中的
时间差可以归因于 queue 策略。

## 演示场景

Controller 只监听带有标签 `workqueue.demo/enabled=true` 的 ConfigMap。
注解 `workqueue.demo/failures` 指定每个 Controller 在同一个
`resourceVersion` 上需要模拟失败多少次。示例值为 `4`，即第 1～4 次调谐返回
错误，第 5 次成功。

三个队列分别使用：

| Controller | client-go 队列 | 失败后的操作 | 默认重试间隔 |
| --- | --- | --- | --- |
| `immediate` | `workqueue.TypedInterface[string]` | `Add(key)` | `0, 0, 0, 0`，形成 hot loop |
| `delaying` | `workqueue.TypedDelayingInterface[string]` | `AddAfter(key, delay)` | `1s, 1s, 1s, 1s` |
| `rate-limit` | `workqueue.TypedRateLimitingInterface[string]` | `AddRateLimited(key)` | `250ms, 500ms, 1s, 2s` |

限速队列使用
`NewTypedItemExponentialFailureRateLimiter(baseDelay, maxDelay)`，所以每个 key
会独立进行指数退避。成功或超过最大重试次数时，三个 Controller 都调用
`Forget(key)` 清理重试状态。

普通队列本身也具有去重和“同一个 key 不并发处理”的语义，但它没有失败退避；
直接 `Add` 会让持续失败的请求马上再次占用 worker。延迟队列能避免 hot loop，
但不了解失败次数。限速队列会随着连续失败逐步降低请求频率。

## 运行

要求 Go 1.25 和一个当前 kubeconfig 可访问的 Kubernetes 集群。Controller
只需要目标 namespace 中 ConfigMap 的 `list`、`watch`、`get` 权限。

先启动 Controller：

```bash
cd chapter10
go run .
```

另一个终端创建演示请求：

```bash
kubectl apply -f chapter10/config/demo-configmap.yaml
```

如果已经存在该 ConfigMap，可以修改请求来产生新的 `resourceVersion`，并重置
三个 Controller 的模拟失败计数：

```bash
kubectl annotate configmap workqueue-demo \
  workqueue.demo/failures=5 --overwrite
```

也可以直接修改 `data.request`：

```bash
kubectl patch configmap workqueue-demo \
  --type merge -p '{"data":{"request":"run-2"}}'
```

## 如何读日志

日志以 Informer 事件的时刻作为统一的 `elapsed=0`：

```text
[SYSTEM    ] WATCH namespace="default" selector="workqueue.demo/enabled=true"
[IMMEDIATE ] START queue=workqueue_demo_immediate  type=ordinary      retry=immediate
[DELAYING  ] START queue=workqueue_demo_delaying   type=delaying      retry=fixed(1s)
[RATE-LIMIT] START queue=workqueue_demo_rate_limit type=rate-limiting retry=exponential(250ms..4s)
[EVENT     ] add    key=default/workqueue-demo rv=346621445 queues=3
[IMMEDIATE ] key=default/workqueue-demo try=1 elapsed=0s     result=RETRY   retry=1/6 next=0s
[RATE-LIMIT] key=default/workqueue-demo try=1 elapsed=0s     result=RETRY   retry=1/6 next=250ms
[DELAYING  ] key=default/workqueue-demo try=1 elapsed=0s     result=RETRY   retry=1/6 next=1s
[RATE-LIMIT] key=default/workqueue-demo try=2 elapsed=251ms  result=RETRY   retry=2/6 next=500ms
[DELAYING  ] key=default/workqueue-demo try=2 elapsed=1.001s result=RETRY   retry=2/6 next=1s
```

每次调谐只输出一行。三个 Controller 的日志通过同一把输出锁串行写入，避免并发
打印造成时间戳倒序。重点比较同一个 `key` 的 `elapsed`、`retry` 和 `next`：

- `immediate` 的五次处理几乎发生在同一毫秒。
- `delaying` 大约在第 0、1、2、3、4 秒处理。
- `rate-limit` 大约在第 0、0.25、0.75、1.75、3.75 秒处理。

实际时间会受 Go 调度和 API Server 延迟影响，`after` 是 queue 计划的等待时间，
`elapsed` 是 worker 真正开始处理时的累计时间。

## 可调参数

```bash
go run . \
  -namespace default \
  -workers 1 \
  -max-retries 6 \
  -fixed-delay 1s \
  -rate-base-delay 250ms \
  -rate-max-delay 4s
```

- `-workers` 是每个 Controller 的 worker 数；为了观察单 key 节奏，默认值 `1`
  最直观。
- `-max-retries` 不包含第一次处理。设为 `6` 表示最多处理 7 次。
- `-resync` 默认为 `0`，避免周期性 resync 干扰演示。
- `-kubeconfig` 和 `-master` 使用 client-go 标准配置加载方式；不传时使用默认
  kubeconfig 或 Pod 内的 ServiceAccount 配置。

## 测试

```bash
go test ./...
```

测试会用 10～40ms 的缩短参数真实运行三个 worker，并验证固定延迟和指数退避
产生了不同的调用间隔；不需要 Kubernetes 集群。

## 代码导航

- `main.go`：创建 Clientset、InformerFactory、三个 Controller 并启动它们。
- `handler.go`：接收 add/update/delete 事件，把同一个 key 扇出到三个 queue。
- `controller.go`：sample-controller 风格的 `Run -> worker ->
  processNextWorkItem -> Reconcile` 主链路。
- `queue.go`：普通、延迟、限速三种 typed workqueue 的适配与重试操作。
- `reconciler.go`：通过 ConfigMap Lister 读取请求并产生可重复的模拟错误。
- `tracker.go`：记录共享事件时间，使三组日志可直接比较。
- `logger.go`：串行化三个 Controller 的输出，保证日志时间顺序。
