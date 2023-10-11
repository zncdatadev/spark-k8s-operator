/*
Copyright 2023 zncdata-labs.

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SparkHistoryServerSpec defines the desired state of SparkHistoryServer
type SparkHistoryServerSpec struct {
	// +kubebuilder:validation:Required
	Image *ImageSpec `json:"image"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas,omitempty"`

	// +kubebuilder:validation:Required
	Resources *corev1.ResourceRequirements `json:"resources"`

	// +kubebuilder:validation:Required
	SecurityContext *corev1.PodSecurityContext `json:"securityContext"`

	// +kubebuilder:validation:Required
	Persistence *PersistenceSpec `json:"persistence"`

	// +kubebuilder:validation:Required
	Ingress *IngressSpec `json:"ingress"`

	// +kubebuilder:validation:Required
	Service *ServiceSpec `json:"service"`

	// +kubebuilder:validation:Optional
	Labels map[string]string `json:"labels"`

	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations"`
}

func (sparkHistory *SparkHistoryServer) GetNameWithSuffix(suffix string) string {
	// return sparkHistory.GetName() + rand.String(5) + suffix
	return sparkHistory.GetName() + suffix
}

type ImageSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="bitnami/spark"
	Repository string `json:"repository,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="3.4.1"
	Tag string `json:"tag,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=IfNotPresent
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

type PersistenceSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=true
	Enable bool `json:"enable,omitempty"`
	// +kubebuilder:validation:Optional
	ExistingClaim *string `json:"existingClaim,omitempty"`
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="10Gi"
	StorageSize string `json:"storageSize,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="ReadWriteOnce"
	AccessMode string `json:"accessMode,omitempty"`
	// +kubebuilder:validation:Optional
	StorageClassName *string `json:"storageClassName,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=Filesystem
	VolumeMode *corev1.PersistentVolumeMode `json:"volumeMode,omitempty"`
}

func (p *PersistenceSpec) Existing() bool {
	return p.ExistingClaim != nil
}

func (sparkHistory *SparkHistoryServer) GetPvcName() string {
	if sparkHistory.Spec.Persistence != nil && sparkHistory.Spec.Persistence.Existing() {
		return *sparkHistory.Spec.Persistence.ExistingClaim
	}

	return sparkHistory.GetNameWithSuffix("-pvc")
}

type IngressSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`
	// +kubebuilder:validation:Optional
	TLS *networkingv1.IngressTLS `json:"tls,omitempty"`
	// +kubebuilder:validation:Optional
	Annotations map[string]string   `json:"annotations,omitempty"`
	Hosts       []*IngressHostsSpec `json:"hosts"`
}

type IngressHostsSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="spark-history-server.example.coom"
	Host string `json:"host,omitempty"`
	// +kubebuilder:validation:Required
	Paths []*IngressHostPathsSpec `json:"paths"`
}

type IngressHostPathsSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="/"
	Path string `json:"path,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=ImplementationSpecific
	PathType *networkingv1.PathType `json:"pathType,omitempty"`
}

type ServiceSpec struct {
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +kubebuilder:validation:enum=ClusterIP;NodePort;LoadBalancer;ExternalName
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=18080
	Port int32 `json:"port"`
}

// SetStatusCondition updates the status condition using the provided arguments.
// If the condition already exists, it updates the condition; otherwise, it appends the condition.
// If the condition status has changed, it updates the condition's LastTransitionTime.
func (sparkHistory *SparkHistoryServer) SetStatusCondition(condition metav1.Condition) {
	// if the condition already exists, update it
	existingCondition := apimeta.FindStatusCondition(sparkHistory.Status.Conditions, condition.Type)
	if existingCondition == nil {
		condition.ObservedGeneration = sparkHistory.GetGeneration()
		condition.LastTransitionTime = metav1.Now()
		sparkHistory.Status.Conditions = append(sparkHistory.Status.Conditions, condition)
	} else if existingCondition.Status != condition.Status || existingCondition.Reason != condition.Reason || existingCondition.Message != condition.Message {
		existingCondition.Status = condition.Status
		existingCondition.Reason = condition.Reason
		existingCondition.Message = condition.Message
		existingCondition.ObservedGeneration = sparkHistory.GetGeneration()
		existingCondition.LastTransitionTime = metav1.Now()
	}
}

// InitStatusConditions initializes the status conditions to the provided conditions.
func (sparkHistory *SparkHistoryServer) InitStatusConditions() {
	sparkHistory.Status.Conditions = []metav1.Condition{}
	sparkHistory.SetStatusCondition(metav1.Condition{
		Type:               ConditionTypeProgressing,
		Status:             metav1.ConditionTrue,
		Reason:             ConditionReasonPreparing,
		Message:            "SparkHistoryServer is preparing",
		ObservedGeneration: sparkHistory.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	})
	sparkHistory.SetStatusCondition(metav1.Condition{
		Type:               ConditionTypeAvailable,
		Status:             metav1.ConditionFalse,
		Reason:             ConditionReasonPreparing,
		Message:            "SparkHistoryServer is preparing",
		ObservedGeneration: sparkHistory.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	})
}

// SparkHistoryServerStatus defines the observed state of SparkHistoryServer
type SparkHistoryServerStatus struct {
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"condition,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SparkHistoryServer is the Schema for the sparkhistoryservers API
type SparkHistoryServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SparkHistoryServerSpec   `json:"spec,omitempty"`
	Status SparkHistoryServerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SparkHistoryServerList contains a list of SparkHistoryServer
type SparkHistoryServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SparkHistoryServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SparkHistoryServer{}, &SparkHistoryServerList{})
}
