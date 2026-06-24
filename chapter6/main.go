package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	versioned "github.com/normalzzz/clientgo-learning/chapter6/pkg/generated/clientset/versioned"
	externalversions "github.com/normalzzz/clientgo-learning/chapter6/pkg/generated/informers/externalversions"
	leaderelection "github.com/normalzzz/clientgo-learning/chapter6/pkg/leaderelection"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	config, err := buildConfig()
	if err != nil {
		log.Fatalf("failed to build Kubernetes config: %v", err)
	}

	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create Kubernetes client: %v", err)
	}
	websiteClient, err := versioned.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create Website client: %v", err)
	}

	leaseNamespace := os.Getenv("SELF_POD_NAMESPACE")
	if leaseNamespace == "" {
		leaseNamespace = "default"
	}

	leaderelection.Run(
		ctx,
		kubeClient,
		leaseNamespace,
		onStartedLeading(kubeClient, websiteClient),
		onStoppedLeading(cancel),
	)
}

func onStartedLeading(kubeClient kubernetes.Interface, websiteClient versioned.Interface) func(context.Context) {
	return func(ctx context.Context) {
		log.Println("became leader, starting Website controller")

		if err := runController(ctx, kubeClient, websiteClient); err != nil {
			log.Fatalf("controller stopped with error: %v", err)
		}
	}
}

func onStoppedLeading(cancel context.CancelFunc) func() {
	return func() {
		log.Println("lost leadership, shutting down")
		cancel()
	}
}

func runController(ctx context.Context, kubeClient kubernetes.Interface, websiteClient versioned.Interface) error {
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

	websiteInformer := websiteInformerFactory.Apps().V1alpha1().Websites()
	deploymentInformer := kubeInformerFactory.Apps().V1().Deployments()
	serviceInformer := kubeInformerFactory.Core().V1().Services()

	controller := NewController(
		kubeClient,
		websiteClient,
		websiteInformer,
		deploymentInformer,
		serviceInformer,
	)
	if err := controller.AddEventHandlers(
		websiteInformer.Informer(),
		deploymentInformer.Informer(),
		serviceInformer.Informer(),
	); err != nil {
		log.Fatalf("failed to add event handlers: %v", err)
	}

	kubeInformerFactory.Start(ctx.Done())
	websiteInformerFactory.Start(ctx.Done())

	return controller.Run(ctx, 2)
}

func buildConfig() (*rest.Config, error) {
	config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err == nil {
		return config, nil
	}
	return rest.InClusterConfig()
}
