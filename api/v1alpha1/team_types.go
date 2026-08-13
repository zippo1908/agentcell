package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A Team is a membership list that outlives any one project.
//
// Roles started on the Cell, which is the right place for "who works on
// THIS", and the wrong place for everything else a real group needs: a
// person joining does not join one project, and a person leaving must not be
// removed from eleven of them one at a time — the twelfth is the one that
// gets forgotten. The Cell list stays; it now sits on top of a team list
// rather than instead of one.
//
// Deliberately thin. A Team owns no quota, no credentials and no repository.
// It answers exactly one question — who may do what, by default, in the
// projects that name it — and every other grouping idea (billing, storage,
// a shared key pool) is a separate decision that should not be smuggled in
// by making this object the natural home for it.

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tm
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Members",type=integer,JSONPath=`.status.members`
// +kubebuilder:printcolumn:name="Cells",type=integer,JSONPath=`.status.cells`
type Team struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TeamSpec   `json:"spec,omitempty"`
	Status TeamStatus `json:"status,omitempty"`
}

type TeamSpec struct {
	// DisplayName is what people call it; the object name is a label-safe id.
	// +kubebuilder:validation:MaxLength=200
	DisplayName string `json:"displayName,omitempty"`
	// +kubebuilder:validation:MaxLength=1000
	Description string `json:"description,omitempty"`
	// Members are the people in the team and the role they carry into every
	// Cell that names it. A Cell may still name somebody explicitly, and
	// that entry wins — for raising a role or lowering it.
	Members []Member `json:"members,omitempty"`
}

type TeamStatus struct {
	// Members and Cells are counts, so a list page does not have to fetch
	// every team's full membership to say how big it is.
	Members int `json:"members,omitempty"`
	Cells   int `json:"cells,omitempty"`
	// CellNames is which projects this team governs. Recorded because the
	// question "what does removing this person actually affect" has to be
	// answerable before the removal, not after.
	CellNames          []string `json:"cellNames,omitempty"`
	ObservedGeneration int64    `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
type TeamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Team `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Team{}, &TeamList{})
}

// RoleOf returns a user's role in this team, or "" if they are not in it.
func (t *Team) RoleOf(userID string) Role {
	if userID == "" {
		return ""
	}
	for _, m := range t.Spec.Members {
		if m.UserID == userID {
			return m.Role
		}
	}
	return ""
}
