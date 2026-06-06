package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	topicARN := os.Getenv("SNS_TOPIC_ARN")
	if topicARN == "" {
		log.Fatal("SNS_TOPIC_ARN is required")
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("failed to load aws config: %v", err)
	}
	snsClient := sns.NewFromConfig(awsConfig)

	config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err != nil {
		restConfig, err := rest.InClusterConfig()
		if err != nil {
			log.Fatal(err)
		}
		config = restConfig
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create kubernetes client: %v", err)
	}

	namespace := os.Getenv("WATCH_NAMESPACE")
	if namespace == "" {
		namespace = metav1.NamespaceAll
	}

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		30*time.Second,
		informers.WithNamespace(namespace),
	)
	eventInformer := informerFactory.Core().V1().Events()

	controller := NewController(clientset, snsClient, topicARN, eventInformer)
	if err := controller.AddEventHandlers(eventInformer.Informer()); err != nil {
		log.Fatalf("failed to add event handlers: %v", err)
	}

	informerFactory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), eventInformer.Informer().HasSynced) {
		log.Fatal("failed to sync event informer cache")
	}

	if err := controller.Run(ctx, 2); err != nil {
		log.Fatalf("controller stopped with error: %v", err)
	}
}
