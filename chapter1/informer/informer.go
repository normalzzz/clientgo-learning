package informer

import (
	"fmt"
	"log"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("failed to build kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create clientset: %v", err)
	}

	stopCh := make(chan struct{})
	defer close(stopCh)

	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		30*time.Second,
		informers.WithNamespace("default"),
	)

	podInformer := factory.Core().V1().Pods().Informer()

	_, err = podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			fmt.Printf("[ADD] %s/%s phase=%s\n", pod.Namespace, pod.Name, pod.Status.Phase)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldPod := oldObj.(*corev1.Pod)
			newPod := newObj.(*corev1.Pod)

			if oldPod.ResourceVersion == newPod.ResourceVersion {
				return
			}

			fmt.Printf("[UPDATE] %s/%s phase=%s\n", newPod.Namespace, newPod.Name, newPod.Status.Phase)
		},
		DeleteFunc: func(obj interface{}) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					log.Printf("failed to get deleted pod object")
					return
				}
				pod, ok = tombstone.Obj.(*corev1.Pod)
				if !ok {
					log.Printf("deleted object is not a pod")
					return
				}
			}

			fmt.Printf("[DELETE] %s/%s\n", pod.Namespace, pod.Name)
		},
	})
	if err != nil {
		log.Fatalf("failed to add event handler: %v", err)
	}

	factory.Start(stopCh)

	if !cache.WaitForCacheSync(stopCh, podInformer.HasSynced) {
		log.Fatalf("failed to sync pod informer cache")
	}

	fmt.Println("pod informer started")

	pods := podInformer.GetStore().List()
	fmt.Printf("current cached pods: %d\n", len(pods))

	<-stopCh
}