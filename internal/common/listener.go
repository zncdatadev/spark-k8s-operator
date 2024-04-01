package common

import (
	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

type ListenerInfo[T InstanceAttributes] struct {
	InstanceAttributes T
	ResourceClient     ResourceClient
}

func NewListenerInfo(ia InstanceAttributes, resourceClient ResourceClient) *ListenerInfo[InstanceAttributes] {
	return &ListenerInfo[InstanceAttributes]{
		InstanceAttributes: ia,
		ResourceClient:     resourceClient,
	}
}

func (l *ListenerInfo[T]) GetListenerClassName() sparkv1alpha1.ListenerClass {
	return l.InstanceAttributes.GetClusterConfig().ListenerClass
}

// TODO: move ListenerClass mapping to ServiceType logic to listener project
// Finnaly, we should use it from operator-go project
func (l *ListenerInfo[T]) GetServiceType() corev1.ServiceType {
	switch l.GetListenerClassName() {
	case sparkv1alpha1.ClusterInternal:
		return corev1.ServiceTypeClusterIP

	case sparkv1alpha1.ExternalUnstable:
		return corev1.ServiceTypeNodePort

	case sparkv1alpha1.ExternalStable:
		return corev1.ServiceTypeLoadBalancer

	default:
		return corev1.ServiceTypeLoadBalancer
	}
}
