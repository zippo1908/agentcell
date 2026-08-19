package egress

import (
	"context"
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
func (r *Resolver) Attribute(ctx context.Context, ip string) Attribution {
	ttl := r.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := time.Now()

	r.mu.Lock()
	if e, ok := r.cache[ip]; ok && now.Sub(e.at) < ttl {
		r.mu.Unlock()
		return e.attr
	}
	r.mu.Unlock()

	attr := r.lookup(ctx, ip)

	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]entry{}
	}
	r.cache[ip] = entry{at: now, attr: attr}
	r.mu.Unlock()
	return attr
}

func (r *Resolver) lookup(ctx context.Context, ip string) Attribution {
	attr := Attribution{IP: ip}
	if r.Client == nil {
		return attr
	}
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods); err != nil {
		return attr
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
		return attr
	}
	attr.Pod = pod.Name
	attr.Cell = pod.Labels[ids.CellLabelKey]
	if attr.Cell == "" {
		attr.Cell = strings.TrimPrefix(pod.Namespace, "cell-")
	}
	attr.Session = pod.Labels[ids.SessionLabelKey]
	if attr.Session == "" {
		return attr
	}
	// The session is what carries the owner: a runtime pod belongs to one
	// person, and spec.ownerUserID is the principal that pays for it and is
	// the same id the rest of the platform authorizes against.
	var sess acv1.Session
	if err := r.Client.Get(ctx,
		client.ObjectKey{Namespace: r.ControlNS, Name: attr.Session}, &sess); err != nil {
		return attr
	}
	attr.PrincipalID = sess.Spec.OwnerUserID
	if attr.Cell == "" {
		attr.Cell = sess.Spec.Cell
	}
	return attr
}
