# Chapter 1: Clientset 与 Informer 入门

本章通过两个简单示例介绍 Kubernetes `client-go` 的基本用法：使用 Clientset 查询集群中的 Node，以及使用 Informer 监听 `default` namespace 中的 Pod 变化。

这两个示例分别展示了直接请求 API Server 和通过本地缓存、事件回调处理资源的方式，为后续章节的 Controller 实现打基础。

## 目录结构

```text
chapter1/
  clientset/
    clientset.go  # 加载 kubeconfig，查询 Node 并输出状态
  informer/
    informer.go   # 监听 Pod 的 Add、Update、Delete，读取本地缓存
  go.mod          # 本章独立的 Go module
  go.sum
  README.md
```

## 核心概念

| 组件 | 作用 | 本章示例 |
| --- | --- | --- |
| kubeconfig | 提供集群地址、身份凭据和 context 等连接配置 | 读取 `~/.kube/config` |
| Clientset | 提供按 API Group、Version 和资源类型组织的客户端 | `CoreV1().Nodes().List(...)` |
| SharedInformerFactory | 创建和管理共享 Informer | 创建 Pod Informer |
| Informer | 同步资源到本地缓存，并分发资源变化回调 | 监听 `default` 中的 Pod |
| Store | 保存 Informer 缓存的资源对象 | `GetStore().List()` |

Clientset 示例执行一次查询后退出。Informer 示例则持续接收资源变化，并维护本地缓存。

## 初始化 Kubernetes client

两个示例使用相同的初始化流程：

```go
kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")

config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
if err != nil {
    log.Fatalf("failed to build kubeconfig: %v", err)
}

clientset, err := kubernetes.NewForConfig(config)
if err != nil {
    log.Fatalf("failed to create clientset: %v", err)
}
```

`BuildConfigFromFlags` 从指定的 kubeconfig 构建连接配置，`NewForConfig` 根据配置创建 Clientset。后续调用资源 API 时，客户端会使用这些连接和认证信息。

当前代码固定读取用户主目录下的 `.kube/config`，没有提供 kubeconfig 路径参数，也没有实现读取失败后回退到 `rest.InClusterConfig()` 的逻辑。仅设置 `KUBECONFIG` 环境变量不会改变这里显式传入的路径。

## Clientset 查询 Node

代码位于 [clientset/clientset.go](./clientset/clientset.go)。

```go
nodes, err := clientset.CoreV1().Nodes().List(
    context.Background(),
    metav1.ListOptions{},
)
```

- `CoreV1()`：访问 Kubernetes 核心 API 组的 `v1` 版本。
- `Nodes()`：获取 Node 客户端。Node 是集群级资源，不需要指定 namespace。
- `List()`：向 API Server 查询 Node 列表。
- `metav1.ListOptions{}`：使用默认查询选项，没有设置 label 或 field selector。

程序遍历返回的 `nodes.Items`，输出每个 Node 的名称、Ready 状态和 Finalizers：

```text
Node Name: worker-1
Ready Status: True
Finalizers: []
---
```

`getNodeReadyStatus` 从 `node.Status.Conditions` 中寻找 `NodeReady` 条件，返回其 `Status`，可能是 `True`、`False` 或 `Unknown`。如果没有找到该条件，也返回 `Unknown`。

Finalizers 来自 `node.Finalizers`，用于展示对象上记录的删除清理标记；本示例只读取这些信息。

## Informer 监听 Pod

代码位于 [informer/informer.go](./informer/informer.go)。整体流程如下：

```text
创建 Clientset
    |
    v
创建 SharedInformerFactory 和 Pod Informer
    |
    v
注册 Add / Update / Delete 回调
    |
    v
启动 Informer，等待缓存同步
    |
    v
输出缓存中的 Pod 数量，持续监听资源变化
```

### 创建 Informer

```go
factory := informers.NewSharedInformerFactoryWithOptions(
    clientset,
    30*time.Second,
    informers.WithNamespace("default"),
)

podInformer := factory.Core().V1().Pods().Informer()
```

`WithNamespace("default")` 把监听范围限定为 `default` namespace。Informer 会获取已有资源并持续监听变化，将 Pod 对象保存在本地缓存中。

`30*time.Second` 是 resync 周期，用于周期性地重新分发缓存对象的事件，不表示每隔 30 秒重新向 API Server 查询所有 Pod。

### 注册事件回调

通过 `cache.ResourceEventHandlerFuncs` 注册三类回调：

| 回调 | 处理逻辑 | 输出示例 |
| --- | --- | --- |
| `AddFunc` | 输出新增或初始同步发现的 Pod | `[ADD] default/demo-pod phase=Pending` |
| `UpdateFunc` | 比较 ResourceVersion，变化时输出新的 Pod phase | `[UPDATE] default/demo-pod phase=Running` |
| `DeleteFunc` | 获取被删除的 Pod 并输出名称 | `[DELETE] default/demo-pod` |

Update 回调包含以下判断：

```go
if oldPod.ResourceVersion == newPod.ResourceVersion {
    return
}
```

它会跳过 ResourceVersion 相同的回调，例如 resync 触发的重复通知。这里没有比较 `Status.Phase`，所以修改 Pod 的 label 等字段也可能输出 Update 日志，即使 phase 没有变化。

Delete 回调同时处理 `*corev1.Pod` 和 `cache.DeletedFinalStateUnknown`。后者用于 Informer 未能直接获得删除事件时，携带缓存中最后已知的对象；示例会从其中取出 Pod，再输出删除日志。

### 启动和缓存同步

```go
factory.Start(stopCh)

if !cache.WaitForCacheSync(stopCh, podInformer.HasSynced) {
    log.Fatalf("failed to sync pod informer cache")
}

fmt.Println("pod informer started")

pods := podInformer.GetStore().List()
fmt.Printf("current cached pods: %d\n", len(pods))

<-stopCh
```

`Start` 启动 Informer，`WaitForCacheSync` 等待初始缓存同步完成。随后通过 `GetStore().List()` 读取本地缓存，这次读取不会直接请求 API Server，输出数量对应当时的缓存状态。

程序最后阻塞在 `<-stopCh`，持续监听。当前示例没有注册系统信号处理，也没有主动关闭 `stopCh` 的运行路径；本地实验可使用 `Ctrl+C` 结束进程。

## 本地运行

### 环境准备

- Go 1.26.0 或更高版本，与本章 `go.mod` 要求一致。
- 一个可访问的 Kubernetes 集群，以及配置好的 `~/.kube/config`。
- `kubectl`，用于检查连接和创建测试 Pod。
- kubeconfig 对应的身份需要具有集群级 Node 的 `list` 权限，以及 `default` namespace 中 Pod 的 `list`、`watch` 权限。创建测试 Pod 还需要对应的创建、修改和删除权限。

从仓库根目录进入本章并下载依赖：

```bash
cd chapter1
go mod download

kubectl --kubeconfig="$HOME/.kube/config" get nodes
kubectl --kubeconfig="$HOME/.kube/config" get pods -n default
```

### 程序入口说明

当前 `clientset/clientset.go` 声明的是 `package clientset`，`informer/informer.go` 声明的是 `package informer`。虽然两个文件都定义了 `func main()`，但 Go 可执行程序要求入口位于 `package main`。

运行前，需要把要执行的示例文件第一行改为：

```go
package main
```

两个示例位于不同目录，可以分别调整并运行。如果保留当前包声明，执行下面的 `go run` 命令会提示该包不是 main 包。

### 运行 Clientset 示例

在 `chapter1` 目录执行：

```bash
go run ./clientset
```

程序输出集群中每个 Node 的名称、Ready 状态和 Finalizers，然后退出。

### 运行 Informer 示例

在 `chapter1` 目录执行：

```bash
go run ./informer
```

启动时可能先看到已有 Pod 的 `[ADD]` 日志，随后输出：

```text
pod informer started
current cached pods: 3
```

实际数量取决于 `default` namespace 中的 Pod。在另一个终端创建、修改并删除测试 Pod：

```bash
kubectl --kubeconfig="$HOME/.kube/config" run clientgo-demo --image=nginx:1.27 --restart=Never -n default
kubectl --kubeconfig="$HOME/.kube/config" label pod clientgo-demo chapter=one -n default
kubectl --kubeconfig="$HOME/.kube/config" delete pod clientgo-demo -n default
```

观察 Informer 终端中的 `[ADD]`、`[UPDATE]` 和 `[DELETE]` 日志。Pod 调度和状态变化也会触发更新，具体日志数量和顺序取决于集群中的实际变化。

## 与后续章节的关系

本章在 Informer 回调中直接打印资源信息。[Chapter 2](../chapter2/Readme.md) 在此基础上引入 workqueue、Lister 和 worker，将事件接收与实际处理逻辑连接起来，实现 Pod crash Event 通知 Controller。
