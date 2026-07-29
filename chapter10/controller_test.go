package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestControllersUseDifferentRetrySchedules(t *testing.T) {
	const failures = 3

	reconciler := newTimingReconciler(failures)
	tracker := NewEventTracker()
	synced := cache.InformerSynced(func() bool { return true })
	controllers := []*Controller{
		NewImmediateController(reconciler, tracker, synced, failures+1),
		NewDelayingController(reconciler, tracker, synced, failures+1, 30*time.Millisecond),
		NewRateLimitingController(
			reconciler,
			tracker,
			synced,
			failures+1,
			10*time.Millisecond,
			40*time.Millisecond,
		),
	}

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, len(controllers))
	for _, controller := range controllers {
		go func() {
			errs <- controller.Run(ctx, 1)
		}()
	}

	const key = "default/demo"
	tracker.Mark(key, "1")
	for _, controller := range controllers {
		controller.queue.Add(key)
	}

	completed := make(map[string]bool)
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for len(completed) < len(controllers) {
		select {
		case name := <-reconciler.completed:
			completed[name] = true
		case <-timeout.C:
			t.Fatalf("timed out; completed controllers: %v", completed)
		}
	}

	cancel()
	for range controllers {
		if err := <-errs; err != nil {
			t.Fatalf("controller Run() error = %v", err)
		}
	}

	immediate := reconciler.invocations("immediate")
	delaying := reconciler.invocations("delaying")
	rateLimited := reconciler.invocations("rate-limit")
	for name, invocations := range map[string][]time.Time{
		"immediate":  immediate,
		"delaying":   delaying,
		"rate-limit": rateLimited,
	} {
		if got, want := len(invocations), failures+1; got != want {
			t.Fatalf("%s invocation count = %d, want %d", name, got, want)
		}
	}

	assertMinimumGaps(t, "delaying", delaying, []time.Duration{
		20 * time.Millisecond,
		20 * time.Millisecond,
		20 * time.Millisecond,
	})
	assertMinimumGaps(t, "rate-limit", rateLimited, []time.Duration{
		5 * time.Millisecond,
		12 * time.Millisecond,
		28 * time.Millisecond,
	})

	if elapsed(immediate) >= elapsed(delaying) {
		t.Errorf(
			"immediate elapsed %s should be less than delaying elapsed %s",
			elapsed(immediate),
			elapsed(delaying),
		)
	}
}

func TestEnqueueHandlerFansOutTheSameKey(t *testing.T) {
	tracker := NewEventTracker()
	queues := []Queue{
		NewImmediateQueue("handler_one"),
		NewImmediateQueue("handler_two"),
		NewImmediateQueue("handler_three"),
	}
	t.Cleanup(func() {
		for _, queue := range queues {
			queue.ShutDown()
		}
	})

	handler := NewEnqueueHandler(tracker, queues...)
	handler.OnAdd(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "demo",
			Name:            "request",
			ResourceVersion: "42",
		},
	})

	for i, queue := range queues {
		key, shutdown := queue.Get()
		if shutdown {
			t.Fatalf("queue %d unexpectedly shut down", i)
		}
		if key != "demo/request" {
			t.Errorf("queue %d key = %q, want %q", i, key, "demo/request")
		}
		queue.Done(key)
	}
	if _, resourceVersion := tracker.Since("demo/request"); resourceVersion != "42" {
		t.Errorf("tracked resourceVersion = %q, want %q", resourceVersion, "42")
	}
}

func TestQueueConfigurations(t *testing.T) {
	tests := []struct {
		name  string
		queue Queue
		want  QueueConfig
	}{
		{
			name:  "immediate",
			queue: NewImmediateQueue("workqueue_demo_immediate"),
			want: QueueConfig{
				Name:        "workqueue_demo_immediate",
				Type:        "ordinary",
				RetryPolicy: "immediate",
			},
		},
		{
			name:  "delaying",
			queue: NewDelayingQueue("workqueue_demo_delaying", time.Second),
			want: QueueConfig{
				Name:        "workqueue_demo_delaying",
				Type:        "delaying",
				RetryPolicy: "fixed(1s)",
			},
		},
		{
			name: "rate limiting",
			queue: NewRateLimitingQueue(
				"workqueue_demo_rate_limit",
				250*time.Millisecond,
				4*time.Second,
			),
			want: QueueConfig{
				Name:        "workqueue_demo_rate_limit",
				Type:        "rate-limiting",
				RetryPolicy: "exponential(250ms..4s)",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Cleanup(test.queue.ShutDown)
			if got := test.queue.Config(); got != test.want {
				t.Errorf("Config() = %+v, want %+v", got, test.want)
			}
		})
	}
}

type timingReconciler struct {
	mu        sync.Mutex
	failures  int
	times     map[string][]time.Time
	completed chan string
}

func newTimingReconciler(failures int) *timingReconciler {
	return &timingReconciler{
		failures:  failures,
		times:     make(map[string][]time.Time),
		completed: make(chan string, 3),
	}
}

func (r *timingReconciler) Reconcile(
	_ context.Context,
	controllerName, _ string,
) (ReconcileResult, error) {
	r.mu.Lock()
	r.times[controllerName] = append(r.times[controllerName], time.Now())
	attempt := len(r.times[controllerName])
	r.mu.Unlock()

	result := ReconcileResult{Attempt: attempt, Failures: r.failures}
	if attempt <= r.failures {
		return result, fmt.Errorf("failure %d", attempt)
	}
	r.completed <- controllerName
	return result, nil
}

func (r *timingReconciler) invocations(controllerName string) []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.times[controllerName]...)
}

func assertMinimumGaps(
	t *testing.T,
	name string,
	invocations []time.Time,
	minimums []time.Duration,
) {
	t.Helper()
	for i, minimum := range minimums {
		gap := invocations[i+1].Sub(invocations[i])
		if gap < minimum {
			t.Errorf("%s gap %d = %s, want at least %s", name, i+1, gap, minimum)
		}
	}
}

func elapsed(invocations []time.Time) time.Duration {
	return invocations[len(invocations)-1].Sub(invocations[0])
}
