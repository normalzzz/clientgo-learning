package main

import (
	"log"

	appsv1alpha1 "github.com/normalzzz/clientgo-learning/chapter6/pkg/apis/apps/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type WebsiteHandler struct {
	queue workqueue.TypedRateLimitingInterface[string]
}

func NewWebsiteHandler(queue workqueue.TypedRateLimitingInterface[string]) *WebsiteHandler {
	return &WebsiteHandler{queue: queue}
}

func (h *WebsiteHandler) OnAdd(obj interface{}) {
	h.enqueue(obj)
}

func (h *WebsiteHandler) OnUpdate(oldObj, newObj interface{}) {
	oldWebsite, oldOK := oldObj.(*appsv1alpha1.Website)
	newWebsite, newOK := newObj.(*appsv1alpha1.Website)
	if !oldOK || !newOK {
		log.Printf("received update objects that are not Websites")
		return
	}
	if oldWebsite.ResourceVersion == newWebsite.ResourceVersion {
		return
	}
	h.enqueue(newWebsite)
}

func (h *WebsiteHandler) OnDelete(obj interface{}) {
	h.enqueue(obj)
}

func (h *WebsiteHandler) enqueue(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		log.Printf("failed to build Website key: %v", err)
		return
	}
	h.queue.Add(key)
}

type OwnedResourceHandler struct {
	queue workqueue.TypedRateLimitingInterface[string]
}

func NewOwnedResourceHandler(queue workqueue.TypedRateLimitingInterface[string]) *OwnedResourceHandler {
	return &OwnedResourceHandler{queue: queue}
}

func (h *OwnedResourceHandler) OnAdd(obj interface{}) {
	h.enqueueOwner(obj)
}

func (h *OwnedResourceHandler) OnUpdate(oldObj, newObj interface{}) {
	oldMeta, oldErr := meta.Accessor(oldObj)
	newMeta, newErr := meta.Accessor(newObj)
	if oldErr != nil || newErr != nil {
		log.Printf("received child update objects without metadata")
		return
	}
	if oldMeta.GetResourceVersion() == newMeta.GetResourceVersion() {
		return
	}
	h.enqueueOwner(newObj)
}

func (h *OwnedResourceHandler) OnDelete(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	h.enqueueOwner(obj)
}

func (h *OwnedResourceHandler) enqueueOwner(obj interface{}) {
	object, err := meta.Accessor(obj)
	if err != nil {
		log.Printf("failed to read child object metadata: %v", err)
		return
	}

	owner := metav1.GetControllerOf(object)
	if owner == nil || owner.APIVersion != appsv1alpha1.SchemeGroupVersion.String() || owner.Kind != "Website" {
		return
	}

	key, err := cache.MetaNamespaceKeyFunc(&metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{Namespace: object.GetNamespace(), Name: owner.Name},
	})
	if err != nil {
		log.Printf("failed to build owner Website key: %v", err)
		return
	}
	h.queue.Add(key)
}
