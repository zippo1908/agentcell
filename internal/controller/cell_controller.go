package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

const cellFinalizer = "agentcell.io/cleanup"

// CellReconciler reconciles a Cell into its workload namespace: namespace,
// git secret copy, workspace PVC, anchor StatefulSet (which also hosts the
// resident product preview) and preview Service.
type CellReconciler struct {
	client.Client
}

func (r *CellReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cell acv1.Cell
	if err := r.Get(ctx, req.NamespacedName, &cell); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cell.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &cell)
	}
	if controllerutil.AddFinalizer(&cell, cellFinalizer) {
		if err := r.Update(ctx, &cell); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := ids.ValidateCellName(cell.Name); err != nil {
		return r.fail(ctx, &cell, err)
	}
	applyCellDefaults(&cell)

	ns := ids.WorkloadNamespace(cell.Name)
	if err := r.ensureNamespace(ctx, ns, cell.Name); err != nil {
		return r.fail(ctx, &cell, err)
	}
	if err := r.ensureNetworkPolicies(ctx, ns, cell.Namespace); err != nil {
		return r.fail(ctx, &cell, fmt.Errorf("network policies: %w", err))
	}
	if cell.Spec.Repo.SecretName != "" {
		if err := r.copySecret(ctx, cell.Namespace, cell.Spec.Repo.SecretName, ns, ids.GitSecretName); err != nil {
			return r.fail(ctx, &cell, fmt.Errorf("copy git secret: %w", err))
		}
	}
	if err := r.ensurePVC(ctx, &cell, ns); err != nil {
		return r.fail(ctx, &cell, err)
	}
	if err := r.ensureAnchor(ctx, &cell, ns); err != nil {
		return r.fail(ctx, &cell, err)
	}
	if err := r.ensurePreviewService(ctx, &cell, ns); err != nil {
		return r.fail(ctx, &cell, err)
	}
	if err := r.ensureProduction(ctx, &cell, ns); err != nil {
		return r.fail(ctx, &cell, err)
	}

	// Observe.
	var sts appsv1.StatefulSet
	ready := false
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.AnchorStatefulSet}, &sts); err == nil {
		ready = sts.Status.ReadyReplicas > 0
	}
	active, liveIDs, err := r.observeSessions(ctx, &cell)
	if err != nil {
		return ctrl.Result{}, err
	}

	cell.Status.ObservedGeneration = cell.Generation
	cell.Status.ActiveSessions = active
	// Stale-lease reconciliation: a lease whose session is gone or terminal
	// is a leaked slot (e.g. controller crash between terminal-status write
	// and release). Drop it here so the gate self-heals.
	if len(cell.Status.SlotLeases) > 0 {
		kept := cell.Status.SlotLeases[:0]
		for _, l := range cell.Status.SlotLeases {
			if liveIDs[l] {
				kept = append(kept, l)
			}
		}
		cell.Status.SlotLeases = kept
	}
	cell.Status.PreviewPath = "/preview/" + cell.Name + "/"
	if released(&cell) {
		cell.Status.ProductionPath = "/app/" + cell.Name + "/"
	} else {
		cell.Status.ProductionPath = ""
	}
	cell.Status.Message = ""
	if ready {
		cell.Status.Phase = acv1.CellReady
	} else {
		cell.Status.Phase = acv1.CellPending
	}
	if err := r.Status().Update(ctx, &cell); err != nil {
		return ctrl.Result{}, err
	}
	log.V(1).Info("reconciled cell", "cell", cell.Name, "ready", ready, "activeSessions", active)
	// Poll anchor readiness until Ready; afterwards session events drive us.
	if !ready {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func applyCellDefaults(cell *acv1.Cell) {
	if cell.Spec.Repo.Branch == "" {
		cell.Spec.Repo.Branch = "main"
	}
	if cell.Spec.MaxSessions == 0 {
		cell.Spec.MaxSessions = 2
	}
	if cell.Spec.WorkspaceSize == "" {
		cell.Spec.WorkspaceSize = "10Gi"
	}
	if cell.Spec.SessionResources.CPU == "" {
		cell.Spec.SessionResources.CPU = "1"
	}
	if cell.Spec.SessionResources.Memory == "" {
		cell.Spec.SessionResources.Memory = "2Gi"
	}
}

func (r *CellReconciler) fail(ctx context.Context, cell *acv1.Cell, err error) (ctrl.Result, error) {
	cell.Status.Phase = acv1.CellError
	cell.Status.Message = err.Error()
	if serr := r.Status().Update(ctx, cell); serr != nil {
		return ctrl.Result{}, serr
	}
	return ctrl.Result{}, err
}

func (r *CellReconciler) finalize(ctx context.Context, cell *acv1.Cell) (ctrl.Result, error) {
	ns := ids.WorkloadNamespace(cell.Name)
	err := r.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if controllerutil.RemoveFinalizer(cell, cellFinalizer) {
		if err := r.Update(ctx, cell); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *CellReconciler) ensureNamespace(ctx context.Context, name, cellName string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		ns.Labels[ids.CellLabelKey] = cellName
		ns.Labels["app.kubernetes.io/managed-by"] = "agentcell"
		// Pod Security: all platform-rendered pods are non-root, seccomp,
		// drop-ALL — they satisfy the restricted profile.
		ns.Labels["pod-security.kubernetes.io/enforce"] = "restricted"
		return nil
	})
	return err
}

// ensureNetworkPolicies locks a Cell namespace down to default-deny, then
// reopens only what the workload needs: DNS + HTTPS egress (model APIs and
// git over 443) and ingress from the control-plane namespace to the
// preview/prod ports. Cross-project reachability is thereby removed even
// though projects share the cluster network.
func (r *CellReconciler) ensureNetworkPolicies(ctx context.Context, ns, controlNS string) error {
	deny := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default-deny"}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, deny, func() error {
		deny.Spec = netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{}, // all pods
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress},
		}
		return nil
	}); err != nil {
		return err
	}

	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	dns := intstr.FromInt(53)
	https := intstr.FromInt(443)
	egress := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "allow-egress"}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, egress, func() error {
		egress.Spec = netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress: []netv1.NetworkPolicyEgressRule{
				{Ports: []netv1.NetworkPolicyPort{
					{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns},
				}},
				{Ports: []netv1.NetworkPolicyPort{{Protocol: &tcp, Port: &https}}},
			},
		}
		return nil
	}); err != nil {
		return err
	}

	// Ingress to preview/prod is allowed only from the control-plane
	// namespace (celld's reverse proxy), never peer cells.
	ingress := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "allow-control-plane-ingress"}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		ingress.Spec = netv1.NetworkPolicySpec{
			// Both zones' serving pods (preview anchor + prod) are reachable
			// only from the control plane.
			PodSelector: metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      ids.AnchorPodLabelKey,
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{ids.AnchorPodLabelVal, ids.ProdPodLabelVal},
				}},
			},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress: []netv1.NetworkPolicyIngressRule{{
				From: []netv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": controlNS},
					},
				}},
			}},
		}
		return nil
	})
	return err
}

func (r *CellReconciler) copySecret(ctx context.Context, srcNS, srcName, dstNS, dstName string) error {
	var src corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: srcNS, Name: srcName}, &src); err != nil {
		return err
	}
	dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: dstNS, Name: dstName}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		dst.Type = src.Type
		dst.Data = src.Data
		return nil
	})
	return err
}

func (r *CellReconciler) ensurePVC(ctx context.Context, cell *acv1.Cell, ns string) error {
	size, err := resource.ParseQuantity(cell.Spec.WorkspaceSize)
	if err != nil {
		return fmt.Errorf("workspaceSize: %w", err)
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.WorkspacePVC}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		if pvc.CreationTimestamp.IsZero() { // spec is immutable after create
			pvc.Spec = corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: size},
				},
			}
			if sc := cell.Spec.StorageClassName; sc != "" {
				pvc.Spec.StorageClassName = &sc
			}
		}
		return nil
	})
	return err
}

// previewTargetDir resolves where the resident preview serves from: the
// main checkout, or a followed session's worktree ("watch the agent work").
func previewTargetDir(cell *acv1.Cell) string {
	if s := cell.Spec.Preview.FollowSession; s != "" {
		return ids.WorktreePath(s)
	}
	return ids.RepoPath
}

func (r *CellReconciler) ensureAnchor(ctx context.Context, cell *acv1.Cell, ns string) error {
	previewCmd, err := json.Marshal(cell.Spec.Preview.Command)
	if err != nil {
		return err
	}
	env := []corev1.EnvVar{
		{Name: runtimeapi.EnvRepoURL, Value: cell.Spec.Repo.URL},
		{Name: runtimeapi.EnvRepoBranch, Value: cell.Spec.Repo.Branch},
		{Name: runtimeapi.EnvPreviewCmd, Value: string(previewCmd)},
		{Name: runtimeapi.EnvPreviewPort, Value: fmt.Sprint(cell.Spec.Preview.Port)},
		{Name: runtimeapi.EnvPreviewTarget, Value: previewTargetDir(cell)},
	}
	if cell.Spec.Repo.SecretName != "" {
		env = append(env, gitCredEnv(ids.GitSecretName)...)
	}

	labels := map[string]string{
		ids.CellLabelKey:      cell.Name,
		ids.AnchorPodLabelKey: ids.AnchorPodLabelVal,
	}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.AnchorStatefulSet}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		one := int32(1)
		sts.Spec.Replicas = &one
		sts.Spec.ServiceName = ids.PreviewService
		sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		sts.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				SecurityContext: podSecurity(),
				Containers: []corev1.Container{{
					Name:            "anchor",
					Image:           cell.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{runtimeapi.RuntimeBin, "anchor"},
					SecurityContext: containerSecurity(),
					Env:             env,
					Ports: []corev1.ContainerPort{{
						Name: "preview", ContainerPort: previewPort(cell),
					}},
					// Readiness = the preview is actually serving. Without
					// this, the pod (and Cell) reports Ready the instant the
					// container starts — before the clone finishes and the
					// dev server binds — and the proxy 502s on early hits.
					// Only gated when a preview command exists; otherwise the
					// anchor idles and never binds the port.
					ReadinessProbe: previewReadiness(cell),
					VolumeMounts:   []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
				}},
				Volumes: []corev1.Volume{{
					Name: "workspace",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ids.WorkspacePVC},
					},
				}},
			},
		}
		return nil
	})
	return err
}

func previewPort(cell *acv1.Cell) int32 {
	if cell.Spec.Preview.Port != 0 {
		return cell.Spec.Preview.Port
	}
	return 3000
}

// previewReadiness gates anchor readiness on the preview actually listening,
// but only when a preview command is configured (an anchor with no preview
// idles and never binds a port, so a probe would wedge it NotReady).
func previewReadiness(cell *acv1.Cell) *corev1.Probe {
	if len(cell.Spec.Preview.Command) == 0 {
		return nil
	}
	return tcpReadiness(previewPort(cell))
}

// tcpReadiness returns a lenient TCP-connect readiness probe: it flips to
// Ready once something accepts on the port, which is exactly when the proxy
// stops 502-ing. Generous failureThreshold covers slow first clones.
func tcpReadiness(port int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		},
		InitialDelaySeconds: 3,
		PeriodSeconds:       5,
		FailureThreshold:    60,
	}
}

func (r *CellReconciler) ensurePreviewService(ctx context.Context, cell *acv1.Cell, ns string) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.PreviewService}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.Selector = map[string]string{ids.AnchorPodLabelKey: ids.AnchorPodLabelVal}
		svc.Spec.Ports = []corev1.ServicePort{{Name: "preview", Port: previewPort(cell)}}
		return nil
	})
	return err
}

// released reports whether the Cell has a production zone at all: it only
// exists after the first explicit release action.
func released(cell *acv1.Cell) bool {
	return cell.Spec.Production.ReleaseID != "" && len(prodCommand(cell)) > 0
}

// prodCommand falls back to the preview command: the same dev server run
// against a release checkout is a sane default production shape for the
// products this targets.
func prodCommand(cell *acv1.Cell) []string {
	if len(cell.Spec.Production.Command) > 0 {
		return cell.Spec.Production.Command
	}
	return cell.Spec.Preview.Command
}

func prodPort(cell *acv1.Cell) int32 {
	if cell.Spec.Production.Port != 0 {
		return cell.Spec.Production.Port
	}
	return previewPort(cell)
}

// ensureProduction reconciles the 正式区: an isolated Deployment + Service.
// Isolation is structural — the prod pod mounts an emptyDir and shallow-
// clones the release ref itself; it never mounts the dev-zone PVC, so no
// amount of dev/test debugging can reach it. A new ReleaseID changes the
// pod env, which rolls the pod, which re-clones: that is the release.
func (r *CellReconciler) ensureProduction(ctx context.Context, cell *acv1.Cell, ns string) error {
	if !released(cell) {
		return nil
	}
	cmdJSON, err := json.Marshal(prodCommand(cell))
	if err != nil {
		return err
	}
	ref := cell.Spec.Production.Ref
	if ref == "" {
		ref = cell.Spec.Repo.Branch
	}
	// Credential hygiene: only the init container (which runs exclusively
	// our clone applet) sees the git token; the serving container executes
	// repo-controlled commands and gets no git env at all.
	cloneEnv := []corev1.EnvVar{
		{Name: runtimeapi.EnvRepoURL, Value: cell.Spec.Repo.URL},
		{Name: runtimeapi.EnvProdRef, Value: ref},
		{Name: runtimeapi.EnvProdReleaseID, Value: cell.Spec.Production.ReleaseID},
	}
	if cell.Spec.Repo.SecretName != "" {
		cloneEnv = append(cloneEnv, gitCredEnv(ids.GitSecretName)...)
	}
	serveEnv := []corev1.EnvVar{
		{Name: runtimeapi.EnvProdCmd, Value: string(cmdJSON)},
		{Name: runtimeapi.EnvProdReleaseID, Value: cell.Spec.Production.ReleaseID},
	}
	labels := map[string]string{
		ids.CellLabelKey:      cell.Name,
		ids.AnchorPodLabelKey: ids.ProdPodLabelVal,
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.ProdDeployment}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		one := int32(1)
		dep.Spec.Replicas = &one
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		dep.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				SecurityContext: podSecurity(),
				InitContainers: []corev1.Container{{
					Name:            "clone",
					Image:           cell.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{runtimeapi.RuntimeBin, "prod-clone"},
					SecurityContext: containerSecurity(),
					Env:             cloneEnv,
					VolumeMounts:    []corev1.VolumeMount{{Name: "prodspace", MountPath: "/prodspace"}},
				}},
				Containers: []corev1.Container{{
					Name:            "prod",
					Image:           cell.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{runtimeapi.RuntimeBin, "prod-serve"},
					SecurityContext: containerSecurity(),
					Env:             serveEnv,
					Ports:           []corev1.ContainerPort{{Name: "prod", ContainerPort: prodPort(cell)}},
					ReadinessProbe:  tcpReadiness(prodPort(cell)),
					// emptyDir only: structurally impossible to share dev state.
					VolumeMounts: []corev1.VolumeMount{{Name: "prodspace", MountPath: "/prodspace"}},
				}},
				Volumes: []corev1.Volume{{
					Name:         "prodspace",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				}},
			},
		}
		return nil
	})
	if err != nil {
		return err
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.ProdService}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{Name: "prod", Port: prodPort(cell)}}
		return nil
	})
	return err
}

// observeSessions counts active sessions and returns the set of session
// ids that may legitimately hold a slot lease (existing and non-terminal).
func (r *CellReconciler) observeSessions(ctx context.Context, cell *acv1.Cell) (int32, map[string]bool, error) {
	var list acv1.SessionList
	if err := r.List(ctx, &list, client.InNamespace(cell.Namespace)); err != nil {
		return 0, nil, err
	}
	var n int32
	live := map[string]bool{}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Cell != cell.Name {
			continue
		}
		switch s.Status.Phase {
		case acv1.SessionQueued, acv1.SessionRunning, acv1.SessionSettling, "":
			n++
			if s.Status.SessionID != "" {
				live[s.Status.SessionID] = true
			}
			// Pre-id sessions can't hold a lease yet (claim needs the id).
		}
	}
	return n, live, nil
}

// SetupWithManager wires the controller: Cell events plus Session events
// mapped back to their Cell (to keep ActiveSessions and preview-follow
// fresh without polling).
func (r *CellReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&acv1.Cell{}).
		Watches(&acv1.Session{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []ctrl.Request {
				s, ok := obj.(*acv1.Session)
				if !ok || s.Spec.Cell == "" {
					return nil
				}
				return []ctrl.Request{{NamespacedName: types.NamespacedName{
					Namespace: s.Namespace, Name: s.Spec.Cell,
				}}}
			})).
		Named("cell").
		Complete(r)
}
