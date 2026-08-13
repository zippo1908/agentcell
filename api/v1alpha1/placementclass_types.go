package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A PlacementClass is a machine pool a cluster administrator is willing to
// offer projects.
//
// It exists because the first design had the console read the cluster's own
// node labels and let a Cell maintainer pick any of them — then DERIVED the
// tolerations needed to land there, taints included. That crossed a trust
// boundary: "maintainer of a project" is not "administrator of the cluster",
// and the two are routinely different people. A maintainer could name
// node-role.kubernetes.io/control-plane, and the platform would helpfully
// tolerate the taint that exists precisely to keep workloads off it — while
// the workload in question runs a model's output against a repository.
//
// Taints are a cluster administrator's refusal. A platform that reads one
// and writes the matching toleration has not implemented placement; it has
// implemented a bypass.
//
// So the offer is explicit. An administrator writes the classes, including
// any tolerations, by hand. Maintainers choose among them and can express
// nothing else.

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pc
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Selector",type=string,JSONPath=`.spec.nodeSelectorText`
type PlacementClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PlacementClassSpec `json:"spec,omitempty"`
}

type PlacementClassSpec struct {
	// +kubebuilder:validation:MaxLength=200
	DisplayName string `json:"displayName,omitempty"`
	// Description is what a maintainer reads when choosing — "big memory,
	// shared", "GPU, ask first".
	// +kubebuilder:validation:MaxLength=1000
	Description string `json:"description,omitempty"`
	// NodeSelector is where this class puts a Cell.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations are written by the administrator offering the class, never
	// derived from what the nodes happen to be tainted with. Offering a
	// tainted pool is a decision; discovering the taint is not.
	//
	// NoExecute is deliberately not special-cased here — an administrator
	// may still write one — but nothing in the platform will ever add one on
	// somebody's behalf.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// NodeSelectorText is the selector rendered for a printcolumn.
	NodeSelectorText string `json:"nodeSelectorText,omitempty"`
}

// +kubebuilder:object:root=true
type PlacementClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlacementClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PlacementClass{}, &PlacementClassList{})
}
