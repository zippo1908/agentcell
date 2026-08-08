// Hand-written deepcopy implementations (controller-gen output would be
// equivalent; regenerate with `make generate` once controller-gen is wired
// into the build).
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
)

func (in *RepoSpec) DeepCopyInto(out *RepoSpec) { *out = *in }

func (in *PreviewSpec) DeepCopyInto(out *PreviewSpec) {
	*out = *in
	if in.Command != nil {
		out.Command = make([]string, len(in.Command))
		copy(out.Command, in.Command)
	}
}

func (in *ResourceBudget) DeepCopyInto(out *ResourceBudget) { *out = *in }

func (in *ProductionSpec) DeepCopyInto(out *ProductionSpec) {
	*out = *in
	if in.Command != nil {
		out.Command = make([]string, len(in.Command))
		copy(out.Command, in.Command)
	}
}

func (in *CellSpec) DeepCopyInto(out *CellSpec) {
	*out = *in
	in.Repo.DeepCopyInto(&out.Repo)
	in.Preview.DeepCopyInto(&out.Preview)
	in.Production.DeepCopyInto(&out.Production)
	in.SessionResources.DeepCopyInto(&out.SessionResources)
}

func (in *CellStatus) DeepCopyInto(out *CellStatus) { *out = *in }

func (in *Cell) DeepCopyInto(out *Cell) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Cell) DeepCopy() *Cell {
	if in == nil {
		return nil
	}
	out := new(Cell)
	in.DeepCopyInto(out)
	return out
}

func (in *Cell) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *CellList) DeepCopyInto(out *CellList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Cell, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *CellList) DeepCopy() *CellList {
	if in == nil {
		return nil
	}
	out := new(CellList)
	in.DeepCopyInto(out)
	return out
}

func (in *CellList) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *SessionSpec) DeepCopyInto(out *SessionSpec) { *out = *in }

func (in *SessionStatus) DeepCopyInto(out *SessionStatus) {
	*out = *in
	if in.StartTime != nil {
		out.StartTime = in.StartTime.DeepCopy()
	}
}

func (in *Session) DeepCopyInto(out *Session) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Session) DeepCopy() *Session {
	if in == nil {
		return nil
	}
	out := new(Session)
	in.DeepCopyInto(out)
	return out
}

func (in *Session) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *SessionList) DeepCopyInto(out *SessionList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Session, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *SessionList) DeepCopy() *SessionList {
	if in == nil {
		return nil
	}
	out := new(SessionList)
	in.DeepCopyInto(out)
	return out
}

func (in *SessionList) DeepCopyObject() runtime.Object { return in.DeepCopy() }
