package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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

	nodes, err := clientset.CoreV1().Nodes().List(
		context.Background(),
		metav1.ListOptions{},
	)
	if err != nil {
		log.Fatalf("failed to list nodes: %v", err)
	}

	for _, node := range nodes.Items {
		fmt.Printf("Node Name: %s\n", node.Name)
		fmt.Printf("Ready Status: %s\n", getNodeReadyStatus(node.Status.Conditions))
		fmt.Printf("Finalizers: %v\n", node.Finalizers)
		fmt.Println("---")
	}
}

func getNodeReadyStatus(conditions []corev1.NodeCondition) string {
	for _, condition := range conditions {
		if condition.Type == corev1.NodeReady {
			return string(condition.Status)
		}
	}

	return "Unknown"
}