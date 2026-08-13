// Package v1alpha1 defines the two AgentCell CRDs: Cell (a project's
// resident instance) and Session (one disposable work session inside it).
// +groupName=agentcell.io
// +kubebuilder:object:generate=true
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion identifies the API group served by these types.
	GroupVersion = schema.GroupVersion{Group: "agentcell.io", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a runtime scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&Cell{}, &CellList{}, &Session{}, &SessionList{})
}
