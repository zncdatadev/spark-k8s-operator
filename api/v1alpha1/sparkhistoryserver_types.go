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
	"github.com/zncdata-labs/operator-go/pkg/image"
	"github.com/zncdata-labs/operator-go/pkg/ingress"
	"github.com/zncdata-labs/operator-go/pkg/persistence"
	"github.com/zncdata-labs/operator-go/pkg/service"
	"github.com/zncdata-labs/operator-go/pkg/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SparkHistoryServerSpec defines the desired state of SparkHistoryServer
type SparkHistoryServerSpec struct {
	// +kubebuilder:validation:Required
	Image *image.ImageSpec `json:"image"`

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
	Persistence *persistence.PersistenceSpec `json:"persistence"`

	// +kubebuilder:validation:Required
	Ingress *ingress.IngressSpec `json:"ingress"`

	// +kubebuilder:validation:Required
	Service *service.ServiceSpec `json:"service"`

	// +kubebuilder:validation:Optional
	Labels map[string]string `json:"labels"`

	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations"`

	// +kubebuilder:validation:Optional
	NodeSelector map[string]string `json:"nodeSelector"`

	// +kubebuilder:validation:Optional
	Tolerations *corev1.Toleration `json:"tolerations"`

	// +kubebuilder:validation:Optional
	Affinity *corev1.Affinity `json:"affinity"`
}

func (sparkHistory *SparkHistoryServer) GetNameWithSuffix(suffix string) string {
	return sparkHistory.GetName() + suffix
}

func (sparkHistory *SparkHistoryServer) GetPvcName() string {
	if sparkHistory.Spec.Persistence != nil && sparkHistory.Spec.Persistence.Existing() {
		return *sparkHistory.Spec.Persistence.ExistingClaim
	}

	return sparkHistory.GetNameWithSuffix("-pvc")
}

// SetStatusCondition updates the status condition using the provided arguments.
// If the condition already exists, it updates the condition; otherwise, it appends the condition.
// If the condition status has changed, it updates the condition's LastTransitionTime.
func (sparkHistory *SparkHistoryServer) SetStatusCondition(condition metav1.Condition) {
	sparkHistory.Status.SetStatusCondition(condition)
}

// InitStatusConditions initializes the status conditions to the provided conditions.
func (sparkHistory *SparkHistoryServer) InitStatusConditions() {
	sparkHistory.Status.InitStatus(sparkHistory)
	sparkHistory.Status.InitStatusConditions()
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SparkHistoryServer is the Schema for the sparkhistoryservers API
type SparkHistoryServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SparkHistoryServerSpec `json:"spec,omitempty"`
	Status status.ZncdataStatus   `json:"status,omitempty"`
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
