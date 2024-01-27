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
	"github.com/zncdata-labs/operator-go/pkg/status"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SparkHistoryServer is the Schema for the sparkhistoryservers API
type SparkHistoryServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SparkHistoryServerSpec `json:"spec,omitempty"`
	Status status.Status          `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SparkHistoryServerList contains a list of SparkHistoryServer
type SparkHistoryServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SparkHistoryServer `json:"items"`
}

// SparkHistoryServerSpec defines the desired state of SparkHistoryServer
type SparkHistoryServerSpec struct {
	// +kubebuilder:validation:Optional
	RoleConfig *RoleConfigSpec `json:"roleConfig"`

	// +kubebuilder:validation:Optional
	RoleGroups map[string]*RoleConfigSpec `json:"roleGroups"`
}

type RoleConfigSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	Config *ConfigSpec `json:"config"`

	// +kubebuilder:validation:Optional
	Image *ImageSpec `json:"image"`

	// +kubebuilder:validation:Optional
	Ingress *IngressSpec `json:"ingress"`

	// +kubebuilder:validation:Optional
	Service *ServiceSpec `json:"service"`

	// +kubebuilder:validation:Optional
	History *HistorySpec `json:"history"`

	// +kubebuilder:validation:Optional
	EventLog *EventLogSpec `json:"eventLog"`

	// +kubebuilder:validation:Optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// +kubebuilder:validation:Optional
	NodeSelector map[string]string `json:"nodeSelector"`

	// +kubebuilder:validation:Optional
	Affinity *corev1.Affinity `json:"affinity"`

	// +kubebuilder:validation:Optional
	Tolerations *corev1.Toleration `json:"tolerations"`

	// +kubebuilder:validation:Optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext"`

	// +kubebuilder:validation:Optional
	StorageClass *string `json:"storageClass,omitempty"`

	// +kubebuilder:default="10Gi"
	StorageSize string `json:"size,omitempty"`
}

type ConfigSpec struct {
	// +kubebuilder:validation:Optional
	Resources *corev1.ResourceRequirements `json:"resources"`

	// +kubebuilder:validation:Optional
	S3 *S3Spec `json:"s3"`
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

type IngressSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`
	// +kubebuilder:validation:Optional
	TLS *networkingv1.IngressTLS `json:"tls,omitempty"`
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="spark-history-server.example.com"
	Host string `json:"host,omitempty"`
}

type S3Spec struct {
	S3BucketSpec `json:"bucket"`

	// +kubebuilder:validation=Optional
	PathStyleAccess bool `json:"pathStyle_access"`
}

type S3BucketSpec struct {
	Reference string `json:"reference"`
}

type HistorySpec struct {
	// +kubebuilder:validation:Optional
	FsCleaner *FsCleanerSpec `json:"fsCleaner"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=20
	FsEnentLogRollingMaxFiles int32 `json:"fsEnentLogRollingMaxFiles"`
}

type FsCleanerSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=50
	MaxNum int32 `json:"maxNum,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="7d"
	MaxAge string `json:"maxAge,omitempty"`
}

type EventLogSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="/tmp/spark-events"
	Dir string `json:"dir,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="pvc"
	MountMode string `json:"mountMode,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled,omitempty"`
}

func init() {
	SchemeBuilder.Register(&SparkHistoryServer{}, &SparkHistoryServerList{})
}

func (r *SparkHistoryServer) GetNameWithSuffix(suffix string) string {
	// return sparkHistory.GetName() + rand.String(5) + suffix
	return r.GetName() + "-" + suffix
}

// SetStatusCondition updates the status condition using the provided arguments.
// If the condition already exists, it updates the condition; otherwise, it appends the condition.
// If the condition status has changed, it updates the condition's LastTransitionTime.
func (r *SparkHistoryServer) SetStatusCondition(condition metav1.Condition) {
	r.Status.SetStatusCondition(condition)
}

// InitStatusConditions initializes the status conditions to the provided conditions.
func (r *SparkHistoryServer) InitStatusConditions() {
	r.Status.InitStatus(r)
	r.Status.InitStatusConditions()
}
