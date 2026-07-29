package main

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/client-go/util/workqueue"
)

// Queue hides only the retry-policy differences needed by Controller. The
// worker lifecycle remains identical for all three controller instances.
type Queue interface {
	Add(key string)
	Get() (key string, shutdown bool)
	Done(key string)
	ShutDown()

	Config() QueueConfig
	Retry(key string) Retry
	Forget(key string)
	NumRequeues(key string) int
}

type QueueConfig struct {
	Name        string
	Type        string
	RetryPolicy string
}

type Retry struct {
	Number int
	After  time.Duration
}

type retryCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newRetryCounter() retryCounter {
	return retryCounter{counts: make(map[string]int)}
}

func (c *retryCounter) increment(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
	return c.counts[key]
}

func (c *retryCounter) get(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[key]
}

func (c *retryCounter) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.counts, key)
}

// immediateQueue retries with Add. A continuously failing key therefore forms
// a hot loop and consumes a worker as quickly as reconcile can return.
type immediateQueue struct {
	workqueue.TypedInterface[string]
	config  QueueConfig
	retries retryCounter
}

func NewImmediateQueue(name string) Queue {
	return &immediateQueue{
		TypedInterface: workqueue.NewTypedWithConfig(
			workqueue.TypedQueueConfig[string]{Name: name},
		),
		config: QueueConfig{
			Name:        name,
			Type:        "ordinary",
			RetryPolicy: "immediate",
		},
		retries: newRetryCounter(),
	}
}

func (q *immediateQueue) Config() QueueConfig {
	return q.config
}

func (q *immediateQueue) Retry(key string) Retry {
	number := q.retries.increment(key)
	q.Add(key)
	return Retry{Number: number}
}

func (q *immediateQueue) Forget(key string) {
	q.retries.forget(key)
}

func (q *immediateQueue) NumRequeues(key string) int {
	return q.retries.get(key)
}

// delayingQueue retries every key after the same fixed duration.
type delayingQueue struct {
	workqueue.TypedDelayingInterface[string]
	config  QueueConfig
	delay   time.Duration
	retries retryCounter
}

func NewDelayingQueue(name string, delay time.Duration) Queue {
	return &delayingQueue{
		TypedDelayingInterface: workqueue.NewTypedDelayingQueueWithConfig(
			workqueue.TypedDelayingQueueConfig[string]{Name: name},
		),
		config: QueueConfig{
			Name:        name,
			Type:        "delaying",
			RetryPolicy: fmt.Sprintf("fixed(%s)", delay),
		},
		delay:   delay,
		retries: newRetryCounter(),
	}
}

func (q *delayingQueue) Config() QueueConfig {
	return q.config
}

func (q *delayingQueue) Retry(key string) Retry {
	number := q.retries.increment(key)
	q.AddAfter(key, q.delay)
	return Retry{Number: number, After: q.delay}
}

func (q *delayingQueue) Forget(key string) {
	q.retries.forget(key)
}

func (q *delayingQueue) NumRequeues(key string) int {
	return q.retries.get(key)
}

// observingRateLimiter records the duration selected by the real client-go
// limiter, so the controller can put that value in the comparison log.
type observingRateLimiter struct {
	delegate workqueue.TypedRateLimiter[string]

	mu    sync.Mutex
	after map[string]time.Duration
}

func newObservingRateLimiter(delegate workqueue.TypedRateLimiter[string]) *observingRateLimiter {
	return &observingRateLimiter{
		delegate: delegate,
		after:    make(map[string]time.Duration),
	}
}

func (r *observingRateLimiter) When(key string) time.Duration {
	after := r.delegate.When(key)
	r.mu.Lock()
	r.after[key] = after
	r.mu.Unlock()
	return after
}

func (r *observingRateLimiter) Forget(key string) {
	r.delegate.Forget(key)
	r.mu.Lock()
	delete(r.after, key)
	r.mu.Unlock()
}

func (r *observingRateLimiter) NumRequeues(key string) int {
	return r.delegate.NumRequeues(key)
}

func (r *observingRateLimiter) lastDelay(key string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.after[key]
}

type rateLimitingQueue struct {
	workqueue.TypedRateLimitingInterface[string]
	limiter *observingRateLimiter
	config  QueueConfig
}

func NewRateLimitingQueue(name string, baseDelay, maxDelay time.Duration) Queue {
	limiter := newObservingRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[string](baseDelay, maxDelay),
	)
	return &rateLimitingQueue{
		TypedRateLimitingInterface: workqueue.NewTypedRateLimitingQueueWithConfig(
			limiter,
			workqueue.TypedRateLimitingQueueConfig[string]{Name: name},
		),
		limiter: limiter,
		config: QueueConfig{
			Name:        name,
			Type:        "rate-limiting",
			RetryPolicy: fmt.Sprintf("exponential(%s..%s)", baseDelay, maxDelay),
		},
	}
}

func (q *rateLimitingQueue) Config() QueueConfig {
	return q.config
}

func (q *rateLimitingQueue) Retry(key string) Retry {
	q.AddRateLimited(key)
	return Retry{
		Number: q.NumRequeues(key),
		After:  q.limiter.lastDelay(key),
	}
}
