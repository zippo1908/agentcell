package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Single-use has to mean single-use across every replica.
//
// The replay guard was a map in one process, which is correct for exactly one
// celld and silently wrong for two: a ticket captured from a URL could be
// redeemed once on each replica, and nothing anywhere would say so. Leader
// election makes it SAFE to run several celld replicas, so it is also what
// makes this bug reachable — fixing it is part of the same change, not a
// follow-up.
//
// The shared store is the API server, which every replica already talks to
// and already trusts. Redeeming a ticket creates one small object with the
// nonce in its name: Create fails with AlreadyExists if somebody got there
// first, which is precisely the atomic test-and-set needed and is not
// something a cache can weaken — creates go to the API server.
//
// Cost is one Create per preview open. That is a person clicking a link, not
// a hot path.

const ticketNS = "agentcell-ticket-"

// sharedTickets records redeemed nonces as short-lived ConfigMaps.
type sharedTickets struct {
	client    client.Client
	namespace string
	// fallback keeps the process-local guard working when no client was
	// wired (tests, and any deployment that never got one). It is strictly
	// weaker, so it is a fallback and never the primary.
	fallback usedTickets
}

func (s *sharedTickets) consume(ctx context.Context, nonce string, exp time.Time) bool {
	if s.client == nil {
		return s.fallback.consume(nonce, exp)
	}
	// The nonce is a capability, so it must not be readable from an object
	// name that anyone with list access can see. The digest is enough to
	// collide-detect and reveals nothing.
	sum := sha256.Sum256([]byte(nonce))
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: s.namespace,
		Name:      ticketNS + hex.EncodeToString(sum[:16]),
		Labels:    map[string]string{"agentcell.io/ticket": "redeemed"},
		Annotations: map[string]string{
			// Expiry is carried so the sweeper does not have to guess, and so
			// a human reading these can tell live from litter.
			"agentcell.io/expires": exp.UTC().Format(time.RFC3339),
		},
	}}
	err := s.client.Create(ctx, cm)
	if err == nil {
		return true
	}
	if apierrors.IsAlreadyExists(err) {
		return false // replay
	}
	// The API server is unreachable or refusing. Falling back to the local
	// guard keeps previews working on one replica and degrades to
	// once-per-replica on several — which is the behaviour that existed
	// before this file, so it is a degradation and not a new hole.
	return s.fallback.consume(nonce, exp)
}

// sweepTickets deletes redeemed-ticket records once they cannot be replayed.
//
// Nothing else collects them: a ConfigMap has no TTL, and a ticket that has
// expired is no longer a secret worth remembering. Left alone these would
// accumulate one per preview open, forever.
func (s *sharedTickets) sweep(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	var list corev1.ConfigMapList
	if err := s.client.List(ctx, &list,
		client.InNamespace(s.namespace),
		client.MatchingLabels{"agentcell.io/ticket": "redeemed"}); err != nil {
		return err
	}
	now := time.Now()
	for i := range list.Items {
		cm := &list.Items[i]
		exp, err := time.Parse(time.RFC3339, cm.Annotations["agentcell.io/expires"])
		// An unparseable record is litter too — but only once it is old
		// enough that it cannot still be guarding a live ticket.
		if err != nil {
			if now.Sub(cm.CreationTimestamp.Time) < time.Hour {
				continue
			}
		} else if now.Before(exp) {
			continue
		}
		_ = s.client.Delete(ctx, cm)
	}
	return nil
}

// SweepTickets runs the sweeper until the context ends. It is safe to run on
// every replica: deleting an already-deleted record is not an error worth
// reporting, and the work is a list every few minutes.
func (a *Authenticator) SweepTickets(ctx context.Context) error {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if err := a.tickets.sweep(ctx); err != nil {
			// Not fatal: failing to collect litter must never take down the
			// console.
			_ = err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
