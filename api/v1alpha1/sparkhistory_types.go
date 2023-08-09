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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	// DefaultImageRepository is the default docker image repository for the operator
	DefaultImageRepository = "bitnami/spark"
	// DefaultImageTag is the default docker image tag for the operator
	DefaultImageTag = "3.4.0"
	// DefaultImagePullPolicy is the default docker image pull policy for the operator
	DefaultImagePullPolicy = corev1.PullIfNotPresent
	// DefaultServiceType is the default service type for the operator
	DefaultServiceType = corev1.ServiceTypeClusterIP
	// DefaultSize is the default size for the operator
	DefaultSize = "10Gi"
)

// SparkHistorySpec defines the desired state of SparkHistory
type SparkHistorySpec struct {

	// +kubebuilder:validation:Optional
	Image ImageSpec `json:"image"`

	Replicas int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	Resource *corev1.ResourceRequirements `json:"resource"`

	// +kubebuilder:validation:Optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// +kubebuilder:validation:Optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// +kubebuilder:validation:Optional
	Service *ServiceSpec `json:"service,omitempty"`

	// +kubebuilder:validation:Optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// +kubebuilder:validation:Optional
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// +kubebuilder:validation:Optional
	Ingress *IngressSpec `json:"ingress,omitempty"`
}

func (sparkHistory *SparkHistory) GetLabels() map[string]string {
	return map[string]string{
		"app": sparkHistory.Name,
	}
}

type ImageSpec struct {

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=bitnami/spark
	Repository string `json:"repository"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=latest
	Tag string `json:"tag"`

	// +kubebuilder:validation:enum=Always;Never;IfNotPresent
	// +kubebuilder:default=IfNotPresent
	PullPolicy corev1.PullPolicy `json:"pullPolicy"`
}

// GetImageTag
// get image and tag from instance, if is "" use default value then
// return <ImageRepository>:<ImageTag>
func (sparkHistory *SparkHistory) GetImageTag() string {
	image := sparkHistory.Spec.Image.Repository
	if image == "" {
		image = DefaultImageRepository
	}
	tag := sparkHistory.Spec.Image.Tag
	if tag == "" {
		tag = DefaultImageTag
	}
	return image + ":" + tag
}

func (sparkHistory *SparkHistory) GetImagePullPolicy() corev1.PullPolicy {
	pullPolicy := sparkHistory.Spec.Image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = DefaultImagePullPolicy
	}

	return pullPolicy
}

func (sparkHistory *SparkHistory) GetNameWithSuffix(suffix string) string {
	// return sparkHistory.GetName() + rand.String(5) + suffix
	return sparkHistory.GetName() + suffix
}

func (sparkHistory *SparkHistory) GetPvcName() string {
	if sparkHistory.Spec.Persistence.Existing() {
		return *sparkHistory.Spec.Persistence.ExistingClaim
	}
	return sparkHistory.GetNameWithSuffix("-pvc")
}

// get svc name
func (sparkHistory *SparkHistory) GetSvcName() string {
	return sparkHistory.GetNameWithSuffix("-svc")
}

type ServiceSpec struct {
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +kubebuilder:validation:enum=ClusterIP;NodePort;LoadBalancer;ExternalName
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=18080
	Port int32 `json:"port"`
}

func (sparkHistory *SparkHistory) GetServiceType() corev1.ServiceType {
	serviceType := sparkHistory.Spec.Service.Type
	if serviceType == "" {
		serviceType = DefaultServiceType
	}
	return serviceType
}

type PersistenceSpec struct {
	// +kubebuilder:validation:Optional
	StorageClass *string `json:"storageClass,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={ReadWriteOnce}
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// +kubebuilder:default="10Gi"
	Size string `json:"size,omitempty"`

	// +kubebuilder:validation:Optional
	ExistingClaim *string `json:"existingClaim,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=Filesystem
	VolumeMode *corev1.PersistentVolumeMode `json:"volumeMode,omitempty"`

	// +kubebuilder:validation:Optional
	Labels map[string]string `json:"labels,omitempty"`

	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

func (p *PersistenceSpec) Existing() bool {
	return p.ExistingClaim != nil
}

// GetSize
// get persistence size from instance, if is "" use default value then
// return <Size>
func (p *PersistenceSpec) GetSize() resource.Quantity {
	size := p.Size
	if size == "" {
		return resource.MustParse(DefaultSize)
	}
	return resource.MustParse(size)

}

type IngressSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=spark-history-server.example.com
	Host string `json:"host,omitempty"`
}

// SparkHistoryStatus defines the observed state of SparkHistory
type SparkHistoryStatus struct {
	Nodes      []string                    `json:"nodes"`
	Conditions []corev1.ComponentCondition `json:"conditions"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SparkHistory is the Schema for the sparkhistories API
type SparkHistory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SparkHistorySpec   `json:"spec,omitempty"`
	Status SparkHistoryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SparkHistoryList contains a list of SparkHistory
type SparkHistoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SparkHistory `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SparkHistory{}, &SparkHistoryList{})
}
