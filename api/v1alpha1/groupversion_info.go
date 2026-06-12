// Package v1alpha1 contains the kato.zufardhiyaulhaq.com/v1alpha1 API types.
// +kubebuilder:object:generate=true
// +groupName=kato.zufardhiyaulhaq.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "kato.zufardhiyaulhaq.com", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)
