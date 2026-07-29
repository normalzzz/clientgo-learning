package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const enabledLabelSelector = "workqueue.demo/enabled=true"

type options struct {
	kubeconfig   string
	masterURL    string
	namespace    string
	workers      int
	maxRetries   int
	fixedDelay   time.Duration
	rateBase     time.Duration
	rateMax      time.Duration
	resyncPeriod time.Duration
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	opts := bindFlags()
	flag.Parse()

	if err := opts.validate(); err != nil {
		log.Fatalf("invalid options: %v", err)
	}

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	config, err := buildConfig(opts.masterURL, opts.kubeconfig)
	if err != nil {
		log.Fatalf("failed to build Kubernetes config: %v", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create Kubernetes client: %v", err)
	}

	if err := run(ctx, client, opts); err != nil {
		log.Fatalf("controllers stopped with error: %v", err)
	}
}

func bindFlags() options {
	var opts options
	flag.StringVar(
		&opts.kubeconfig,
		"kubeconfig",
		"",
		"path to a kubeconfig; empty uses the standard client-go loading rules",
	)
	flag.StringVar(&opts.masterURL, "master", "", "Kubernetes API server address")
	flag.StringVar(&opts.namespace, "namespace", "default", "namespace to watch")
	flag.IntVar(&opts.workers, "workers", 1, "workers per controller")
	flag.IntVar(&opts.maxRetries, "max-retries", 6, "retries before a key is dropped")
	flag.DurationVar(
		&opts.fixedDelay,
		"fixed-delay",
		time.Second, // 默认值为 1 秒
		"retry delay used by the delaying queue",
	)
	flag.DurationVar(
		&opts.rateBase,
		"rate-base-delay",
		250*time.Millisecond,
		"first retry delay used by the exponential rate limiter",
	)
	flag.DurationVar(
		&opts.rateMax,
		"rate-max-delay",
		4*time.Second,
		"maximum retry delay used by the exponential rate limiter",
	)
	flag.DurationVar(
		&opts.resyncPeriod,
		"resync",
		0,
		"informer resync period; zero disables periodic resync",
	)
	return opts
}

func (o options) validate() error {
	switch {
	case o.workers < 1:
		return fmt.Errorf("workers must be at least 1")
	case o.maxRetries < 0:
		return fmt.Errorf("max-retries must be non-negative")
	case o.fixedDelay < 0:
		return fmt.Errorf("fixed-delay must be non-negative")
	case o.rateBase <= 0:
		return fmt.Errorf("rate-base-delay must be positive")
	case o.rateMax < o.rateBase:
		return fmt.Errorf("rate-max-delay must be at least rate-base-delay")
	case o.resyncPeriod < 0:
		return fmt.Errorf("resync must be non-negative")
	default:
		return nil
	}
}

func run(ctx context.Context, client kubernetes.Interface, opts options) error {
	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		opts.resyncPeriod,
		informers.WithNamespace(opts.namespace),
		informers.WithTweakListOptions(func(listOptions *metav1.ListOptions) {
			listOptions.LabelSelector = enabledLabelSelector
		}),
	)
	configMapInformer := factory.Core().V1().ConfigMaps()

	tracker := NewEventTracker()
	reconciler := NewFailureReconciler(configMapInformer.Lister())
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

	handler := NewEnqueueHandler(
		tracker,
		controllers[0].queue,
		controllers[1].queue,
		controllers[2].queue,
	)
	// 向 informer 注册三个 controller 的事件处理函数，保证三个 controller 收到相同的 key 并从一个共享的事件时间戳开始
	if _, err := configMapInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    handler.OnAdd,
			UpdateFunc: handler.OnUpdate,
			DeleteFunc: handler.OnDelete,
		},
	); err != nil {
		return fmt.Errorf("add ConfigMap event handler: %w", err)
	}

	logf(
		"[SYSTEM    ] WATCH namespace=%q selector=%q",
		opts.namespace,
		enabledLabelSelector,
	)

	// 按固定顺序输出三个 queue 的配置，避免并发启动导致配置行交错。
	for _, controller := range controllers {
		controller.logConfiguration(opts.workers)
	}

	errs := make(chan error, len(controllers))
	// 启动三个 controller
	for _, controller := range controllers {
		go func() {
			errs <- controller.run(ctx, opts.workers)
		}()
	}

	// queue 配置全部输出后再启动 informer，确保事件日志位于配置块之后。
	factory.Start(ctx.Done())

	for range controllers {
		if err := <-errs; err != nil {
			return err
		}
	}
	return nil
}

func buildConfig(masterURL, kubeconfig string) (*rest.Config, error) {
	if masterURL == "" && kubeconfig == "" {
		if config, err := rest.InClusterConfig(); err == nil {
			return config, nil
		}
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	overrides.ClusterInfo.Server = masterURL
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		overrides,
	).ClientConfig()
}
