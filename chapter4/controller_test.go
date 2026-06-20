package main

import (
	"context"
	"testing"

	appsv1alpha1 "github.com/normalzzz/clientgo-learning/chapter4/pkg/apis/apps/v1alpha1"
	websitefake "github.com/normalzzz/clientgo-learning/chapter4/pkg/generated/clientset/versioned/fake"
	websitelisters "github.com/normalzzz/clientgo-learning/chapter4/pkg/generated/listers/apps/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

func TestSyncHandlerCreatesResourcesAndUpdatesStatus(t *testing.T) {
	replicas := int32(2)
	website := &appsv1alpha1.Website{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo", Namespace: "default", UID: types.UID("website-uid"),
		},
		Spec: appsv1alpha1.WebsiteSpec{Image: "nginx:1.27", Replicas: &replicas, Port: 8080},
	}

	websiteIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	deploymentIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	serviceIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	if err := websiteIndexer.Add(website); err != nil {
		t.Fatal(err)
	}

	kubeClient := fake.NewSimpleClientset()
	websiteClient := websitefake.NewSimpleClientset(website.DeepCopy())
	controller := &Controller{
		kubeClient:       kubeClient,
		websiteClient:    websiteClient,
		websiteLister:    websitelisters.NewWebsiteLister(websiteIndexer),
		deploymentLister: appslisters.NewDeploymentLister(deploymentIndexer),
		serviceLister:    corelisters.NewServiceLister(serviceIndexer),
	}

	if err := controller.syncHandler(context.Background(), "default/demo"); err != nil {
		t.Fatalf("syncHandler() error = %v", err)
	}

	deployment, err := kubeClient.AppsV1().Deployments("default").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected Deployment: %v", err)
	}
	if *deployment.Spec.Replicas != replicas || deployment.Spec.Template.Spec.Containers[0].Image != "nginx:1.27" {
		t.Fatalf("unexpected Deployment spec: %#v", deployment.Spec)
	}
	if !metav1.IsControlledBy(deployment, website) {
		t.Fatal("Deployment does not have Website as controller owner")
	}

	service, err := kubeClient.CoreV1().Services("default").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected Service: %v", err)
	}
	if service.Spec.Ports[0].Port != 8080 {
		t.Fatalf("Service port = %d, want 8080", service.Spec.Ports[0].Port)
	}

	updatedWebsite, err := websiteClient.AppsV1alpha1().Websites("default").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated Website: %v", err)
	}
	if updatedWebsite.Status.Phase != appsv1alpha1.WebsitePhasePending || updatedWebsite.Status.ReadyReplicas != 0 {
		t.Fatalf("unexpected Website status: %#v", updatedWebsite.Status)
	}
}

func TestDesiredResourcesUseDefaults(t *testing.T) {
	website := &appsv1alpha1.Website{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec:       appsv1alpha1.WebsiteSpec{Image: "nginx:latest"},
	}

	deployment := desiredDeployment(website)
	if *deployment.Spec.Replicas != 1 {
		t.Fatalf("replicas = %d, want 1", *deployment.Spec.Replicas)
	}
	if deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort != 80 {
		t.Fatalf("container port = %d, want 80", deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)
	}
	service := desiredService(website)
	if service.Spec.Ports[0].Port != 80 {
		t.Fatalf("service port = %d, want 80", service.Spec.Ports[0].Port)
	}
}
