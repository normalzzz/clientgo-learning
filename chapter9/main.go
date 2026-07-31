package main

import (
    "context"
    "fmt"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/tools/clientcmd"
)

func main() {
    // Load kubeconfig
    config, err := clientcmd.BuildConfigFromFlags("",
        clientcmd.RecommendedHomeFile)
    if err != nil {
        panic(err)
    }

    // Create clientset
    clientset, err := kubernetes.NewForConfig(config)
    if err != nil {
        panic(err)
    }

    ctx := context.Background()

    // Get the owner deployment
    deployment, err := clientset.AppsV1().Deployments("default").
        Get(ctx, "netshoot", metav1.GetOptions{})
    if err != nil {
        panic(err)
    }

    // Create owner reference
    ownerRef := metav1.OwnerReference{
        APIVersion:         "apps/v1",
        Kind:               "Deployment",
        Name:               deployment.Name,
        UID:                deployment.UID,
        Controller:         boolPtr(false),
        BlockOwnerDeletion: boolPtr(true),
    }

    // Create ConfigMap with owner reference
    configMap := &corev1.ConfigMap{
        ObjectMeta: metav1.ObjectMeta{
            Name:            "netshoot-config",
            Namespace:       "default",
            OwnerReferences: []metav1.OwnerReference{ownerRef},
        },
        Data: map[string]string{
            "config.yaml": "key: value",
        },
    }

    // Create the ConfigMap
    created, err := clientset.CoreV1().ConfigMaps("default").
        Create(ctx, configMap, metav1.CreateOptions{})
    if err != nil {
        panic(err)
    }

    fmt.Printf("Created ConfigMap %s with owner reference\n", created.Name)
}

func boolPtr(b bool) *bool {
    return &b
}
