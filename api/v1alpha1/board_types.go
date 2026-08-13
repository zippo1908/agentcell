package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The board is where work is asked for and where it comes back.
//
// The console had pages for every noun and no place to stand: a dashboard
// counting three numbers, and a form buried two tabs deep. You could operate
// the system without ever seeing what was happening in it.
//
// A board is one stream per team. You say what you want with `@cell do the
// thing` and the answer arrives in the same place — the agent posts back
// when it settles, with its branch and its diff. `@u-…` mentions a person
// and shows up as unread for them. That is the whole feature: a working
// surface where asking and answering are the same conversation.

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=bd
// +kubebuilder:printcolumn:name="Team",type=string,JSONPath=`.spec.team`
// +kubebuilder:printcolumn:name="Posts",type=integer,JSONPath=`.spec.count`
type Board struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BoardSpec `json:"spec,omitempty"`
}

type BoardSpec struct {
	// Team this board belongs to. One board per team, named after it.
	Team string `json:"team,omitempty"`
	// Posts, oldest first, bounded.
	//
	// The whole conversation lives in one object rather than one object per
	// post: a post is not a resource anybody reconciles, and a thousand tiny
	// CRs would be a thousand watch events for something only a person
	// reads. Bounded because a Kubernetes object is not a database — old
	// posts fall off the front, and the durable record of what happened is
	// the git history the work produced, not the chat about it.
	Posts []Post `json:"posts,omitempty"`
	// NextID is the sequence for post ids, so a client can ask for
	// everything after what it already has.
	NextID int64 `json:"nextID,omitempty"`
	// Count mirrors len(Posts) for a printcolumn.
	Count int `json:"count,omitempty"`
	// Read is each member's high-water mark: the last post id they have
	// seen. Unread mentions are derived from it, never stored — a counter
	// somebody has to remember to decrement is a counter that goes wrong.
	Read map[string]int64 `json:"read,omitempty"`
}

// PostKind says who wrote a post, which is the only thing the UI needs to
// know to render it differently.
type PostKind string

const (
	// PostUser: a person typed it.
	PostUser PostKind = "user"
	// PostAgent: a Cell's agent said something back — it accepted a task, or
	// it finished one.
	PostAgent PostKind = "agent"
	// PostSystem: the platform explaining itself, e.g. why an @ did not
	// reach anybody. Never silently do nothing.
	PostSystem PostKind = "system"
)

type Post struct {
	ID   int64    `json:"id"`
	Kind PostKind `json:"kind"`
	// Author is a user id for a person, a Cell name for an agent, empty for
	// the platform.
	Author string `json:"author,omitempty"`
	// +kubebuilder:validation:MaxLength=4096
	Body string `json:"body"`
	// Cell and Session link a post to the work it is about, so "what came of
	// that" is one click rather than a search.
	Cell    string      `json:"cell,omitempty"`
	Session string      `json:"session,omitempty"`
	At      metav1.Time `json:"at,omitempty"`
	// Mentions are the user ids this post is addressed to.
	Mentions []string `json:"mentions,omitempty"`
}

// +kubebuilder:object:root=true
type BoardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Board `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Board{}, &BoardList{})
}

// MaxPosts bounds one board. Roughly a working week of a busy team, after
// which the oldest fall off.
const MaxPosts = 300

// Append adds a post, assigns its id and trims the front.
func (b *Board) Append(p Post) Post {
	if b.Spec.NextID == 0 {
		b.Spec.NextID = 1
	}
	p.ID = b.Spec.NextID
	b.Spec.NextID++
	if p.At.IsZero() {
		p.At = metav1.Now()
	}
	b.Spec.Posts = append(b.Spec.Posts, p)
	if n := len(b.Spec.Posts); n > MaxPosts {
		b.Spec.Posts = b.Spec.Posts[n-MaxPosts:]
	}
	b.Spec.Count = len(b.Spec.Posts)
	return p
}

// Unread counts posts addressed to a user that they have not seen.
//
// Derived rather than stored: a counter somebody has to remember to
// decrement is a counter that eventually disagrees with the messages.
func (b *Board) Unread(userID string) int {
	if userID == "" {
		return 0
	}
	seen := b.Spec.Read[userID]
	n := 0
	for _, p := range b.Spec.Posts {
		if p.ID <= seen || p.Author == userID {
			continue
		}
		for _, m := range p.Mentions {
			if m == userID {
				n++
				break
			}
		}
	}
	return n
}

// MarkRead moves a user's high-water mark to the latest post.
func (b *Board) MarkRead(userID string) {
	if userID == "" || len(b.Spec.Posts) == 0 {
		return
	}
	if b.Spec.Read == nil {
		b.Spec.Read = map[string]int64{}
	}
	b.Spec.Read[userID] = b.Spec.Posts[len(b.Spec.Posts)-1].ID
}
