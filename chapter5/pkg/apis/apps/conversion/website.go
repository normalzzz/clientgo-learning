package conversion

import (
	appsv1 "github.com/normalzzz/clientgo-learning/chapter5/pkg/apis/apps/v1"
	appsv1alpha1 "github.com/normalzzz/clientgo-learning/chapter5/pkg/apis/apps/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const websiteKind = "Website"

func WebsiteV1Alpha1ToV1(in *appsv1alpha1.Website) *appsv1.Website {
	out := &appsv1.Website{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       websiteKind,
		},
		ObjectMeta: *in.ObjectMeta.DeepCopy(),
		Spec: appsv1.WebsiteSpec{
			Image:       in.Spec.Image,
			Replicas:    copyInt32Ptr(in.Spec.Replicas),
			ServicePort: in.Spec.Port,
		},
		Status: appsv1.WebsiteStatus{
			ReadyReplicas: in.Status.ReadyReplicas,
			Phase:         in.Status.Phase,
		},
	}
	return out
}

func WebsiteV1ToV1Alpha1(in *appsv1.Website) *appsv1alpha1.Website {
	out := &appsv1alpha1.Website{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1alpha1.SchemeGroupVersion.String(),
			Kind:       websiteKind,
		},
		ObjectMeta: *in.ObjectMeta.DeepCopy(),
		Spec: appsv1alpha1.WebsiteSpec{
			Image:    in.Spec.Image,
			Replicas: copyInt32Ptr(in.Spec.Replicas),
			Port:     in.Spec.ServicePort,
		},
		Status: appsv1alpha1.WebsiteStatus{
			ReadyReplicas: in.Status.ReadyReplicas,
			Phase:         in.Status.Phase,
		},
	}
	return out
}

func copyInt32Ptr(in *int32) *int32 {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
