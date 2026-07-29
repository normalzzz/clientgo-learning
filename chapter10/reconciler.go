package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const failuresAnnotation = "workqueue.demo/failures"

type ReconcileResult struct {
	Attempt         int
	Failures        int
	ResourceVersion string
	Deleted         bool
}

type Reconciler interface {
	Reconcile(ctx context.Context, controllerName, key string) (ReconcileResult, error)
}

// FailureReconciler is deliberately deterministic: for each controller and
// ConfigMap resourceVersion it fails the first N attempts, where N comes from
// the workqueue.demo/failures annotation. All controllers execute this same
// method, while their counters remain independent.
type FailureReconciler struct {
	configMaps corelisters.ConfigMapLister

	mu       sync.Mutex
	attempts map[attemptKey]int
}

type attemptKey struct {
	controller      string
	key             string
	resourceVersion string
}

func NewFailureReconciler(configMaps corelisters.ConfigMapLister) *FailureReconciler {
	return &FailureReconciler{
		configMaps: configMaps,
		attempts:   make(map[attemptKey]int),
	}
}

func (r *FailureReconciler) Reconcile(
	_ context.Context,
	controllerName, key string,
) (ReconcileResult, error) {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return ReconcileResult{}, err
	}

	configMap, err := r.configMaps.ConfigMaps(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		return ReconcileResult{Deleted: true}, nil
	}
	if err != nil {
		return ReconcileResult{}, err
	}

	failures, err := configuredFailures(configMap.Annotations[failuresAnnotation])
	if err != nil {
		return ReconcileResult{ResourceVersion: configMap.ResourceVersion}, err
	}

	counterKey := attemptKey{
		controller:      controllerName,
		key:             key,
		resourceVersion: configMap.ResourceVersion,
	}
	r.mu.Lock()
	r.attempts[counterKey]++
	attempt := r.attempts[counterKey]
	r.mu.Unlock()

	result := ReconcileResult{
		Attempt:         attempt,
		Failures:        failures,
		ResourceVersion: configMap.ResourceVersion,
	}
	if attempt <= failures {
		return result, fmt.Errorf(
			"simulated transient error (%d/%d)",
			attempt,
			failures,
		)
	}
	return result, nil
}

func configuredFailures(value string) (int, error) {
	if value == "" {
		return 4, nil
	}
	failures, err := strconv.Atoi(value)
	if err != nil || failures < 0 {
		return 0, fmt.Errorf(
			"annotation %s must be a non-negative integer, got %q",
			failuresAnnotation,
			value,
		)
	}
	return failures, nil
}
