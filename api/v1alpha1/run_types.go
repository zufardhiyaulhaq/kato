package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ManagedByLabel marks a Run created by the REST API as an audit record. The
// RunReconciler skips Runs carrying it (value ManagedByAPI) so API-written Runs
// are never re-executed; externally-created Runs (kubectl/GitOps) omit it.
const (
	ManagedByLabel = "kato.zufardhiyaulhaq.com/managed-by"
	ManagedByAPI   = "api"
)

type RunSpec struct {
	UseCase string            `json:"useCase"`
	Inputs  map[string]string `json:"inputs,omitempty"`
}

type RunStep struct {
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=completed;skipped;failed
	Outcome string `json:"outcome"`
	// Reason explains skipped/failed outcomes.
	Reason string `json:"reason,omitempty"`
	// Outputs holds all declared outputs of the step (unfiltered).
	// +kubebuilder:pruning:PreserveUnknownFields
	Outputs *apiextensionsv1.JSON `json:"outputs,omitempty"`
	Error   string                `json:"error,omitempty"`
	// Iterations holds per-item results for a forEach step.
	Iterations []RunStepIteration `json:"iterations,omitempty"`
	// Note records forEach truncation, e.g. "matched 12, checked 3".
	Note string `json:"note,omitempty"`
}

// RunStepIteration records one forEach iteration's result.
type RunStepIteration struct {
	Item map[string]string `json:"item,omitempty"`
	// +kubebuilder:validation:Enum=completed;failed
	Outcome string `json:"outcome"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Outputs *apiextensionsv1.JSON `json:"outputs,omitempty"`
	Error   string                `json:"error,omitempty"`
}

type RunStatus struct {
	// +kubebuilder:validation:Enum=Running;Succeeded;PartiallySucceeded;Failed
	Phase       string       `json:"phase,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	Steps       []RunStep    `json:"steps,omitempty"`
	Summary     string       `json:"summary,omitempty"`
	// Note records a reconciler-level message: a validation failure reason for an
	// externally-created Run, or the reap note when a stuck Running Run is failed.
	Note string `json:"note,omitempty"`
	// Warning is set when the summary could not be produced (LLM down, no
	// default ModelConfig, ...) while step outputs are still valid.
	Warning string `json:"warning,omitempty"`
	// ModelConfig records which ModelConfig produced the summary.
	ModelConfig string `json:"modelConfig,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="UseCase",type=string,JSONPath=`.spec.useCase`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Run struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RunSpec   `json:"spec,omitempty"`
	Status            RunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Run `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Run{}, &RunList{})
}
