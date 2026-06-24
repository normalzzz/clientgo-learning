package conversion

import (
	"testing"

	appsv1 "github.com/normalzzz/clientgo-learning/chapter5/pkg/apis/apps/v1"
	appsv1alpha1 "github.com/normalzzz/clientgo-learning/chapter5/pkg/apis/apps/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWebsiteV1Alpha1ToV1(t *testing.T) {
	replicas := int32(3)
	in := &appsv1alpha1.Website{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1alpha1.SchemeGroupVersion.String(),
			Kind:       "Website",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
		},
		Spec: appsv1alpha1.WebsiteSpec{
			Image:    "nginx:1.27",
			Replicas: &replicas,
			Port:     8080,
		},
		Status: appsv1alpha1.WebsiteStatus{
			ReadyReplicas: 2,
			Phase:         appsv1alpha1.WebsitePhaseAvailable,
		},
	}

	out := WebsiteV1Alpha1ToV1(in)

	if out.APIVersion != appsv1.SchemeGroupVersion.String() {
		t.Fatalf("APIVersion = %q, want %q", out.APIVersion, appsv1.SchemeGroupVersion.String())
	}
	if out.Spec.ServicePort != in.Spec.Port {
		t.Fatalf("servicePort = %d, want %d", out.Spec.ServicePort, in.Spec.Port)
	}
	if out.Spec.Replicas == nil || *out.Spec.Replicas != replicas {
		t.Fatalf("replicas = %v, want %d", out.Spec.Replicas, replicas)
	}
	if out.Status.ReadyReplicas != in.Status.ReadyReplicas {
		t.Fatalf("readyReplicas = %d, want %d", out.Status.ReadyReplicas, in.Status.ReadyReplicas)
	}
}

func TestWebsiteV1ToV1Alpha1(t *testing.T) {
	replicas := int32(5)
	in := &appsv1.Website{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "Website",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
		},
		Spec: appsv1.WebsiteSpec{
			Image:       "nginx:1.28",
			Replicas:    &replicas,
			ServicePort: 9090,
		},
		Status: appsv1.WebsiteStatus{
			ReadyReplicas: 4,
			Phase:         appsv1.WebsitePhaseDegraded,
		},
	}

	out := WebsiteV1ToV1Alpha1(in)

	if out.APIVersion != appsv1alpha1.SchemeGroupVersion.String() {
		t.Fatalf("APIVersion = %q, want %q", out.APIVersion, appsv1alpha1.SchemeGroupVersion.String())
	}
	if out.Spec.Port != in.Spec.ServicePort {
		t.Fatalf("port = %d, want %d", out.Spec.Port, in.Spec.ServicePort)
	}
	if out.Spec.Replicas == nil || *out.Spec.Replicas != replicas {
		t.Fatalf("replicas = %v, want %d", out.Spec.Replicas, replicas)
	}
	if out.Status.Phase != in.Status.Phase {
		t.Fatalf("phase = %q, want %q", out.Status.Phase, in.Status.Phase)
	}
}
