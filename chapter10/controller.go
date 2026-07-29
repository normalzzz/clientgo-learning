package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
)

type Controller struct {
	name        string
	logLabel    string
	queue       Queue
	reconciler  Reconciler
	tracker     *EventTracker
	cacheSynced cache.InformerSynced
	maxRetries  int
}

func NewImmediateController(
	reconciler Reconciler,
	tracker *EventTracker,
	cacheSynced cache.InformerSynced,
	maxRetries int,
) *Controller {
	return newController(
		"immediate",
		NewImmediateQueue("workqueue_demo_immediate"),
		reconciler,
		tracker,
		cacheSynced,
		maxRetries,
	)
}

func NewDelayingController(
	reconciler Reconciler,
	tracker *EventTracker,
	cacheSynced cache.InformerSynced,
	maxRetries int,
	delay time.Duration,
) *Controller {
	return newController(
		"delaying",
		NewDelayingQueue("workqueue_demo_delaying", delay),
		reconciler,
		tracker,
		cacheSynced,
		maxRetries,
	)
}

func NewRateLimitingController(
	reconciler Reconciler,
	tracker *EventTracker,
	cacheSynced cache.InformerSynced,
	maxRetries int,
	baseDelay, maxDelay time.Duration,
) *Controller {
	return newController(
		"rate-limit",
		NewRateLimitingQueue("workqueue_demo_rate_limit", baseDelay, maxDelay),
		reconciler,
		tracker,
		cacheSynced,
		maxRetries,
	)
}

func newController(
	name string,
	queue Queue,
	reconciler Reconciler,
	tracker *EventTracker,
	cacheSynced cache.InformerSynced,
	maxRetries int,
) *Controller {
	return &Controller{
		name:        name,
		logLabel:    strings.ToUpper(name),
		queue:       queue,
		reconciler:  reconciler,
		tracker:     tracker,
		cacheSynced: cacheSynced,
		maxRetries:  maxRetries,
	}
}

func (c *Controller) Run(ctx context.Context, workers int) error {
	c.logConfiguration(workers)
	return c.run(ctx, workers)
}

func (c *Controller) logConfiguration(workers int) {
	queueConfig := c.queue.Config()
	logf(
		"[%-10s] START queue=%-29s type=%-13s retry=%-25s workers=%d max-retries=%d",
		c.logLabel,
		queueConfig.Name,
		queueConfig.Type,
		queueConfig.RetryPolicy,
		workers,
		c.maxRetries,
	)
}

func (c *Controller) run(ctx context.Context, workers int) error {
	defer runtime.HandleCrash()

	if ok := cache.WaitForCacheSync(ctx.Done(), c.cacheSynced); !ok {
		c.queue.ShutDown()
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("%s controller: failed to wait for cache sync", c.name)
	}

	var workersWG sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			c.runWorker(ctx)
		}()
	}

	<-ctx.Done()
	c.queue.ShutDown()
	workersWG.Wait()
	logf("[%-10s] STOP", c.logLabel)
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

	started := time.Now()
	result, err := c.reconciler.Reconcile(ctx, c.name, key)
	duration := time.Since(started)
	elapsed, _ := c.tracker.Since(key)
	if err == nil {
		c.queue.Forget(key)
		outcome := "SUCCESS"
		if result.Deleted {
			outcome = "DELETED"
		}
		logf(
			"[%-10s] key=%-30s try=%-3d elapsed=%-8s result=%-7s work=%s",
			c.logLabel,
			key,
			result.Attempt,
			formatDuration(elapsed),
			outcome,
			formatDuration(duration),
		)
		return true
	}

	if c.queue.NumRequeues(key) >= c.maxRetries {
		retries := c.queue.NumRequeues(key)
		c.queue.Forget(key)
		logf(
			"[%-10s] key=%-30s try=%-3d elapsed=%-8s result=DROP    retry=%d/%d error=%q",
			c.logLabel,
			key,
			result.Attempt,
			formatDuration(elapsed),
			retries,
			c.maxRetries,
			err,
		)
		return true
	}

	retry := c.queue.Retry(key)
	logf(
		"[%-10s] key=%-30s try=%-3d elapsed=%-8s result=RETRY   retry=%d/%d next=%-8s error=%q",
		c.logLabel,
		key,
		result.Attempt,
		formatDuration(elapsed),
		retry.Number,
		c.maxRetries,
		formatDuration(retry.After),
		err,
	)
	return true
}

func formatDuration(duration time.Duration) time.Duration {
	return duration.Round(time.Millisecond)
}
