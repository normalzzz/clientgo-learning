package leaderelection

import (
	"context"
	"log"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const leaseName = "website-controller-leader"

func Run(
	ctx context.Context,
	clientset kubernetes.Interface,
	namespace string,
	onStartedLeading func(context.Context),
	onStoppedLeading func(),
) {
	id := podIdentity()
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseName,
			Namespace: namespace,
		},
		Client: clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}

	log.Printf("starting leader election, identity=%s, namespace=%s", id, namespace)
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: onStartedLeading,
			OnStoppedLeading: onStoppedLeading,
			OnNewLeader: func(identity string) {
				if identity == id {
					return
				}
				log.Printf("new leader elected: %s", identity)
			},
		},
	})
}

func podIdentity() string {
	if name := os.Getenv("SELF_POD_NAME"); name != "" {
		return name
	}
	hostname, _ := os.Hostname()
	return hostname
}
