package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type snsAPI interface {
	Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

type Controller struct {
	kubeclientset kubernetes.Interface
	snsClient     snsAPI
	topicARN      string

	eventLister corelisters.EventLister
	eventSynced cache.InformerSynced
	queue       workqueue.TypedRateLimitingInterface[string]
}

func NewController(
	kubeclientset kubernetes.Interface,
	snsClient snsAPI,
	topicARN string,
	eventInformer v1.EventInformer,
) *Controller {
	return &Controller{
		kubeclientset: kubeclientset,
		snsClient:     snsClient,
		topicARN:      topicARN,
		eventLister:   eventInformer.Lister(),
		eventSynced:   eventInformer.Informer().HasSynced,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "pod-crash-events"},
		),
	}
}

func (c *Controller) AddEventHandlers(eventInformer cache.SharedIndexInformer) error {
	handler := NewEventHandler(c.queue)
	_, err := eventInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    handler.OnAdd,
		UpdateFunc: handler.OnUpdate,
		DeleteFunc: handler.OnDelete,
	})
	return err
}

func (c *Controller) Run(ctx context.Context, workers int) error {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	log.Println("starting pod crash event controller")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.eventSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	<-ctx.Done()
	log.Println("shutting down pod crash event controller")
	return nil
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}

	defer c.queue.Done(key)

	if err := c.syncHandler(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) syncHandler(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	event, err := c.eventLister.Events(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		log.Printf("event %q no longer exists", key)
		return nil
	}
	if err != nil {
		return err
	}
	if !isPodCrashEvent(event) {
		return nil
	}

	return c.publishPodCrashEvent(ctx, event)
}

func (c *Controller) publishPodCrashEvent(ctx context.Context, event *corev1.Event) error {
	subject := fmt.Sprintf("Pod crash detected: %s/%s", event.InvolvedObject.Namespace, event.InvolvedObject.Name)
	message := buildNotificationMessage(event)

	_, err := c.snsClient.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(c.topicARN),
		Subject:  aws.String(subject),
		Message:  aws.String(message),
	})
	if err != nil {
		return err
	}

	log.Printf("sent pod crash notification for event %s/%s", event.Namespace, event.Name)
	return nil
}

func isPodCrashEvent(event *corev1.Event) bool {
	if event.InvolvedObject.Kind != "Pod" {
		return false
	}
	if event.Type != corev1.EventTypeWarning {
		return false
	}

	reason := strings.ToLower(event.Reason)
	message := strings.ToLower(event.Message)

	return reason == "backoff" ||
		strings.Contains(reason, "crash") ||
		strings.Contains(message, "crashloopbackoff") ||
		strings.Contains(message, "back-off restarting failed container")
}

func buildNotificationMessage(event *corev1.Event) string {
	return fmt.Sprintf(`Pod crash event detected

Namespace: %s
Pod: %s
Reason: %s
Type: %s
Message: %s
FirstTimestamp: %s
LastTimestamp: %s
Count: %d
Event: %s/%s
`,
		event.InvolvedObject.Namespace,
		event.InvolvedObject.Name,
		event.Reason,
		event.Type,
		event.Message,
		event.FirstTimestamp.Format(time.RFC3339),
		event.LastTimestamp.Format(time.RFC3339),
		event.Count,
		event.Namespace,
		event.Name,
	)
}
