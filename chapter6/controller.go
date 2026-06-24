package main

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"time"

	appsv1alpha1 "github.com/normalzzz/clientgo-learning/chapter6/pkg/apis/apps/v1alpha1"
	versioned "github.com/normalzzz/clientgo-learning/chapter6/pkg/generated/clientset/versioned"
	websiteinformers "github.com/normalzzz/clientgo-learning/chapter6/pkg/generated/informers/externalversions/apps/v1alpha1"
	websitelisters "github.com/normalzzz/clientgo-learning/chapter6/pkg/generated/listers/apps/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	websiteLabel   = "apps.clientgo-learning.io/website"
	controllerName = "website-controller"
)

type Controller struct {
	kubeClient    kubernetes.Interface
	websiteClient versioned.Interface

	websiteLister    websitelisters.WebsiteLister
	deploymentLister appslisters.DeploymentLister
	serviceLister    corelisters.ServiceLister

	websiteSynced    cache.InformerSynced
	deploymentSynced cache.InformerSynced
	serviceSynced    cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]
}

func NewController(
	kubeClient kubernetes.Interface,
	websiteClient versioned.Interface,
	websiteInformer websiteinformers.WebsiteInformer,
	deploymentInformer appsinformers.DeploymentInformer,
	serviceInformer coreinformers.ServiceInformer,
) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		websiteClient:    websiteClient,
		websiteLister:    websiteInformer.Lister(),
		deploymentLister: deploymentInformer.Lister(),
		serviceLister:    serviceInformer.Lister(),
		websiteSynced:    websiteInformer.Informer().HasSynced,
		deploymentSynced: deploymentInformer.Informer().HasSynced,
		serviceSynced:    serviceInformer.Informer().HasSynced,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "websites"},
		),
	}
}

func (c *Controller) AddEventHandlers(
	websiteInformer cache.SharedIndexInformer,
	deploymentInformer cache.SharedIndexInformer,
	serviceInformer cache.SharedIndexInformer,
) error {
	websiteHandler := NewWebsiteHandler(c.queue)
	if _, err := websiteInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    websiteHandler.OnAdd,
		UpdateFunc: websiteHandler.OnUpdate,
		DeleteFunc: websiteHandler.OnDelete,
	}); err != nil {
		return err
	}

	ownedHandler := NewOwnedResourceHandler(c.queue)
	childHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    ownedHandler.OnAdd,
		UpdateFunc: ownedHandler.OnUpdate,
		DeleteFunc: ownedHandler.OnDelete,
	}
	if _, err := deploymentInformer.AddEventHandler(childHandlers); err != nil {
		return err
	}
	_, err := serviceInformer.AddEventHandler(childHandlers)
	return err
}

func (c *Controller) Run(ctx context.Context, workers int) error {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	log.Println("starting Website controller")
	if ok := cache.WaitForCacheSync(
		ctx.Done(), c.websiteSynced, c.deploymentSynced, c.serviceSynced,
	); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	<-ctx.Done()
	log.Println("shutting down Website controller")
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

	website, err := c.websiteLister.Websites(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	deployment, err := c.reconcileDeployment(ctx, website)
	if err != nil {
		return err
	}
	if err := c.reconcileService(ctx, website); err != nil {
		return err
	}
	return c.updateStatus(ctx, website, deployment)
}

func (c *Controller) reconcileDeployment(ctx context.Context, website *appsv1alpha1.Website) (*appsv1.Deployment, error) {
	desired := desiredDeployment(website)
	current, err := c.deploymentLister.Deployments(website.Namespace).Get(website.Name)
	if apierrors.IsNotFound(err) {
		return c.kubeClient.AppsV1().Deployments(website.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(current, website) {
		return nil, fmt.Errorf("Deployment %s/%s already exists and is not controlled by Website", website.Namespace, website.Name)
	}

	updated := current.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec.Replicas = desired.Spec.Replicas
	updated.Spec.Template.Labels = desired.Spec.Template.Labels
	updated.Spec.Template.Spec.Containers = desired.Spec.Template.Spec.Containers
	if reflect.DeepEqual(current.Labels, updated.Labels) && reflect.DeepEqual(current.Spec, updated.Spec) {
		return current, nil
	}
	return c.kubeClient.AppsV1().Deployments(website.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
}

func (c *Controller) reconcileService(ctx context.Context, website *appsv1alpha1.Website) error {
	desired := desiredService(website)
	current, err := c.serviceLister.Services(website.Namespace).Get(website.Name)
	if apierrors.IsNotFound(err) {
		_, err = c.kubeClient.CoreV1().Services(website.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(current, website) {
		return fmt.Errorf("Service %s/%s already exists and is not controlled by Website", website.Namespace, website.Name)
	}

	if reflect.DeepEqual(current.Labels, desired.Labels) &&
		reflect.DeepEqual(current.Spec.Ports, desired.Spec.Ports) &&
		reflect.DeepEqual(current.Spec.Selector, desired.Spec.Selector) {
		return nil
	}
	updated := current.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec.Ports = desired.Spec.Ports
	updated.Spec.Selector = desired.Spec.Selector
	_, err = c.kubeClient.CoreV1().Services(website.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) updateStatus(ctx context.Context, website *appsv1alpha1.Website, deployment *appsv1.Deployment) error {
	desiredReplicas := replicasFor(website)
	readyReplicas := deployment.Status.ReadyReplicas
	phase := appsv1alpha1.WebsitePhasePending
	if readyReplicas >= desiredReplicas {
		phase = appsv1alpha1.WebsitePhaseAvailable
	} else if readyReplicas > 0 {
		phase = appsv1alpha1.WebsitePhaseDegraded
	}

	if website.Status.ReadyReplicas == readyReplicas && website.Status.Phase == phase {
		return nil
	}
	updated := website.DeepCopy()
	updated.Status.ReadyReplicas = readyReplicas
	updated.Status.Phase = phase
	_, err := c.websiteClient.AppsV1alpha1().Websites(website.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	return err
}

func desiredDeployment(website *appsv1alpha1.Website) *appsv1.Deployment {
	labels := labelsFor(website)
	replicas := replicasFor(website)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            website.Name,
			Namespace:       website.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{websiteControllerRef(website)},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "website",
					Image: website.Spec.Image,
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: portFor(website)}},
				}}},
			},
		},
	}
}

func desiredService(website *appsv1alpha1.Website) *corev1.Service {
	labels := labelsFor(website)
	port := portFor(website)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            website.Name,
			Namespace:       website.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{websiteControllerRef(website)},
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name: "http", Port: port, TargetPort: intstr.FromInt32(port),
			}},
		},
	}
}

func websiteControllerRef(website *appsv1alpha1.Website) metav1.OwnerReference {
	return *metav1.NewControllerRef(website, appsv1alpha1.SchemeGroupVersion.WithKind("Website"))
}

func labelsFor(website *appsv1alpha1.Website) map[string]string {
	return map[string]string{managedByLabel: controllerName, websiteLabel: website.Name}
}

func replicasFor(website *appsv1alpha1.Website) int32 {
	if website.Spec.Replicas == nil {
		return 1
	}
	return *website.Spec.Replicas
}

func portFor(website *appsv1alpha1.Website) int32 {
	if website.Spec.Port == 0 {
		return 80
	}
	return website.Spec.Port
}
