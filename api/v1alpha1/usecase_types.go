package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InputDecl declares a UseCase input. v1 supports type "string" only.
type InputDecl struct {
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=string
	// +kubebuilder:default=string
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
	// Default is used when the caller omits this input. Empty means no default.
	Default string `json:"default,omitempty"`
}

// Step is one method invocation in the flow.
type Step struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	// When is a CEL expression over $() references; absent means run always.
	When string            `json:"when,omitempty"`
	With map[string]string `json:"with,omitempty"`
	// SummaryFilter lists which outputs the LLM sees.
	// nil (omitted) = all outputs; empty list = none (audit-only step).
	SummaryFilter []string `json:"summaryFilter,omitempty"`
	// ForEach references a list output of an earlier step, e.g.
	// "$(steps.crashing.pods)". When set, Method runs once per item and
	// $(item.<field>) is available in With (not in When).
	ForEach string `json:"forEach,omitempty"`
	// MaxItems caps forEach iterations. Default 5; hard ceiling 20.
	MaxItems int `json:"maxItems,omitempty"`
}

type SummarySpec struct {
	// ModelConfigRef names a ModelConfig; empty means the default ModelConfig.
	ModelConfigRef string `json:"modelConfigRef,omitempty"`
	Prompt         string `json:"prompt"`
}

type UseCaseSpec struct {
	Description string      `json:"description,omitempty"`
	Inputs      []InputDecl `json:"inputs,omitempty"`
	// +kubebuilder:validation:MinItems=1
	Steps   []Step      `json:"steps"`
	Summary SummarySpec `json:"summary"`
}

type UseCaseStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type UseCase struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UseCaseSpec   `json:"spec,omitempty"`
	Status            UseCaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type UseCaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UseCase `json:"items"`
}

func init() {
	SchemeBuilder.Register(&UseCase{}, &UseCaseList{})
}
