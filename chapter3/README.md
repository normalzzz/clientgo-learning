# Chapter 3: 使用 controller-gen 定义自定义资源 API

本章通过一个简单的 `Website` 自定义资源，演示如何用 Go 结构体描述 Kubernetes API，并通过代码生成工具得到 CRD、DeepCopy、Clientset、Informer、Lister 等代码。

本章示例关注的是自定义资源 API 的定义和生成，不实现完整 Controller 调谐逻辑。Controller 的核心逻辑可以在后续章节继续基于这里生成的 Clientset、Informer、Lister 编写。

参考资料：

- Kubernetes 自定义资源文档：https://kubernetes.io/zh-cn/docs/concepts/extend-kubernetes/api-extension/custom-resources/
- controller-tools 项目仓库：https://github.com/kubernetes-sigs/controller-tools
- Kubebuilder controller-gen 文档：https://book.kubebuilder.io/reference/controller-gen

## 什么是自定义资源？

Kubernetes 自定义资源是扩展 Kubernetes API 的核心机制。通过自定义资源，开发者可以把自己的业务对象注册到 Kubernetes API Server 中，让它们像 `Pod`、`Deployment`、`Service` 一样被 `kubectl`、client-go、Informer 等机制访问。

例如，本章定义的资源如下：

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

创建 CRD 之后，就可以像操作 Kubernetes 原生资源一样操作它：

```bash
kubectl get websites
kubectl get web
kubectl describe website demo-website
```

## CRD CustomResourceDefinition

CRD 是 `CustomResourceDefinition` 的缩写，它本身也是 Kubernetes 中的一种资源。CRD 的作用是告诉 API Server：现在集群中新增了一种资源类型，它属于哪个 API Group、哪个 Version、叫什么 Kind、字段结构是什么。

CRD 包含几个核心要素：

- API Group：资源所属的 API 分组，例如本章的 `apps.clientgo-learning.io`。
- API Version：资源版本，例如本章的 `v1alpha1`。
- Kind：资源类型名称，例如本章的 `Website`。
- Names：资源的复数名、单数名、短名称，例如 `websites`、`website`、`web`。
- Scope：资源作用域，可以是 `Namespaced` 或 `Cluster`。
- Schema：资源字段结构，使用 OpenAPI v3 schema 描述。
- Subresources：子资源，例如 `status`。
- Additional Printer Columns：`kubectl get` 时额外展示的列。

本章最终生成的 CRD 文件为：

```bash
config/crd/bases/apps.clientgo-learning.io_websites.yaml
```

## CRD、Controller 和 Operator 的关系

CRD 只负责扩展 Kubernetes API，让 API Server 能够存储和校验新的资源对象。它不负责真正执行业务逻辑。

Controller 负责监听资源变化，并把实际状态不断调整到期望状态。例如用户创建了一个 `Website`：

```yaml
spec:
  image: nginx:1.27
  replicas: 2
```

那么 Controller 可以监听这个 `Website` 对象，并根据 `spec` 创建 Deployment、Service 等资源。当 Pod 就绪后，Controller 再更新 `Website.status.readyReplicas` 和 `Website.status.phase`。

Operator 可以理解为 CRD 与 Controller 的组合：CRD 定义领域对象，Controller 实现自动化运维逻辑。很多复杂系统，例如数据库、消息队列、存储系统，都可以通过 Operator 方式运行在 Kubernetes 上。

## 实现自定义资源的开发

自定义资源开发通常可以拆成两部分：

1. 定义 API：编写 Go 类型，生成 CRD、DeepCopy、Clientset、Informer、Lister。
2. 编写 Controller：监听自定义资源事件，根据 `spec` 执行业务逻辑，并更新 `status`。

本章实现的是第一部分，也就是自定义资源 API 的定义和代码生成。

## 为什么需要代码生成？

CRD 本质上是一份 `CustomResourceDefinition` YAML。理论上可以手写 CRD，只要符合 Kubernetes 规范即可。但是手写 CRD 有几个明显问题：

- 字段多，结构深，OpenAPI schema 容易写错。
- Go struct 和 CRD YAML 容易不同步。
- validation、default、status subresource、printer columns 等配置手写成本较高。
- 自定义资源还需要 DeepCopy、Clientset、Informer、Lister 等配套代码，手写成本更高。

更常见的方式是先定义 Go 类型，再通过 marker 注解描述 API 约束，最后使用工具生成所需产物。

本章使用两类工具：

- `controller-gen`：根据 `kubebuilder` marker 生成 CRD 和 DeepCopy。
- `k8s.io/code-generator`：根据 `+genclient` 等 marker 生成 Clientset、Informer、Lister。

## 本章目录结构

本章相关文件如下：

```bash
chapter3/
├── Makefile
├── README.md
├── config/
│   ├── crd/bases/apps.clientgo-learning.io_websites.yaml
│   └── samples/apps_v1alpha1_website.yaml
├── hack/
│   ├── boilerplate.go.txt
│   ├── tools.go
│   └── update-codegen.sh
├── pkg/
│   ├── apis/apps/v1alpha1/
│   │   ├── doc.go
│   │   ├── register.go
│   │   ├── types.go
│   │   └── zz_generated.deepcopy.go
│   └── generated/
│       ├── clientset/
│       ├── informers/
│       └── listers/
├── go.mod
└── go.sum
```

## 如何使用 controller-gen 生成 CRD 模板？

如果只想单独安装 `controller-gen` 命令，可以执行：

```bash
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest
```

安装完成后，二进制通常位于 `GOBIN` 或 `GOPATH/bin` 下：

```bash
which controller-gen
```

本章为了保证示例可复现，没有要求预先安装全局 `controller-gen`。`Makefile` 中直接使用 `go run sigs.k8s.io/controller-tools/cmd/controller-gen` 执行生成命令，并通过 `hack/tools.go` 固定工具依赖版本。

执行下面命令即可生成 CRD 和 DeepCopy：

```bash
make crd
```

执行完整生成流程：

```bash
make generate
```

### controller-gen 工具的简单使用：
本章以 `Website` 自定义资源为例，演示如何通过 Go 类型定义生成 CRD、DeepCopy、Clientset、Informer、Lister。

这里需要先区分两个工具的职责：

- `controller-gen`：读取 `kubebuilder` marker 注解，生成 CRD YAML 和 DeepCopy 方法。
- `k8s.io/code-generator`：读取 `+genclient`、`+k8s:deepcopy-gen` 等注解，生成 Clientset、Informer、Lister。

也就是说，Clientset、Informer、Lister 不是 `controller-gen` 直接生成的，但它们和 CRD 使用同一份 API 类型定义，因此在实际项目中通常会放在同一个生成流程里。

#### 1. 定义 types.go

`types.go` 是自定义资源最核心的文件，用 Go struct 描述 Kubernetes API 中的资源结构。本章中的文件路径为：

```bash
pkg/apis/apps/v1alpha1/types.go
```

示例中定义了三个主要结构：

- `WebsiteSpec`：期望状态，对应 YAML 中的 `spec`。
- `WebsiteStatus`：实际状态，对应 YAML 中的 `status`。
- `Website` / `WebsiteList`：真正注册到 Kubernetes API Server 的资源对象和列表对象。

核心结构如下：

```go
type Website struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebsiteSpec   `json:"spec,omitempty"`
	Status WebsiteStatus `json:"status,omitempty"`
}
```

`TypeMeta` 包含 `apiVersion` 和 `kind`，`ObjectMeta` 包含 `name`、`namespace`、`labels`、`annotations` 等 Kubernetes 通用元数据。

`types.go` 中的注解说明如下。

字段校验和默认值注解：

```go
// +kubebuilder:validation:MinLength=1
Image string `json:"image"`
```

表示 `spec.image` 至少需要 1 个字符，生成 CRD 时会被转换成 OpenAPI v3 schema 中的 `minLength: 1`。

```go
// +kubebuilder:default=1
// +kubebuilder:validation:Minimum=0
// +optional
Replicas *int32 `json:"replicas,omitempty"`
```

表示 `spec.replicas` 默认值为 `1`，最小值为 `0`。`+optional` 和 `omitempty` 表示该字段可以不填写。

```go
// +kubebuilder:default=80
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:Maximum=65535
// +optional
Port int32 `json:"port,omitempty"`
```

表示 `spec.port` 默认值为 `80`，取值范围为 `1` 到 `65535`。

```go
// +kubebuilder:validation:Enum=Pending;Available;Degraded
// +optional
Phase string `json:"phase,omitempty"`
```

表示 `status.phase` 只能是 `Pending`、`Available`、`Degraded` 三个值之一。

资源级别注解：

```go
// +genclient
```

告诉 `k8s.io/code-generator` 为 `Website` 生成 typed client，也就是后续的 Clientset 代码。

```go
// +genclient:nonNamespaced=false
```

表示该资源是 namespace 级别的资源。生成后的 client 调用方式类似：

```go
client.AppsV1alpha1().Websites(namespace)
```

```go
// +kubebuilder:object:root=true
```

告诉 `controller-gen` 这是一个 Kubernetes API 的根对象，需要为它生成 CRD schema 和 DeepCopyObject 方法。`Website` 和 `WebsiteList` 都需要这个注解。

```go
// +kubebuilder:resource:scope=Namespaced,shortName=web
```

定义 CRD 的资源范围和短名称。生成后可以通过下面的命令查看资源：

```bash
kubectl get websites
kubectl get web
```

```go
// +kubebuilder:subresource:status
```

为 CRD 开启 `status` 子资源。开启后，控制器可以单独更新状态：

```bash
kubectl get website demo-website -o yaml
```

API Server 会把 `spec` 和 `status` 的更新路径区分开，避免控制器更新状态时误改用户期望状态。

```go
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
```

定义 `kubectl get website` 时额外展示的列。生成 CRD 后，这些配置会进入 `additionalPrinterColumns`。

`WebsiteList` 的注解：

```go
// +kubebuilder:object:root=true
type WebsiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Website `json:"items"`
}
```

Kubernetes API 中的资源列表也需要是一个标准 API 对象，因此 `WebsiteList` 同样需要 `+kubebuilder:object:root=true`。

#### 2. 定义 register.go

`register.go` 负责把自定义资源注册到 Kubernetes runtime scheme 中。本章文件路径为：

```bash
pkg/apis/apps/v1alpha1/register.go
```

核心内容如下：

```go
const (
	GroupName = "apps.clientgo-learning.io"
	Version   = "v1alpha1"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme
)
```

`SchemeGroupVersion` 定义了资源所属的 API Group 和 Version，对应 YAML 中的：

```yaml
apiVersion: apps.clientgo-learning.io/v1alpha1
kind: Website
```

`addKnownTypes` 会把 `Website` 和 `WebsiteList` 注册进 scheme：

```go
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion, &Website{}, &WebsiteList{})
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
```

后续无论是 clientset、informer，还是 controller-runtime client，都需要依赖 scheme 识别这个资源类型。

#### 3. 定义 doc.go

`doc.go` 用来声明当前 API 包的包级别注解。本章文件路径为：

```bash
pkg/apis/apps/v1alpha1/doc.go
```

内容如下：

```go
// Package v1alpha1 contains API Schema definitions for the apps API group.
//
// +kubebuilder:object:generate=true
// +groupName=apps.clientgo-learning.io
// +k8s:deepcopy-gen=package
package v1alpha1
```

这些注解的含义是：

- `+kubebuilder:object:generate=true`：让 `controller-gen object` 为当前包生成 DeepCopy 相关方法。
- `+groupName=apps.clientgo-learning.io`：声明 CRD 的 API Group。
- `+k8s:deepcopy-gen=package`：让 `k8s.io/code-generator` 识别当前包需要生成 deepcopy 代码。

#### 4. 使用 controller-gen 生成 CRD 和 DeepCopy

本章已经在 `Makefile` 中封装了生成命令：

```bash
make crd
```

实际执行的是：

```bash
go run sigs.k8s.io/controller-tools/cmd/controller-gen \
	object:headerFile=./hack/boilerplate.go.txt \
	crd:crdVersions=v1 \
	paths=./pkg/apis/... \
	output:crd:artifacts:config=./config/crd/bases
```

参数说明：

- `object:headerFile=...`：生成 DeepCopy 代码，并给生成文件添加统一头部。
- `crd:crdVersions=v1`：生成 `apiextensions.k8s.io/v1` 版本的 CRD。
- `paths=./pkg/apis/...`：扫描 API 类型定义所在目录。
- `output:crd:artifacts:config=...`：指定 CRD YAML 输出目录。

生成结果包括：

```bash
config/crd/bases/apps.clientgo-learning.io_websites.yaml
pkg/apis/apps/v1alpha1/zz_generated.deepcopy.go
```

#### 5. 生成 Clientset、Informer、Lister

Clientset、Informer、Lister 使用 `k8s.io/code-generator` 生成。本章通过脚本封装在：

```bash
hack/update-codegen.sh
```

执行完整生成命令：

```bash
make generate
```

`make generate` 会先调用 `controller-gen` 生成 CRD 和 DeepCopy，然后调用 `hack/update-codegen.sh` 生成 Clientset、Informer、Lister。

生成结果包括：

```bash
pkg/generated/clientset/...
pkg/generated/informers/...
pkg/generated/listers/...
```

这些代码生成后，就可以在 controller 中像使用原生 Kubernetes 资源一样使用 `Website`：

```go
client.AppsV1alpha1().Websites("default").Get(ctx, "demo-website", metav1.GetOptions{})
```

最后可以运行验证命令：

```bash
make verify
```
