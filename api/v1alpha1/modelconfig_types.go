package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SecretKeyRef struct {
	// Name of a Secret in kato's own namespace.
	Name string `json:"name"`
	Key  string `json:"key"`
}

type ModelConfigSpec struct {
	// Default marks this ModelConfig as the one used when a UseCase has no
	// modelConfigRef. If multiple are default, the lexicographically-first
	// name wins.
	Default bool   `json:"default,omitempty"`
	BaseURL string `json:"baseURL"`
	Model   string `json:"model"`
	// +optional
	APIKeySecretRef *SecretKeyRef `json:"apiKeySecretRef,omitempty"`
	// +kubebuilder:default=2048
	MaxTokens int `json:"maxTokens,omitempty"`
	// Temperature as a string because CRDs forbid floats; parsed as float64.
	// +kubebuilder:default="0"
	Temperature string `json:"temperature,omitempty"`
}

type ModelConfigStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Default",type=boolean,JSONPath=`.spec.default`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
type ModelConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelConfigSpec   `json:"spec,omitempty"`
	Status            ModelConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ModelConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelConfig{}, &ModelConfigList{})
}
