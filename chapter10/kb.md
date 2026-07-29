# client-go Workqueue 与 Controller 重试机制

## Workqueue 在 Controller 中的作用

在 Kubernetes Controller 中，Informer 的事件回调通常不直接执行调谐逻辑。
回调函数只负责把对象转换为 `namespace/name` 形式的 key，再将 key 放入
workqueue。Controller 的 worker goroutine 从队列中取出 key，通过 Lister 或
Indexer 查询 Informer 本地缓存中的最新对象，最后执行调谐。

完整链路如下：

```text
API Server
    │ List / Watch
    ▼
Informer ──事件回调──> key ──Add──> Workqueue ──Get──> Worker
    │                                                    │
    └────────────── 本地缓存 <──── Lister / Indexer ─────┘
                                                         │
                                                         ▼
                                                     Reconcile
```

事件回调和调谐逻辑解耦后，主要有以下好处：

1. **削峰和异步处理**：Informer 可以继续分发事件，耗时调谐由 worker 异步完成。
2. **控制并发度**：通过 worker 数量控制同时执行的调谐任务数。
3. **对同一个 key 去重**：对象短时间内多次变化时，不必为每个事件保存一份完整对象。
4. **支持失败重试**：Controller 可以根据错误类型选择立即、延迟或限速重试。
5. **降低 API Server 压力**：worker 根据 key 从本地缓存读取最新对象，而不是为每个事件重新请求 API Server。

队列中保存 key 而不是对象还有一个重要原因：对象进入队列后可能再次发生变化。
worker 真正处理时通过 Lister 读取的是缓存中的最新状态，更符合 Controller
“不断把实际状态收敛到期望状态”的工作方式。

## 几种常见的队列：

client-go 的 `k8s.io/client-go/util/workqueue` 包提供了普通队列、延迟队列和
限速队列。新代码应优先使用带泛型的 typed 接口；`Interface`、
`DelayingInterface` 和 `RateLimitingInterface` 等非泛型接口已经被标记为
deprecated。

官方文档：
[k8s.io/client-go/util/workqueue](https://pkg.go.dev/k8s.io/client-go/util/workqueue)

三个接口是逐层扩展的：

```text
TypedInterface[T]
    │ 增加 AddAfter
    ▼
TypedDelayingInterface[T]
    │ 增加 AddRateLimited / Forget / NumRequeues
    ▼
TypedRateLimitingInterface[T]
```

| 队列 | typed 接口 | 重新入队方式 | 重试间隔由谁决定 | 典型用途 |
| --- | --- | --- | --- | --- |
| 普通队列 | `TypedInterface[T]` | `Add(item)` | 调用方；队列立即接收 | 普通事件缓冲、不需要退避的任务 |
| 延迟队列 | `TypedDelayingInterface[T]` | `AddAfter(item, delay)` | 调用方传入固定或动态时长 | 固定时间后检查、避免错误 hot loop |
| 限速队列 | `TypedRateLimitingInterface[T]` | `AddRateLimited(item)` | `TypedRateLimiter[T]` | Controller 失败重试、指数退避、整体限流 |

### Workqueue 共有的行为

三种队列都建立在普通 workqueue 的核心语义之上：

- **FIFO**：默认底层存储是基于 slice 的先进先出队列。
- **去重**：同一个 key 在等待处理期间被多次 `Add`，通常只需要处理一次。
- **同 key 不并发**：同一个 key 不会被多个 worker 同时处理。
- **处理期间可再次入队**：key 正在处理时又收到事件，会在当前处理完成后再处理一次。
- **支持多生产者、多消费者**：Informer 回调和多个 worker 可以安全地并发使用队列。
- **支持优雅关闭**：`ShutDown` 会唤醒阻塞在 `Get` 上的 worker，使其退出循环。

这里的“去重”不代表丢失状态变化。队列只负责保存调谐信号，worker 随后从
Informer 缓存读取对象的最新版本。

### 1. 普通队列：`TypedInterface[T]`

普通队列提供最基础的入队、出队和生命周期方法：

```go
type TypedInterface[T comparable] interface {
    Add(item T)
    Len() int
    Get() (item T, shutdown bool)
    Done(item T)
    ShutDown()
    ShutDownWithDrain()
    ShuttingDown() bool
}
```

创建一个带名称的普通队列：

```go
queue := workqueue.NewTypedWithConfig(
    workqueue.TypedQueueConfig[string]{
        Name: "workqueue_demo_immediate",
    },
)
```

普通队列的 `Add` 会让 key 尽快进入可消费状态，不会自动增加等待时间，也不会
记录该 key 已经失败多少次。因此，如果 worker 在每次失败后都立即调用
`Add(key)`，持续失败的 key 会形成 **hot loop**：

```text
Reconcile 失败 → Add(key) → 立即再次 Reconcile → 再次失败 → ...
```

这会持续占用 worker 和 CPU。如果调谐逻辑还会访问外部服务或 API Server，也
可能放大下游故障。因此，普通队列适合正常事件分发，但通常不适合没有保护措施的
错误重试。

chapter10 为了展示这种现象，在 `immediateQueue` 外层维护了一个
`retryCounter`。失败时先增加计数，再直接调用 `Add`：

```go
func (q *immediateQueue) Retry(key string) Retry {
    number := q.retries.increment(key)
    q.Add(key)
    return Retry{Number: number}
}
```

这个计数器是示例项目为了统一三个 Controller 的日志和最大重试判断而添加的，
不是 `TypedInterface[T]` 自带的能力。

### 2. 延迟队列：`TypedDelayingInterface[T]`

延迟队列在普通队列基础上增加了 `AddAfter`：

```go
type TypedDelayingInterface[T comparable] interface {
    TypedInterface[T]
    AddAfter(item T, duration time.Duration)
}
```

创建延迟队列：

```go
queue := workqueue.NewTypedDelayingQueueWithConfig(
    workqueue.TypedDelayingQueueConfig[string]{
        Name: "workqueue_demo_delaying",
    },
)
```

调用 `AddAfter(key, time.Second)` 后，key 要等待约 1 秒才会进入可消费队列。
它适用于：

- 失败后等待固定时间再重试；
- 等待外部系统完成异步操作；
- 定期检查尚未满足的条件；
- 避免普通队列立即重试造成 hot loop。

需要注意，延迟队列只负责“到指定时间后入队”，它本身不判断错误、不计算退避，
也不维护失败次数。固定延迟、线性增长或其他延迟算法都要由调用方决定。

chapter10 使用固定延迟：

```go
func (q *delayingQueue) Retry(key string) Retry {
    number := q.retries.increment(key)
    q.AddAfter(key, q.delay)
    return Retry{
        Number: number,
        After:  q.delay,
    }
}
```

默认 `q.delay` 为 1 秒，所以每次失败后的等待时间相同：

```text
1s → 1s → 1s → 1s
```

固定延迟可以保护 worker，但无法根据连续失败次数自动降低重试频率。

### 3. 限速队列：`TypedRateLimitingInterface[T]`

限速队列建立在延迟队列之上，并增加三个与重试状态相关的方法：

```go
type TypedRateLimitingInterface[T comparable] interface {
    TypedDelayingInterface[T]

    AddRateLimited(item T)
    Forget(item T)
    NumRequeues(item T) int
}
```

它需要搭配 `TypedRateLimiter[T]`。当 Controller 调用
`AddRateLimited(key)` 时，队列会通过 RateLimiter 的 `When(key)` 计算等待
时间，然后按该时间延迟入队。

chapter10 使用“按 key 指数退避”的 RateLimiter：

```go
limiter := workqueue.NewTypedItemExponentialFailureRateLimiter[string](
    250*time.Millisecond,
    4*time.Second,
)

queue := workqueue.NewTypedRateLimitingQueueWithConfig(
    limiter,
    workqueue.TypedRateLimitingQueueConfig[string]{
        Name: "workqueue_demo_rate_limit",
    },
)
```

指数退避的计算方式可以概括为：

```text
第 n 次重试的 delay = min(baseDelay × 2^(n-1), maxDelay)，n 从 1 开始
```

以 `baseDelay=250ms`、`maxDelay=4s` 为例，各次重试等待时间为：

```text
250ms → 500ms → 1s → 2s → 4s → 4s → ...
```

这里的计数按 key 独立维护。一个持续失败的 key 不会直接改变另一个 key 的按项
退避计数。

调谐成功或决定放弃该 key 时必须调用 `Forget(key)`：

```go
if err == nil {
    queue.Forget(key)
    return
}

if queue.NumRequeues(key) >= maxRetries {
    queue.Forget(key)
    return
}

queue.AddRateLimited(key)
```

如果遗漏 `Forget`，RateLimiter 会继续保存该 key 的失败状态。以后同一个 key
再次入队时，可能仍沿用之前的较长退避时间，同时造成状态长期占用内存。

`Forget` 和 `Done` 的职责不同：

| 方法 | 作用 |
| --- | --- |
| `Done(key)` | 告诉 workqueue：本次从 `Get` 取得的 key 已处理完毕 |
| `Forget(key)` | 告诉 RateLimiter：结束该 key 的重试周期并清除失败计数 |

使用限速队列时，成功路径通常需要同时执行 `Done` 和 `Forget`。chapter10 通过
`defer queue.Done(key)` 保证本次处理结束，并在成功或超过最大重试次数时调用
`Forget(key)`。

### 常见 RateLimiter

限速队列的实际节奏取决于传入的 RateLimiter，而不是
`TypedRateLimitingInterface` 本身。

| RateLimiter | 行为 | 适用场景 |
| --- | --- | --- |
| `NewTypedItemExponentialFailureRateLimiter` | 每个 key 按失败次数指数退避 | 常见的 Controller 错误重试 |
| `NewTypedItemFastSlowRateLimiter` | 前几次使用较短延迟，之后切换到较长延迟 | 希望先快速恢复、持续失败后降频 |
| `NewTypedMaxOfRateLimiter` | 同时计算多个策略，采用等待时间最长的结果 | 组合按 key 退避和全局限流 |
| `DefaultTypedControllerRateLimiter` | 组合按 key 指数退避与整体 token bucket | 通用 Controller 默认策略 |
| `NewTypedWithMaxWaitRateLimiter` | 为其他策略产生的等待时间设置上限 | 限制最坏情况下的等待时间 |

`DefaultTypedControllerRateLimiter` 同时考虑：

- **单个 key 的连续失败**：使用指数退避；
- **所有重试请求的总体速率**：使用 token bucket 控制突发流量。

chapter10 没有直接使用默认策略，而是只使用
`NewTypedItemExponentialFailureRateLimiter`。这样日志中的时间变化完全来自
单个 key 的指数退避，更适合教学对比。

### 三种队列的选择

可以按照下面的思路选择：

```text
是否需要延迟？
├── 否：普通队列
└── 是
    ├── 延迟由业务明确指定：延迟队列
    └── 延迟要根据失败次数或整体负载自动计算：限速队列
```

在生产 Controller 中，错误重试通常优先考虑限速队列。普通队列和延迟队列仍然
有价值，但需要调用方自行维护重试次数、最大重试上限和状态清理。

## chapter10 的对比场景

项目代码：
[github.com/normalzzz/clientgo-learning/tree/main/chapter10](https://github.com/normalzzz/clientgo-learning/tree/main/chapter10)

chapter10 启动一个 ConfigMap Informer 和三个 Controller：

```text
ConfigMap Informer
        │ 同一个 key、同一个事件起始时间
        ├──> immediate queue  ──> worker ──> FailureReconciler
        ├──> delaying queue   ──> worker ──> FailureReconciler
        └──> rate-limit queue ──> worker ──> FailureReconciler
```

Informer 只监听带有以下标签的 ConfigMap：

```yaml
labels:
  workqueue.demo/enabled: "true"
```

`EnqueueHandler` 使用 `DeletionHandlingMetaNamespaceKeyFunc` 生成 key，并把同一个
key 扇出到三个 Controller 的队列。`EventTracker` 为三者记录同一个事件开始
时间，因此日志中的 `elapsed` 可以直接横向比较。

### 如何产生可重复的失败

示例 ConfigMap 使用下面的注解：

```yaml
annotations:
  workqueue.demo/failures: "4"
```

`FailureReconciler` 按下面三个维度保存尝试次数：

```text
controller 名称 + namespace/name key + resourceVersion
```

因此：

- 三个 Controller 都处理同一个资源和同一个 `resourceVersion`；
- 每个 Controller 的尝试次数相互独立；
- 每个 Controller 的前 4 次调谐返回模拟错误；
- 第 5 次调谐成功；
- ConfigMap 产生新的 `resourceVersion` 后，会开始一轮新的模拟失败。

这样可以保证三组调谐输入和业务逻辑一致，唯一变量是失败后的 queue 策略。

### 三个 Controller 的 queue 配置

三个 Controller 共用 `Controller` 的 worker 和
`processNextWorkItem` 实现，只通过不同的构造函数注入 queue：

```go
controllers := []*Controller{
    NewImmediateController(
        reconciler,
        tracker,
        configMapInformer.Informer().HasSynced,
        opts.maxRetries,
    ),
    NewDelayingController(
        reconciler,
        tracker,
        configMapInformer.Informer().HasSynced,
        opts.maxRetries,
        opts.fixedDelay,
    ),
    NewRateLimitingController(
        reconciler,
        tracker,
        configMapInformer.Informer().HasSynced,
        opts.maxRetries,
        opts.rateBase,
        opts.rateMax,
    ),
}
```

默认参数为：

| Controller | Queue 名称 | 类型 | 重试策略 |
| --- | --- | --- | --- |
| `IMMEDIATE` | `workqueue_demo_immediate` | ordinary | 立即重入队 |
| `DELAYING` | `workqueue_demo_delaying` | delaying | 固定等待 1 秒 |
| `RATE-LIMIT` | `workqueue_demo_rate_limit` | rate-limiting | 250ms 起步、最大 4s 的指数退避 |

项目定义了统一的 `Queue` 接口，使 Controller 不需要知道底层具体是哪一种
client-go queue：

```go
type Queue interface {
    Add(key string)
    Get() (key string, shutdown bool)
    Done(key string)
    ShutDown()

    Config() QueueConfig
    Retry(key string) Retry
    Forget(key string)
    NumRequeues(key string) int
}
```

其中，普通队列和延迟队列没有原生的 `Forget`、`NumRequeues`，因此对应适配器
使用 `retryCounter` 补齐这些能力；限速队列则直接使用 client-go 自带的重试
状态。

## 运行示例

在第一个终端启动 Controller：

```bash
cd chapter10
go run .
```

在第二个终端创建测试 ConfigMap：

```bash
kubectl apply -f chapter10/config/demo-configmap.yaml
```

如果第二个终端也位于 `chapter10` 目录，则使用：

```bash
kubectl apply -f config/demo-configmap.yaml
```

启动日志会先按固定顺序输出三个 queue 的配置：

```text
[SYSTEM    ] WATCH namespace="default" selector="workqueue.demo/enabled=true"
[IMMEDIATE ] START queue=workqueue_demo_immediate  type=ordinary      retry=immediate
[DELAYING  ] START queue=workqueue_demo_delaying   type=delaying      retry=fixed(1s)
[RATE-LIMIT] START queue=workqueue_demo_rate_limit type=rate-limiting retry=exponential(250ms..4s)
```

一次调谐只输出一行，常用字段含义如下：

| 字段 | 含义 |
| --- | --- |
| `try` | 当前 `resourceVersion` 的第几次调谐 |
| `elapsed` | 从 Informer 收到该事件到本次调谐完成的累计时间 |
| `result` | `RETRY`、`SUCCESS`、`DELETED` 或 `DROP` |
| `retry` | 已重新入队次数 / 最大允许重试次数 |
| `next` | 本次失败后，queue 计划等待多久再提供该 key |
| `work` | 本次 Reconcile 自身的执行耗时 |

## 日志结果分析

示例配置让三个 Controller 都失败 4 次并在第 5 次成功。

### 普通队列

```text
[IMMEDIATE ] key=default/workqueue-demo try=1 elapsed=0s result=RETRY   retry=1/6 next=0s
[IMMEDIATE ] key=default/workqueue-demo try=2 elapsed=0s result=RETRY   retry=2/6 next=0s
[IMMEDIATE ] key=default/workqueue-demo try=3 elapsed=0s result=RETRY   retry=3/6 next=0s
[IMMEDIATE ] key=default/workqueue-demo try=4 elapsed=0s result=RETRY   retry=4/6 next=0s
[IMMEDIATE ] key=default/workqueue-demo try=5 elapsed=0s result=SUCCESS work=0s
```

五次调谐几乎发生在同一毫秒，说明普通队列不会为失败请求提供退避保护。

### 延迟队列

```text
[DELAYING  ] key=default/workqueue-demo try=1 elapsed=0s     result=RETRY   retry=1/6 next=1s
[DELAYING  ] key=default/workqueue-demo try=2 elapsed=1.001s result=RETRY   retry=2/6 next=1s
[DELAYING  ] key=default/workqueue-demo try=3 elapsed=2.002s result=RETRY   retry=3/6 next=1s
[DELAYING  ] key=default/workqueue-demo try=4 elapsed=3.002s result=RETRY   retry=4/6 next=1s
[DELAYING  ] key=default/workqueue-demo try=5 elapsed=4.003s result=SUCCESS work=0s
```

每次失败后都固定等待约 1 秒，因此累计时间大约为
`0s、1s、2s、3s、4s`。

### 限速队列

```text
[RATE-LIMIT] key=default/workqueue-demo try=1 elapsed=0s     result=RETRY   retry=1/6 next=250ms
[RATE-LIMIT] key=default/workqueue-demo try=2 elapsed=251ms  result=RETRY   retry=2/6 next=500ms
[RATE-LIMIT] key=default/workqueue-demo try=3 elapsed=751ms  result=RETRY   retry=3/6 next=1s
[RATE-LIMIT] key=default/workqueue-demo try=4 elapsed=1.752s result=RETRY   retry=4/6 next=2s
[RATE-LIMIT] key=default/workqueue-demo try=5 elapsed=3.752s result=SUCCESS work=0s
```

单次等待时间按 `250ms、500ms、1s、2s` 增长，累计调谐时间约为
`0s、0.25s、0.75s、1.75s、3.75s`。这体现了指数退避“开始时快速恢复，连续
失败后逐渐降频”的特点。

### 对比结论

| Controller | 单次重试等待 | 第 5 次调谐的累计时间 | 特征 |
| --- | --- | --- | --- |
| `IMMEDIATE` | `0s、0s、0s、0s` | 约 `0s` | 最快，但持续失败时会 hot loop |
| `DELAYING` | `1s、1s、1s、1s` | 约 `4s` | 节奏稳定，但不会自动调整 |
| `RATE-LIMIT` | `250ms、500ms、1s、2s` | 约 `3.75s` | 根据连续失败次数逐步退避 |

本示例并不是为了比较哪种队列“性能最好”，而是展示三种不同的调度语义。生产
环境应根据错误性质和下游承载能力选择策略。

## Controller 使用 Workqueue 的注意事项

1. **事件回调只做轻量工作**：回调中生成 key 并调用 `Add`，不要执行耗时调谐。

2. **始终调用 `Done`**：通常在 `Get` 成功后立即使用
   `defer queue.Done(key)`，确保所有返回路径都会
   标记本次处理结束。

3. **限速队列要调用 `Forget`**：成功或决定放弃重试时清除 RateLimiter 保存的
   失败状态。

4. **必须设置最大重试次数**：指数退避只能降低频率，不能阻止永久错误无限重试。
   本项目通过
   `-max-retries` 控制上限，默认值为 6。

5. **区分临时错误和永久错误**：网络超时、资源冲突等通常适合重试；参数非法、
   依赖对象永久缺失等错误应直接
   记录并停止重试，或者等待新的资源事件再次触发。

6. **不要把可变对象直接放入队列**：推荐只保存稳定、可比较的 key，并通过
   Lister 读取最新对象。

7. **等待 Informer 缓存同步**：worker 启动前调用
   `cache.WaitForCacheSync`，避免缓存尚未完成初始 List 时就
   开始调谐。

8. **为队列设置名称**：`TypedQueueConfig.Name` 用于区分 queue，并可供
   workqueue metrics 使用。

9. **注意 workqueue 的去重语义**：它传递的是“需要再次调谐”的信号，不保证为
   每一次对象变化执行一次调谐。

10. **谨慎增加 worker 数量**：多 worker 可以提高不同 key 的吞吐量，但同一个
    key 仍不会并发处理；同时还
    要考虑 API Server、数据库或外部服务的容量。
