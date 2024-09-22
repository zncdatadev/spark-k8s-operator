package v1alpha1

import (
	commonsv1alph1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
)

type LoggingSpec struct {
	// +kubebuilder:validation:Optional
	// Config log for containers
	// containers:
	//  - sparkHistory
	Containers map[string]commonsv1alph1.LoggingConfigSpec `json:"containers,omitempty"`
	// +kubebuilder:validation:Optional
	EnableVectorAgent *bool `json:"enableVectorAgent,omitempty"`
}
