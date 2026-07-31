# client-go Learning

这是一个以实践为主的 Kubernetes `client-go` 学习项目。项目从最基础的
Clientset 和 Informer 开始，逐步实现 CRD、代码生成、Controller 调谐循环、
资源生命周期管理、Leader Election、Conversion Webhook，并进一步使用
Kubebuilder 和 controller-runtime 构建完整的 Operator。

项目按章节组织，每个章节都是独立的 Go module，可以单独阅读、运行和测试。
示例代码以“可观察、可调试”为目标，适合希望理解 Kubernetes Controller
内部工作方式的开发者。

## 学习路线

```text
Clientset / Informer
        │
        ▼
CRD 与代码生成
        │
        ▼
手写 Controller 调谐循环
        │
        ├── 多版本 API 与 Conversion Webhook
        ├── Leader Election
        ├── Finalizer
        └── OwnerReference
        │
        ▼
Kubebuilder / controller-runtime
        │
        ▼
workqueue 重试策略
```

## 章节导航

| 章节 | 主题 | 核心内容 |
| --- | --- | --- |
| [Chapter 1](./chapter1/) | Clientset 与 Informer 入门 | 加载 kubeconfig、创建 Clientset、查询 Node，以及使用 Informer 监听资源 |
| [Chapter 2](./chapter2/Readme.md) | Pod Crash Event Controller | 监听 Kubernetes Warning Event，通过 workqueue 处理 Pod 崩溃事件，并发送 AWS SNS 通知 |
| [Chapter 3](./chapter3/README.md) | CRD 与代码生成 | 使用 controller-gen 定义 `Website` CRD，生成 Clientset、Informer、Lister 和 DeepCopy 代码 |
| [Chapter 4](./chapter4/README.md) | 手写 Website Controller | 使用 Informer、Lister 和 rate-limited workqueue 调谐 Deployment、Service 与 Website Status |
| [Chapter 5](./chapter5/README.md) | 多版本 CRD | 实现 `v1alpha1`、`v1` API 以及 Conversion Webhook |
| [Chapter 6](./chapter6/README.md) | Leader Election | 使用 Lease 保证多个 Controller 副本中只有 Leader 执行调谐 |
| [Chapter 7](./chapter7/README.md) | Finalizer | 在 Website 删除流程中执行资源清理，并安全移除 Finalizer |
| [Chapter 8](./chapter8/README.md) | Kubebuilder 与 controller-runtime | 使用 Manager、Reconciler、Owns、Status 和标准工程脚手架实现 Website Operator |
| [Chapter 9](./chapter9/Readme.md) | OwnerReference | 理解 Kubernetes 垃圾回收、级联删除策略以及 Owner 与 Dependent 的生命周期关系 |
| [Chapter 10](./chapter10/README.md) | workqueue 重试策略 | 对比普通队列、延迟队列和限速队列的立即重试、固定延迟与指数退避 |

## 项目结构

```text
clientgo-learn/
├── chapter1/                 # Clientset、Informer 基础
├── chapter2/                 # Pod Crash Event Controller
├── chapter3/                 # CRD 与代码生成
├── chapter4/                 # 手写 Website Controller
├── chapter5/                 # 多版本 API 与 Conversion Webhook
├── chapter6/                 # Leader Election
├── chapter7/                 # Finalizer
├── chapter8/                 # Kubebuilder / controller-runtime
├── chapter9/                 # OwnerReference 与垃圾回收
├── chapter10/                # workqueue 重试策略
└── LICENSE
```

各章节通常包含以下内容：

- `go.mod`、`go.sum`：章节独立依赖。
- `main.go` 或 `cmd/`：程序入口。
- `controller.go`、`handler.go`：Controller 与事件处理逻辑。
- `config/`：CRD、RBAC、Deployment 和示例资源清单。
- `deploy/`：手写 Controller 的部署清单。
- `pkg/generated/`：由 Kubernetes 代码生成工具产生的客户端代码。

## 环境要求

建议准备以下环境：

- Go 1.26 或更高版本。
- 一个可访问的 Kubernetes 集群。
- `kubectl`，且当前 kubeconfig context 指向测试集群。
- Docker；运行镜像构建或部署示例时需要。
- Kind；运行 Chapter 8 E2E 测试时推荐使用隔离的 Kind 集群。
- AWS 凭据和 SNS Topic；仅 Chapter 2 的完整通知流程需要。


## 快速开始

克隆仓库并确认开发环境：

```bash
git clone https://github.com/normalzzz/clientgo-learning.git
cd clientgo-learning

go version
kubectl cluster-info
kubectl config current-context
```

本仓库根目录不是 Go module，请进入具体章节后运行 Go 命令：

```bash
cd chapter10
go mod download
go test ./...
go run .
```

不同章节的启动方式和依赖不同，请优先阅读对应章节的 README。

## 常用操作

### 运行单元测试

```bash
cd chapter7
go test ./...
```

### 生成 CRD 和客户端代码

Chapter 3～7 使用 Makefile 管理代码生成：

```bash
cd chapter7
make generate
make crd
make verify
```

生成文件与 API 类型必须保持同步。修改 `pkg/apis/` 下的类型后，应重新运行代码
生成命令。

### 运行 Kubebuilder 项目

```bash
cd chapter8
make manifests
make generate
make test
make run
```

`make run` 会使用当前 kubeconfig 连接集群。部署到集群前，请检查
`config/manager/kustomization.yaml` 中的镜像地址。

### 部署示例资源

以下命令以 Chapter 8 为例：

```bash
cd chapter8
make install
kubectl apply -f config/samples/apps_v1alpha1_website.yaml
kubectl get websites
```

实验结束后可以卸载 CRD：

```bash
make uninstall
```

## 核心概念

本项目围绕标准 Kubernetes Controller 链路展开：

```text
API Server
    │ List / Watch
    ▼
Informer ──事件回调──> workqueue ──worker──> Reconcile
    │                                         │
    └──本地缓存 <──────── Lister 读取──────────┘
                                              │
                                              ├── 创建或更新资源
                                              ├── 更新 Status
                                              └── 失败重试
```

- Informer 负责 List、Watch、缓存和事件分发。
- Lister 从本地缓存读取对象，减少对 API Server 的直接请求。
- workqueue 对资源 key 去重，并控制失败后的重试节奏。
- Reconcile 比较期望状态和实际状态，执行幂等调谐。
- OwnerReference 交给 Kubernetes 垃圾回收器管理从属资源。
- Finalizer 用于删除资源前必须完成的外部清理。
- Leader Election 保证高可用部署中只有一个实例执行关键逻辑。


## 说明

这是学习型项目，部分实现会刻意保留较底层的 client-go 写法，以展示 Informer、
Lister、workqueue 和调谐循环之间的协作关系。用于生产环境前，还需要根据实际
需求补充权限收敛、可观测性、Webhook 证书管理、升级策略和完整的集成测试。

## License

本项目使用 [MIT License](./LICENSE)。
