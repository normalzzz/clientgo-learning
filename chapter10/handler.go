package main

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// EnqueueHandler fans one informer event out to all controller queues. This is
// what guarantees that the three controllers receive the same key and start
// from one shared event timestamp.
type EnqueueHandler struct {
	queues  []Queue
	tracker *EventTracker
}

func NewEnqueueHandler(tracker *EventTracker, queues ...Queue) *EnqueueHandler {
	return &EnqueueHandler{queues: queues, tracker: tracker}
}

func (h *EnqueueHandler) OnAdd(obj interface{}) {
	h.enqueue("add", obj)
}

func (h *EnqueueHandler) OnUpdate(oldObj, newObj interface{}) {
	oldMeta, oldOK := oldObj.(metav1.Object)
	newMeta, newOK := newObj.(metav1.Object)
	if oldOK && newOK && oldMeta.GetResourceVersion() == newMeta.GetResourceVersion() {
		return
	}
	h.enqueue("update", newObj)
}

func (h *EnqueueHandler) OnDelete(obj interface{}) {
	h.enqueue("delete", obj)
}
func (h *EnqueueHandler) enqueue(eventType string, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		logf("[EVENT     ] %-6s result=DROP error=%q", eventType, err)
		return
	}

	resourceVersion := ""
	if meta, err := metaObject(obj); err == nil {
		resourceVersion = meta.GetResourceVersion()
	}
	h.tracker.Mark(key, resourceVersion)

	logf(
		"[EVENT     ] %-6s key=%-30s rv=%-10s queues=%d",
		eventType,
		key,
		resourceVersion,
		len(h.queues),
	)
	// Fan out the key to all controller queues.
	for _, queue := range h.queues {
		queue.Add(key)
	}
}

func metaObject(obj interface{}) (metav1.Object, error) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	return meta.Accessor(obj)
}
