package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

func TestFailureReconcilerFailsPerControllerAndResourceVersion(t *testing.T) {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
	})
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            "demo",
			ResourceVersion: "1",
			Annotations: map[string]string{
				failuresAnnotation: "2",
			},
		},
	}
	if err := indexer.Add(configMap); err != nil {
		t.Fatalf("indexer.Add() error = %v", err)
	}

	reconciler := NewFailureReconciler(corelisters.NewConfigMapLister(indexer))
	ctx := context.Background()
	for attempt := 1; attempt <= 3; attempt++ {
		result, err := reconciler.Reconcile(ctx, "immediate", "default/demo")
		if result.Attempt != attempt {
			t.Errorf("immediate attempt = %d, want %d", result.Attempt, attempt)
		}
		if (err != nil) != (attempt <= 2) {
			t.Errorf("immediate attempt %d error = %v", attempt, err)
		}
	}

	result, err := reconciler.Reconcile(ctx, "delaying", "default/demo")
	if result.Attempt != 1 || err == nil {
		t.Errorf("delaying first result = %+v, error = %v; want independent failure", result, err)
	}

	updated := configMap.DeepCopy()
	updated.ResourceVersion = "2"
	if err := indexer.Update(updated); err != nil {
		t.Fatalf("indexer.Update() error = %v", err)
	}
	result, err = reconciler.Reconcile(ctx, "immediate", "default/demo")
	if result.Attempt != 1 || err == nil {
		t.Errorf("new resourceVersion result = %+v, error = %v; want reset failure", result, err)
	}
}
