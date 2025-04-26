/*
Copyright 2023 zncdatadev.

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
	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	s3v1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/s3/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultRepository     = "quay.io/zncdatadev"
	DefaultProductVersion = "3.5.1"
	DefaultProductName    = "spark-k8s"
)

// https://book.kubebuilder.io/reference/generating-crd
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SparkHistoryServer is the Schema for the sparkhistoryservers API
type SparkHistoryServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SparkHistoryServerSpec `json:"spec,omitempty"`
	Status status.Status          `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SparkHistoryServerList contains a list of SparkHistoryServer
type SparkHistoryServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SparkHistoryServer `json:"items"`
}

// SparkHistoryServerSpec defines the desired state of SparkHistoryServer
type SparkHistoryServerSpec struct {
	// +kubebuilder:validation:Optional
	// +default:value={"repo": "quay.io/zncdatadev", "pullPolicy": "IfNotPresent"}
	Image *ImageSpec `json:"image,omitempty"`

	// spark history server cluster config
	// +kubebuilder:validation:Required
	ClusterConfig *ClusterConfigSpec `json:"clusterConfig"`

	// +kubebuilder:validation:Optional
	ClusterOperation *commonsv1alpha1.ClusterOperationSpec `json:"clusterOperation,omitempty"`

	// spark history server role spec
	// +kubebuilder:validation:Required
	Node *RoleSpec `json:"node"`
}

type ClusterConfigSpec struct {
	// +kubebuilder:validation:Optional
	Authentication *AuthenticationSpec `json:"authentication,omitempty"`

	// +kubebuilder:validation:Required
	LogFileDirectory *LogFileDirectorySpec `json:"logFileDirectory"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=cluster-internal
	// +kubebuilder:validation:Enum=cluster-internal;external-unstable;external-stable
	ListenerClass string `json:"listenerClass,omitempty"`

	// +kubebuilder:validation:Optional
	VectorAggregatorConfigMapName string `json:"vectorAggregatorConfigMapName,omitempty"`
}

type AuthenticationSpec struct {
	// +kubebuilder:validation:Required
	AuthenticationClass string `json:"authenticationClass"`

	// +kubebuilder:validation:Optional
	Oidc *OidcSpec `json:"oidc,omitempty"`
}

// OidcSpec defines the OIDC spec.
type OidcSpec struct {
	// OIDC client credentials secret. It must contain the following keys:
	//   - `CLIENT_ID`: The client ID of the OIDC client.
	//   - `CLIENT_SECRET`: The client secret of the OIDC client.
	// credentials will omit to pod environment variables.
	// +kubebuilder:validation:Required
	ClientCredentialsSecret string `json:"clientCredentialsSecret"`

	// +kubebuilder:validation:Optional
	ExtraScopes []string `json:"extraScopes,omitempty"`
}

type LogFileDirectorySpec struct {
	// +kubebuilder:validation:Required
	S3 *S3Spec `json:"s3"`
}

type S3Spec struct {
	// +kubebuilder:validation:Required
	Bucket *BucketSpec `json:"bucket"`

	// +kubebuilder:validation:Required
	Prefix string `json:"prefix"`
}

type BucketSpec struct {
	// +kubebuilder:validation:Optional
	Inline *s3v1alpha1.S3BucketSpec `json:"inline,omitempty"`

	// +kubebuilder:validation:Optional
	Reference string `json:"reference,omitempty"`
}

type ImageSpec struct {
	// +kubebuilder:validation:Optional
	Custom string `json:"custom,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=quay.io/zncdatadev
	Repo string `json:"repo,omitempty"`

	// +kubebuilder:validation:Optional
	KubedoopVersion string `json:"kubedoopVersion,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="3.5.1"
	ProductVersion string `json:"productVersion,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=IfNotPresent
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	PullSecretName string `json:"pullSecretName,omitempty"`
}

type RoleSpec struct {
	*commonsv1alpha1.OverridesSpec `json:",inline"`

	// +kubebuilder:validation:Optional
	Config *ConfigSpec `json:"config,omitempty"`

	RoleGroups map[string]*RoleGroupSpec `json:"roleGroups,omitempty"`

	// +kubebuilder:validation:Optional
	RoleConfig *commonsv1alpha1.RoleConfigSpec `json:"roleConfig,omitempty"`
}

type ConfigSpec struct {
	*commonsv1alpha1.RoleGroupConfigSpec `json:",inline"`

	// +kubebuilder:validation:Optional
	Cleaner *bool `json:"cleaner,omitempty"`
}

type RoleGroupSpec struct {
	*commonsv1alpha1.OverridesSpec `json:",inline"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=1
	Replicas *int32 `json:"replicas,omitempty"`

	// +kubebuilder:validation:Optional
	Config *ConfigSpec `json:"config,omitempty"`
}

type PodOverridesSpec struct {
}

func init() {
	SchemeBuilder.Register(&SparkHistoryServer{}, &SparkHistoryServerList{})
}
