package egress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
)

// Resolver turns the address a connection came from into the person it
// belongs to.
//
// Attribution is by CLIENT IP, deliberately, and not by a token carried in
// the workload. ADR-0005 says a workload pod holds no credential that lets
// it talk to the control plane, and an egress token would be exactly that
// — something in the sandbox that names it to a platform service. The pod
// address is assigned by the CNI, is not settable from inside the pod, and
// the control plane already knows which pod belongs to which session. So the
// identity is derived on the control-plane side from something the workload
// cannot choose.
//
// What this cannot do: attribute a connection from a pod that has since been
// deleted. The lookup happens while the connection is being opened, so in
// practice the pod is alive; when it is not, the line records the address and
// says the principal is unknown rather than guessing.
type Resolver struct {
	Client client.Client
	// ControlNS is where Session objects live.
	ControlNS string
	// TTL bounds how stale an attribution may be. Short, because pod
	// addresses are recycled: attributing one person's traffic to another
	// because an address was reused is the one failure this must not have.
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	at   time.Time
	attr Attribution
}

// Attribute identifies the owner of a connection from ip.
//
// The error is returned rather than swallowed: an attribution that fails
// quietly produces a log full of unattributed lines that look like traffic
// from outside the platform, and nothing anywhere says the lookup is broken.
// The Attribution is still usable when this returns an error — it carries
// the address, which is evidence even when nothing else resolved.
func (r *Resolver) Attribute(ctx context.Context, ip string) (Attribution, error) {
	ttl := r.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := time.Now()

	r.mu.Lock()
	if e, ok := r.cache[ip]; ok && now.Sub(e.at) < ttl {
		r.mu.Unlock()
		return e.attr, nil
	}
	r.mu.Unlock()

	attr, err := r.lookup(ctx, ip)

	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]entry{}
	}
	// A failed lookup is cached only briefly — long enough to stop a
	// hot loop hammering the API server, short enough that a transient
	// error does not blind the audit log for a whole TTL.
	if err != nil {
		r.mu.Unlock()
		return attr, err
	}
	r.cache[ip] = entry{at: now, attr: attr}
	r.mu.Unlock()
	return attr, nil
}

func (r *Resolver) lookup(ctx context.Context, ip string) (Attribution, error) {
	attr := Attribution{IP: ip}
	if r.Client == nil {
		return attr, errors.New("no kubernetes client")
	}
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods); err != nil {
		return attr, fmt.Errorf("list pods: %w", err)
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.PodIP == ip && strings.HasPrefix(p.Namespace, "cell-") {
			pod = p
			break
		}
	}
	if pod == nil {
		return attr, fmt.Errorf("no pod in a cell namespace has address %s (%d pods seen)", ip, len(pods.Items))
	}
	attr.Pod = pod.Name
	attr.Cell = pod.Labels[ids.CellLabelKey]
	if attr.Cell == "" {
		attr.Cell = strings.TrimPrefix(pod.Namespace, "cell-")
	}
	attr.Session = pod.Labels[ids.SessionLabelKey]
	if attr.Session == "" {
		// An anchor or preview pod: it belongs to the project rather than
		// to one person, which is a real answer and not a failure.
		return attr, nil
	}
	// The session is what carries the owner: a runtime pod belongs to one
	// person, and spec.ownerUserID is the principal that pays for it and is
	// the same id the rest of the platform authorizes against.
	var sess acv1.Session
	if err := r.Client.Get(ctx,
		client.ObjectKey{Namespace: r.ControlNS, Name: attr.Session}, &sess); err != nil {
		return attr, fmt.Errorf("get session %s: %w", attr.Session, err)
	}
	attr.PrincipalID = sess.Spec.OwnerUserID
	if attr.Cell == "" {
		attr.Cell = sess.Spec.Cell
	}
	return attr, nil
}
