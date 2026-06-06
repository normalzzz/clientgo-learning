package main

import (
	"log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type EventHandler struct {
	queue workqueue.TypedRateLimitingInterface[string]
}

func NewEventHandler(queue workqueue.TypedRateLimitingInterface[string]) *EventHandler {
	return &EventHandler{queue: queue}
}

func (h *EventHandler) OnAdd(obj interface{}) {
	event, ok := obj.(*corev1.Event)
	if !ok {
		log.Printf("received add object that is not an event")
		return
	}
	h.enqueueIfPodCrashEvent(event)
}

func (h *EventHandler) OnUpdate(oldObj, newObj interface{}) {
	oldEvent, ok := oldObj.(*corev1.Event)
	if !ok {
		log.Printf("received old update object that is not an event")
		return
	}
	newEvent, ok := newObj.(*corev1.Event)
	if !ok {
		log.Printf("received new update object that is not an event")
		return
	}
	if oldEvent.ResourceVersion == newEvent.ResourceVersion {
		return
	}

	h.enqueueIfPodCrashEvent(newEvent)
}

func (h *EventHandler) OnDelete(obj interface{}) {
	event, ok := obj.(*corev1.Event)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			log.Printf("failed to get deleted event object")
			return
		}
		event, ok = tombstone.Obj.(*corev1.Event)
		if !ok {
			log.Printf("deleted object is not an event")
			return
		}
	}

	log.Printf("event deleted: %s/%s", event.Namespace, event.Name)
}

func (h *EventHandler) enqueueIfPodCrashEvent(event *corev1.Event) {
	if !isPodCrashEvent(event) {
		return
	}

	key, err := cache.MetaNamespaceKeyFunc(event)
	if err != nil {
		log.Printf("failed to build event key: %v", err)
		return
	}

	h.queue.Add(key)
}
