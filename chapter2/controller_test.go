package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestIsPodCrashEvent(t *testing.T) {
	tests := []struct {
		name  string
		event *corev1.Event
		want  bool
	}{
		{
			name: "pod backoff warning",
			event: &corev1.Event{
				Type:           corev1.EventTypeWarning,
				Reason:         "BackOff",
				Message:        "Back-off restarting failed container app in pod demo",
				InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "demo"},
			},
			want: true,
		},
		{
			name: "pod crashloop message",
			event: &corev1.Event{
				Type:           corev1.EventTypeWarning,
				Reason:         "Failed",
				Message:        "container app is in CrashLoopBackOff",
				InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "demo"},
			},
			want: true,
		},
		{
			name: "normal pod event",
			event: &corev1.Event{
				Type:           corev1.EventTypeNormal,
				Reason:         "Pulled",
				Message:        "Successfully pulled image",
				InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "demo"},
			},
			want: false,
		},
		{
			name: "node backoff event",
			event: &corev1.Event{
				Type:           corev1.EventTypeWarning,
				Reason:         "BackOff",
				Message:        "Back-off restarting failed container",
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "node-1"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPodCrashEvent(tt.event); got != tt.want {
				t.Fatalf("isPodCrashEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}
