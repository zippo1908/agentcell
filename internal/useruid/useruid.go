// Package useruid allocates the stable Unix UID a user's workloads run as.
//
// The allocation is recorded, never derived. Hashing a user id into the UID
// space would be simpler and is wrong: hashes collide, and a collision here
// means two people share a UID — the exact property this layer exists to
// prevent (ADR-0009).
//
// Allocations are also never released. A recycled UID silently inherits the
// previous holder's files, so a departed user keeps their number forever;
// the record is a tombstone as much as a mapping.
package useruid

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ConfigMapName holds the allocations in the control namespace. It is
	// deliberately not a Secret: a UID is not a credential, and being able
	// to read the mapping is what makes an incident investigable.
	ConfigMapName = "agentcell-uids"

	// FirstUID starts well above the ranges distributions hand out to system
	// and login accounts, so a UID here can never coincide with one baked
	// into a container image.
	FirstUID int64 = 100000
	// LastUID bounds the space. Exhausting it should be a loud error, not a
	// silent wrap into somebody else's identity.
	LastUID int64 = 165535

	// ProjectUID is the shared project identity: what the anchor and the
	// production pod run as, and what a deployment with no user identity
	// keeps using. It matches the uid the devbox image ships.
	ProjectUID int64 = 1000
	// ProjectGID owns the shared project volume. Every user's pod joins it
	// via fsGroup, which is how private UIDs still collaborate on one PVC.
	ProjectGID int64 = 1000

	keyNext     = "next"
	prefixAlloc = "uid."
	maxAttempts = 5
)

// Allocator hands out UIDs and remembers them.
type Allocator struct {
	Client    client.Client
	Namespace string
}

// Ensure returns the UID for a user, allocating one on first sight.
//
// An empty userID is the pre-identity / static-token case and maps to the
// shared project identity, so single-principal deployments behave exactly
// as they did before this layer existed.
func (a *Allocator) Ensure(ctx context.Context, userID string) (int64, error) {
	if userID == "" {
		return ProjectUID, nil
	}
	if err := validate(userID); err != nil {
		return 0, err
	}
	var lastErr error
	for range maxAttempts {
		uid, err := a.attempt(ctx, userID)
		if err == nil {
			return uid, nil
		}
		if !apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err) {
			return 0, err
		}
		// Another writer won the race; re-read and try again.
		lastErr = err
	}
	return 0, fmt.Errorf("allocate uid for %s: %w", userID, lastErr)
}

func (a *Allocator) attempt(ctx context.Context, userID string) (int64, error) {
	cm := &corev1.ConfigMap{}
	err := a.Client.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: ConfigMapName}, cm)
	switch {
	case apierrors.IsNotFound(err):
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Namespace: a.Namespace, Name: ConfigMapName,
		}}
		uid := FirstUID
		cm.Data = map[string]string{
			keyNext:              strconv.FormatInt(uid+1, 10),
			prefixAlloc + userID: strconv.FormatInt(uid, 10),
		}
		if err := a.Client.Create(ctx, cm); err != nil {
			return 0, err
		}
		return uid, nil
	case err != nil:
		return 0, err
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if raw, ok := cm.Data[prefixAlloc+userID]; ok {
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || uid < FirstUID || uid > LastUID {
			// A corrupted record must never fall through to "allocate a new
			// one": that would hand this user a second identity while their
			// files still belong to the first.
			return 0, fmt.Errorf("uid record for %s is invalid (%q)", userID, raw)
		}
		return uid, nil
	}

	next := FirstUID
	if raw, ok := cm.Data[keyNext]; ok {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("uid counter is invalid (%q)", raw)
		}
		next = parsed
	}
	if next > LastUID {
		return 0, fmt.Errorf("uid space exhausted at %d; widen the range deliberately rather than reusing numbers", LastUID)
	}
	cm.Data[prefixAlloc+userID] = strconv.FormatInt(next, 10)
	cm.Data[keyNext] = strconv.FormatInt(next+1, 10)
	if err := a.Client.Update(ctx, cm); err != nil {
		return 0, err
	}
	return next, nil
}

// validate keeps the key space to the two prefixes the platform issues, so a
// crafted id cannot collide with the counter key or forge somebody's entry.
//
// "u-" is a person. "t-" is a TEAM: a board's conversation with a project is
// the team's, not the asker's, so it needs a uid of its own — its worktree
// and tmux socket must be separate from every individual's, exactly as one
// person's are separate from another's.
func validate(userID string) error {
	if !strings.HasPrefix(userID, "u-") && !strings.HasPrefix(userID, "t-") {
		return fmt.Errorf("id %q is not in the expected form", userID)
	}
	if len(userID) > 63 {
		return fmt.Errorf("user id %q is not in the expected form", userID)
	}
	for _, r := range userID {
		if r != '-' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return fmt.Errorf("user id %q contains %q", userID, r)
		}
	}
	return nil
}
