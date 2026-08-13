package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
)

// What each workload asks the scheduler for.
//
// Until now the anchor, the production pod and the settle Job declared
// nothing at all, which makes them BestEffort: invisible to the scheduler
// when it packs a node, and the FIRST things the kernel kills under memory
// pressure. That is exactly backwards. The anchor is the one pod in a Cell
// that must not die — it holds the checkout and serves the preview — and the
// preview it runs is a dev server, which for a real project is the largest
// resident consumer in the namespace, not a rounding error.
//
// Requests are what capacity planning is actually made of, so they are set
// deliberately and modestly; limits bound the damage a runaway build can do.

// anchorResources sizes the resident anchor.
//
// The preview command is a dev server (vite, webpack, a JVM) whose memory is
// a property of the project, not of AgentCell — so the limit is generous and
// the request is honest about the idle case. An operator who knows their
// build sets previewResources on the Cell.
func anchorResources(cell *acv1.Cell) corev1.ResourceRequirements {
	req, lim := budget(cell.Spec.PreviewResources, "200m", "512Mi", "2", "4Gi")
	return corev1.ResourceRequirements{Requests: req, Limits: lim}
}

// prodResources sizes the released build. Same shape as the anchor's
// preview: it is the same command against a released checkout.
func prodResources(cell *acv1.Cell) corev1.ResourceRequirements {
	return anchorResources(cell)
}

// settleResources sizes the settle Job: git add/commit/push over a worktree.
// Short-lived and IO-bound, but a large repository needs real memory to pack
// objects, and a settle that is OOM-killed is work that looks lost.
func settleResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
}

// budget resolves an optional per-Cell override against defaults.
func budget(b acv1.ResourceBudget, reqCPU, reqMem, limCPU, limMem string) (corev1.ResourceList, corev1.ResourceList) {
	lc, lm := limCPU, limMem
	if b.CPU != "" {
		lc = b.CPU
	}
	if b.Memory != "" {
		lm = b.Memory
	}
	parse := func(s, fallback string) resource.Quantity {
		q, err := resource.ParseQuantity(s)
		if err != nil {
			// A bad value must not make a pod unschedulable or unbounded;
			// the Cell controller surfaces the error separately.
			return resource.MustParse(fallback)
		}
		return q
	}
	return corev1.ResourceList{
			corev1.ResourceCPU:    parse(reqCPU, "200m"),
			corev1.ResourceMemory: parse(reqMem, "512Mi"),
		}, corev1.ResourceList{
			corev1.ResourceCPU:    parse(lc, limCPU),
			corev1.ResourceMemory: parse(lm, limMem),
		}
}

// cellQuota is what makes a Cell's budget real.
//
// Without it "this project gets 4 CPU" is an arithmetic convention: the slot
// count bounds how many SESSIONS run, but nothing bounds the namespace, so
// one project's dev server can starve the node its neighbours were sized
// for. A quota turns the number into something the API server enforces at
// admission — a pod that would exceed it is refused with a reason, which is
// far easier to act on than a node that quietly starts OOM-killing.
//
// It caps REQUESTS, not limits, and that distinction was learned the hard
// way on a cluster: a limits quota sums every pod's ceiling, so N users each
// with a runtime allowed to burst to the whole Cell exceeded it before any
// of them ran anything. Requests are what the scheduler actually reserves
// and what capacity planning is made of; limits stay per-pod, bounding a
// runaway build rather than the namespace.
//
// The ceiling is every slot at full budget, the resident anchor and
// production pods, headroom for a settle Job — which must be able to run
// even when every slot is busy, because publishing finished work is the
// worst moment to run out of room — and one runtime per slot, since that is
// the most users who can have work in flight at once.
func cellQuota(cell *acv1.Cell) corev1.ResourceList {
	slots := cell.Spec.MaxSessions
	if slots <= 0 {
		slots = 2
	}
	cpu := mustQuantity(cell.Spec.SessionResources.CPU, "1")
	mem := mustQuantity(cell.Spec.SessionResources.Memory, "2Gi")
	residentReq, _ := budget(cell.Spec.PreviewResources, "200m", "512Mi", "2", "4Gi")
	runtimeReq := runtimeResources(cell).Requests
	settleReq := settleResources().Requests

	totalCPU := resource.NewMilliQuantity(0, resource.DecimalSI)
	totalMem := resource.NewQuantity(0, resource.BinarySI)
	add := func(c, m resource.Quantity) {
		totalCPU.Add(c)
		totalMem.Add(m)
	}
	for range slots {
		add(cpu, mem)
		// One runtime per slot: a resident session runs inside its owner's,
		// and that is the most owners who can be working at once.
		add(runtimeReq[corev1.ResourceCPU], runtimeReq[corev1.ResourceMemory])
	}
	add(residentReq[corev1.ResourceCPU], residentReq[corev1.ResourceMemory]) // anchor
	add(residentReq[corev1.ResourceCPU], residentReq[corev1.ResourceMemory]) // production
	add(settleReq[corev1.ResourceCPU], settleReq[corev1.ResourceMemory])

	return corev1.ResourceList{
		corev1.ResourceRequestsCPU:    *totalCPU,
		corev1.ResourceRequestsMemory: *totalMem,
	}
}

func mustQuantity(s, fallback string) resource.Quantity {
	if s == "" {
		return resource.MustParse(fallback)
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return resource.MustParse(fallback)
	}
	return q
}
