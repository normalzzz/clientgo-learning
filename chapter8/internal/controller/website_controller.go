/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	appsv1alpha1 "github.com/normalzzz/clientgo-learning/chapter8/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	websiteLabel   = "apps.clientgo-learning.io/website"
	controllerName = "website-controller"
)

// WebsiteReconciler reconciles a Website object
type WebsiteReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps.clientgo-learning.io,resources=websites,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.clientgo-learning.io,resources=websites/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.clientgo-learning.io,resources=websites/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Website object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *WebsiteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	website := &appsv1alpha1.Website{}
	if err := r.Get(ctx, req.NamespacedName, website); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get Website %s: %w", req.NamespacedName, err)
	}

	deployment, err := r.reconcileDeployment(ctx, website)
	if err != nil {
		log.Error(err, "unable to reconcile Deployment")
		return ctrl.Result{}, err
	}
	if err := r.reconcileService(ctx, website); err != nil {
		log.Error(err, "unable to reconcile Service")
		return ctrl.Result{}, err
	}
	if err := r.updateStatus(ctx, website, deployment); err != nil {
		log.Error(err, "unable to update Website status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *WebsiteReconciler) reconcileDeployment(
	ctx context.Context,
	website *appsv1alpha1.Website,
) (*appsv1.Deployment, error) {
	desired := desiredDeployment(website)
	current := &appsv1.Deployment{}
	key := client.ObjectKeyFromObject(desired)

	if err := r.Get(ctx, key, current); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		if err := controllerutil.SetControllerReference(website, desired, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		return desired, nil
	}
	if !metav1.IsControlledBy(current, website) {
		return nil, fmt.Errorf(
			"Deployment %s/%s already exists and is not controlled by Website",
			website.Namespace,
			website.Name,
		)
	}

	updated := current.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec.Replicas = desired.Spec.Replicas
	updated.Spec.Template.Labels = desired.Spec.Template.Labels
	updated.Spec.Template.Spec.Containers = desired.Spec.Template.Spec.Containers
	if reflect.DeepEqual(current.Labels, updated.Labels) &&
		reflect.DeepEqual(current.Spec, updated.Spec) {
		return current, nil
	}
	if err := r.Update(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func (r *WebsiteReconciler) reconcileService(
	ctx context.Context,
	website *appsv1alpha1.Website,
) error {
	desired := desiredService(website)
	current := &corev1.Service{}
	key := client.ObjectKeyFromObject(desired)

	if err := r.Get(ctx, key, current); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if err := controllerutil.SetControllerReference(website, desired, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, desired)
	}
	if !metav1.IsControlledBy(current, website) {
		return fmt.Errorf(
			"Service %s/%s already exists and is not controlled by Website",
			website.Namespace,
			website.Name,
		)
	}
	if reflect.DeepEqual(current.Labels, desired.Labels) &&
		reflect.DeepEqual(current.Spec.Ports, desired.Spec.Ports) &&
		reflect.DeepEqual(current.Spec.Selector, desired.Spec.Selector) {
		return nil
	}

	updated := current.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec.Ports = desired.Spec.Ports
	updated.Spec.Selector = desired.Spec.Selector
	return r.Update(ctx, updated)
}

func (r *WebsiteReconciler) updateStatus(
	ctx context.Context,
	website *appsv1alpha1.Website,
	deployment *appsv1.Deployment,
) error {
	desiredReplicas := replicasFor(website)
	readyReplicas := deployment.Status.ReadyReplicas
	phase := appsv1alpha1.WebsitePhasePending
	if readyReplicas >= desiredReplicas {
		phase = appsv1alpha1.WebsitePhaseAvailable
	} else if readyReplicas > 0 {
		phase = appsv1alpha1.WebsitePhaseDegraded
	}

	if website.Status.ReadyReplicas == readyReplicas && website.Status.Phase == phase {
		return nil
	}
	updated := website.DeepCopy()
	updated.Status.ReadyReplicas = readyReplicas
	updated.Status.Phase = phase
	return r.Status().Update(ctx, updated)
}

func desiredDeployment(website *appsv1alpha1.Website) *appsv1.Deployment {
	labels := labelsFor(website)
	replicas := replicasFor(website)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      website.Name,
			Namespace: website.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "website",
						Image: website.Spec.Image,
						Ports: []corev1.ContainerPort{{
							Name:          "http",
							ContainerPort: portFor(website),
						}},
					}},
				},
			},
		},
	}
}

func desiredService(website *appsv1alpha1.Website) *corev1.Service {
	labels := labelsFor(website)
	port := portFor(website)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      website.Name,
			Namespace: website.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
			}},
		},
	}
}

func labelsFor(website *appsv1alpha1.Website) map[string]string {
	return map[string]string{
		managedByLabel: controllerName,
		websiteLabel:   website.Name,
	}
}

func replicasFor(website *appsv1alpha1.Website) int32 {
	if website.Spec.Replicas == nil {
		return 1
	}
	return *website.Spec.Replicas
}

func portFor(website *appsv1alpha1.Website) int32 {
	if website.Spec.Port == 0 {
		return 80
	}
	return website.Spec.Port
}

// func mapToWebsite(ctx context.Context,obj client.Object,) []reconcile.Request {
// 	var requests []reconcile.Request
// 	website, ok := obj.(*appsv1alpha1.Website)
// 	if !ok {
// 		return requests
// 	}
// 	// Map the website object to a reconcile.Request
// 	req := reconcile.Request{
// 		NamespacedName: client.ObjectKey{
// 			Namespace: website.Namespace,
// 			Name:      website.Name,
// 		},
// 	}
// 	requests = append(requests, req)
// 	return requests
// }

// SetupWithManager sets up the controller with the Manager.
func (r *WebsiteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1alpha1.Website{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("website").
		Complete(r)
}
