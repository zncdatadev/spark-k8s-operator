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

// https://book.kubebuilder.io/reference/generating-crd
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +operator-sdk:csv:customresourcedefinitions:displayName="Spark History Server"
// This annotation provides a hint for OLM which resources are managed by OpenTelemetryCollector kind.
// It's not mandatory to list all resources.
// https://sdk.operatorframework.io/docs/olm-integration/generation/#csv-fields
// https://sdk.operatorframework.io/docs/building-operators/golang/references/markers/
// +operator-sdk:csv:customresourcedefinitions:resources={{Deployment,app/v1},{Service,v1},{Pod,v1},{ConfigMap,v1},{PersistentVolumeClaim,v1},{PersistentVolume,v1},{PodDisruptionBudget,v1},{Ingress,v1}}

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
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	//+kubebuilder:validation:Optional
	Image *ImageSpec `json:"image,omitempty"`

	// spark history server cluster config
	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	ClusterConfig *ClusterConfigSpec `json:"clusterConfig,omitempty"`

	// spark history server role spec
	// +kubebuilder:validation:Required
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	SparkHistory *RoleSpec `json:"sparkHistory"`
}

type ClusterConfigSpec struct {
	// +kubebuilder:validation:Optional
	S3Bucket *S3BucketSpec `json:"s3Bucket,omitempty"`

	// +kubebuilder:validation:Optional
	Listener *ListenerSpec `json:"listener,omitempty"`

	// +kubebuilder:validation:Optional
	Ingress *IngressSpec `json:"ingress,omitempty"`
}

type ImageSpec struct {
	// +kubebuilder:validation=Optional
	// +kubebuilder:default=docker.io/apache/hive
	Repository string `json:"repository,omitempty"`

	// +kubebuilder:validation=Optional
	// +kubebuilder:default="4.0.0-beta-1"
	Tag string `json:"tag,omitempty"`

	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +kubebuilder:default=IfNotPresent
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}
type RoleSpec struct {

	// +kubebuilder:validation:Optional
	Config *ConfigSpec `json:"config,omitempty"`

	RoleGroups map[string]*RoleGroupSpec `json:"roleGroups,omitempty"`

	PodDisruptionBudget *PodDisruptionBudgetSpec `json:"podDisruptionBudget,omitempty"`

	// +kubebuilder:validation:Optional
	CommandArgsOverrides []string `json:"commandArgsOverrides,omitempty"`
	// +kubebuilder:validation:Optional
	ConfigOverrides *ConfigOverridesSpec `json:"configOverrides,omitempty"`
	// +kubebuilder:validation:Optional
	EnvOverrides map[string]string `json:"envOverrides,omitempty"`
	//// +kubebuilder:validation:Optional
	//PodOverride corev1.PodSpec `json:"podOverride,omitempty"`
}

type ConfigOverridesSpec struct {
	SparkConfig map[string]string `json:"spark-defaults.conf,omitempty"`
}

type ConfigSpec struct {
	// +kubebuilder:validation:Optional
	Resources *ResourcesSpec `json:"resources,omitempty"`

	// +kubebuilder:validation:Optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext"`

	// +kubebuilder:validation:Optional
	Affinity *corev1.Affinity `json:"affinity"`

	// +kubebuilder:validation:Optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	Tolerations []corev1.Toleration `json:"tolerations"`

	// +kubebuilder:validation:Optional
	PodDisruptionBudget *PodDisruptionBudgetSpec `json:"podDisruptionBudget,omitempty"`

	// +kubebuilder:validation:Optional
	StorageClass string `json:"storageClass,omitempty"`
	// +kubebuilder:default="10Gi"
	StorageSize string `json:"size,omitempty"`
	// +kubebuilder:validation:Optional
	History *HistorySpec `json:"history"`

	// +kubebuilder:validation:Optional
	EventLog *EventLogSpec `json:"eventLog"`

	// +kubebuilder:validation:Optional
	Logging *ContainerLoggingSpec `json:"logging,omitempty"`
}

type PodDisruptionBudgetSpec struct {
	// +kubebuilder:validation:Optional
	MinAvailable int32 `json:"minAvailable,omitempty"`

	// +kubebuilder:validation:Optional
	MaxUnavailable int32 `json:"maxUnavailable,omitempty"`
}

type RoleGroupSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=1
	Replicas int32 `json:"replicas,omitempty"`

	Config *ConfigSpec `json:"config,omitempty"`

	// +kubebuilder:validation:Optional
	CommandArgsOverrides []string `json:"commandArgsOverrides,omitempty"`
	// +kubebuilder:validation:Optional
	ConfigOverrides *ConfigOverridesSpec `json:"configOverrides,omitempty"`
	// +kubebuilder:validation:Optional
	EnvOverrides map[string]string `json:"envOverrides,omitempty"`
	//// +kubebuilder:validation:Optional
	//PodOverride corev1.PodSpec `json:"podOverride,omitempty"`
}

type ListenerSpec struct {
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// +kubebuilder:validation:enum=ClusterIP;NodePort;LoadBalancer;ExternalName
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9083
	Port int32 `json:"port,omitempty"`
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

type S3BucketSpec struct {
	// S3 bucket name with S3Bucket
	// +kubebuilder:validation=Optional
	Reference *string `json:"reference"`

	// +kubebuilder:validation=Optional
	Inline *S3BucketInlineSpec `json:"inline,omitempty"`

	// +kubebuilder:validation=Optional
	// +kubebuilder:default=20
	MaxConnect int `json:"maxConnect"`

	// +kubebuilder:validation=Optional
	PathStyleAccess bool `json:"pathStyle_access"`
}

type S3BucketInlineSpec struct {

	// +kubeBuilder:validation=Required
	Bucket string `json:"bucket"`

	// +kubebuilder:validation=Optional
	// +kubebuilder:default="us-east-1"
	Region string `json:"region,omitempty"`

	// +kubebuilder:validation=Required
	Endpoints string `json:"endpoints"`

	// +kubebuilder:validation=Optional
	// +kubebuilder:default=false
	SSL bool `json:"ssl,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=false
	PathStyle bool `json:"pathStyle,omitempty"`

	// +kubebuilder:validation=Optional
	AccessKey string `json:"accessKey,omitempty"`

	// +kubebuilder:validation=Optional
	SecretKey string `json:"secretKey,omitempty"`
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
