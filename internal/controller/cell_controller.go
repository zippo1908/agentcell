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
	// GitBrokerURL, when set, routes workload git through the broker so no
	// pod holds a forge credential (ADR-0005).
	GitBrokerURL string
	// ControlNamespace is where the operator's own configuration lives — the
	// source of anything that has to be mirrored into a Cell's namespace.
	ControlNamespace string
	// ImagePullSecret names a docker-registry Secret in ControlNamespace. On
	// private clouds the runtime images usually sit in a private registry, so
	// the Cell namespaces need pull credentials too. It carries no push rights
	// and no forge token, so mirroring it does not widen the blast radius the
	// way copying the git credential would (ADR-0005).
	ImagePullSecret string
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
	if err := r.ensurePullSecret(ctx, ns); err != nil {
		return r.fail(ctx, &cell, err)
	}
	// Record what "who can touch this" resolves to, so an open Cell says so
	// rather than being inferred from a missing field.
	cell.Status.Access = cell.EffectiveAccess()
	if err := r.ensureQuota(ctx, &cell, ns); err != nil {
		return r.fail(ctx, &cell, fmt.Errorf("resource quota: %w", err))
	}
	if err := r.ensureNetworkPolicies(ctx, ns, cell.Namespace); err != nil {
		return r.fail(ctx, &cell, fmt.Errorf("network policies: %w", err))
	}
	if r.GitBrokerURL != "" {
		if err := r.ensureServiceAccounts(ctx, ns); err != nil {
			return r.fail(ctx, &cell, fmt.Errorf("service accounts: %w", err))
		}
	}
	// In broker mode the forge secret must NOT be copied into the workload
	// namespace — it stays readable only by the broker (ADR-0005). Only
	// direct mode needs a per-namespace copy for the askpass helper.
	if cell.Spec.Repo.SecretName != "" && r.GitBrokerURL == "" {
		if err := r.copySecret(ctx, cell.Namespace, cell.Spec.Repo.SecretName, ns, ids.GitSecretName); err != nil {
			return r.fail(ctx, &cell, fmt.Errorf("copy git secret: %w", err))
		}
	}
	if err := r.copyDatabaseSecrets(ctx, &cell, ns); err != nil {
		return r.fail(ctx, &cell, fmt.Errorf("database secrets: %w", err))
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
	if err := r.handOff(ctx, &cell); err != nil {
		// A refusing deployer is a condition to report, not a reason to stop
		// reconciling the rest of the Cell.
		cell.Status.HandoffMessage = err.Error()
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
	cellActiveSessions.WithLabelValues(cell.Name).Set(float64(active))
	if err := r.recordCellsTotal(ctx, cell.Namespace); err != nil {
		log.Error(err, "recording agentcell_cells_total")
	}
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
	cell.Status.Node, cell.Status.SchedulingMessage = r.observePlacement(ctx, ns)
	if err := r.Status().Update(ctx, &cell); err != nil {
		return ctrl.Result{}, err
	}
	log.V(1).Info("reconciled cell", "cell", cell.Name, "ready", ready, "activeSessions", active)
	// Poll anchor readiness until Ready; afterwards session events and the
	// anchor watch drive us. An unscheduled anchor is polled too: the
	// scheduler records its refusal on the pod a moment after creating it,
	// so the first look often finds a reason that is not there yet.
	if !ready || cell.Status.Node == "" {
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

// observePlacement answers the two questions an owner asks about a Cell that
// is not up: which machine is it on, and if none, why.
//
// The second is the one that matters. An unschedulable anchor is this
// system's most opaque failure — a selector that matches nothing, a pool
// whose taint was not tolerated, a node with no room left — and every one of
// those reads identically from outside: a Cell that says Pending forever.
// The scheduler already explains itself, in a pod condition nobody without
// cluster access can reach. This carries that sentence up to where the
// question gets asked.
func (r *CellReconciler) observePlacement(ctx context.Context, ns string) (node, why string) {
	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.AnchorStatefulSet + "-0"}, &pod); err != nil {
		return "", ""
	}
	if pod.Spec.NodeName != "" {
		return pod.Spec.NodeName, ""
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			if c.Message != "" {
				return "", c.Message
			}
			return "", c.Reason
		}
	}
	return "", ""
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
	var namespace corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: ns}, &namespace)
	if err == nil {
		if namespace.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, &namespace); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
		// Namespace deletion is asynchronous. Keep the Cell finalizer until it
		// has actually completed so a same-name Cell cannot reuse a terminating
		// workload namespace.
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if controllerutil.RemoveFinalizer(cell, cellFinalizer) {
		if err := r.Update(ctx, cell); err != nil {
			return ctrl.Result{}, err
		}
	}
	// The cell is gone: drop its per-cell gauge rather than leaving a stale
	// series behind, and recompute the fleet total.
	cellActiveSessions.DeleteLabelValues(cell.Name)
	if err := r.recordCellsTotal(ctx, cell.Namespace); err != nil {
		logf.FromContext(ctx).Error(err, "recording agentcell_cells_total")
	}
	return ctrl.Result{}, nil
}

// ensureServiceAccounts creates the dedicated per-role ServiceAccounts the
// broker distinguishes (ADR-0005 hardening): anchor and prod may only
// fetch; settle is the only role permitted to push. They carry no RBAC —
// their only use is identity at the broker.
func (r *CellReconciler) ensureServiceAccounts(ctx context.Context, ns string) error {
	for _, name := range []string{runtimeapi.SAAnchor, runtimeapi.SASettle, runtimeapi.SAProd} {
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
			sa.ImagePullSecrets = nil
			if r.ImagePullSecret != "" {
				sa.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.ImagePullSecret}}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// ensurePullSecret mirrors the registry credential into the Cell namespace.
// kubelet resolves pull secrets namespace-locally, so a private registry is
// unusable without this copy. Only the data is mirrored — never ownership of
// the original.
func (r *CellReconciler) ensurePullSecret(ctx context.Context, ns string) error {
	if r.ImagePullSecret == "" {
		return nil
	}
	var src corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.ControlNamespace, Name: r.ImagePullSecret}, &src); err != nil {
		return fmt.Errorf("image pull secret %s/%s: %w", r.ControlNamespace, r.ImagePullSecret, err)
	}
	dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: r.ImagePullSecret}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		dst.Type = src.Type
		dst.Data = src.Data
		return nil
	}); err != nil {
		return err
	}
	// Pods that predate the per-role ServiceAccounts (and any run without the
	// broker) fall back to `default`, so it needs the credential as well.
	def := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default"}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, def, func() error {
		def.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.ImagePullSecret}}
		return nil
	})
	return err
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
	broker := intstr.FromInt(8080)
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

	// Broker egress is granted ONLY to pods labeled broker-client
	// (anchor/settle/prod). Session pods lack the label and have no token,
	// so they cannot reach the broker at all (ADR-0005 hardening).
	if r.GitBrokerURL != "" {
		bpol := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "allow-broker-egress"}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, bpol, func() error {
			bpol.Spec = netv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{runtimeapi.BrokerClientLabelKey: runtimeapi.BrokerClientLabelVal},
				},
				PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
				Egress: []netv1.NetworkPolicyEgressRule{{
					To: []netv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"kubernetes.io/metadata.name": controlNS},
						},
					}},
					Ports: []netv1.NetworkPolicyPort{{Protocol: &tcp, Port: &broker}},
				}},
			}
			return nil
		}); err != nil {
			return err
		}
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

// previewTargetDir is where the ANCHOR's preview serves from: always the
// shared checkout.
//
// It used to serve a followed session's worktree so the user could watch the
// agent work. That is no longer the anchor's job: a worktree is private to
// its owner (ADR-0009), and the anchor — which belongs to the project, not
// to a user — cannot read it. Live preview of a session is served by that
// session's own pod, and the preview Service points there while a session is
// followed. The capability is unchanged; what moved is which process holds
// the file handle.
func previewTargetDir(_ *acv1.Cell) string {
	return ids.RepoPath
}

// resolvePlacement turns a Cell's placement into a selector and tolerations.
//
// A named class wins and is the only thing the API can set: its selector and
// its tolerations were written by an administrator offering that pool. The
// raw fields remain for somebody editing the Cell directly, which already
// requires cluster access.
//
// A class that has been deleted resolves to NOTHING rather than to the raw
// fields: falling back would silently move a Cell onto whatever the last
// hand-edit said, and "the pool was withdrawn" must not become "the pool is
// now wherever the scheduler likes, with the old tolerations".
func (r *CellReconciler) resolvePlacement(ctx context.Context, cell *acv1.Cell) (map[string]string, []corev1.Toleration) {
	if cell.Spec.Placement.Class == "" {
		return cell.Spec.Placement.NodeSelector, cell.Spec.Placement.Tolerations
	}
	var pc acv1.PlacementClass
	if err := r.Get(ctx, types.NamespacedName{Name: cell.Spec.Placement.Class}, &pc); err != nil {
		return nil, nil
	}
	return pc.Spec.NodeSelector, pc.Spec.Tolerations
}

func (r *CellReconciler) ensureAnchor(ctx context.Context, cell *acv1.Cell, ns string) error {
	sel, tol := r.resolvePlacement(ctx, cell)
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
		env = append(env, gitWorkloadEnv(r.GitBrokerURL, cell.Name, ids.GitSecretName)...)
	}

	selector := map[string]string{
		ids.CellLabelKey:      cell.Name,
		ids.AnchorPodLabelKey: ids.AnchorPodLabelVal,
	}
	mounts := []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}
	volumes := []corev1.Volume{{
		Name: "workspace",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: ids.WorkspacePVC},
		},
	}}
	podLabels := selector
	sa := ""
	if r.GitBrokerURL != "" {
		podLabels = withBrokerClientLabel(selector)
		sa = runtimeapi.SAAnchor
		mounts = append(mounts, brokerTokenMount())
		volumes = append(volumes, brokerTokenVolume())
	}

	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.AnchorStatefulSet}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		one := int32(1)
		sts.Spec.Replicas = &one
		sts.Spec.ServiceName = ids.PreviewService
		sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		sts.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
			Spec: corev1.PodSpec{
				ServiceAccountName: sa,
				SecurityContext:    podSecurity(),
				// Only the anchor carries the placement: every other pod in
				// the Cell already follows it by pod affinity, so stating it
				// once is both sufficient and the only way it cannot
				// disagree with itself.
				NodeSelector: sel,
				Tolerations:  tol,
				Containers: []corev1.Container{{
					Name:            "anchor",
					Image:           cell.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{runtimeapi.RuntimeBin, "anchor"},
					SecurityContext: containerSecurity(),
					Resources:       anchorResources(cell),
					Env:             env,
					Ports: []corev1.ContainerPort{{
						Name: "preview", ContainerPort: previewPort(cell),
					}},
					ReadinessProbe: previewReadiness(cell),
					EnvFrom:        databaseEnvFrom(cell.Spec.Database.DevSecretName, devDBSecret),
					VolumeMounts:   mounts,
				}},
				Volumes: volumes,
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
		// While a session is followed, its own pod serves the preview: the
		// worktree is private to its owner and the anchor cannot read it.
		if f := cell.Spec.Preview.FollowSession; f != "" {
			svc.Spec.Selector = map[string]string{ids.SessionLabelKey: f}
		} else {
			svc.Spec.Selector = map[string]string{ids.AnchorPodLabelKey: ids.AnchorPodLabelVal}
		}
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

// runsProductionHere reports whether this Cell hosts production itself.
//
// External is a handoff: AgentCell publishes the release and notifies the
// system that owns running it. Keeping a production pod alive as well would
// leave a second, weaker deployment of the same product answering on a URL
// somebody will eventually trust.
func runsProductionHere(cell *acv1.Cell) bool {
	return cell.Spec.Production.Target != acv1.ProductionExternal
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
	if !runsProductionHere(cell) {
		// Switching a Cell to external must REMOVE what was here. Leaving it
		// running would be a zombie serving a stale build on a URL the
		// console used to advertise.
		return r.removeInCellProduction(ctx, ns)
	}
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
		cloneEnv = append(cloneEnv, gitWorkloadEnv(r.GitBrokerURL, cell.Name, ids.GitSecretName)...)
	}
	serveEnv := []corev1.EnvVar{
		{Name: runtimeapi.EnvProdCmd, Value: string(cmdJSON)},
		{Name: runtimeapi.EnvProdReleaseID, Value: cell.Spec.Production.ReleaseID},
	}
	selector := map[string]string{
		ids.CellLabelKey:      cell.Name,
		ids.AnchorPodLabelKey: ids.ProdPodLabelVal,
	}
	cloneMounts := []corev1.VolumeMount{{Name: "prodspace", MountPath: "/prodspace"}}
	volumes := []corev1.Volume{{
		Name:         "prodspace",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	podLabels := selector
	sa := ""
	if r.GitBrokerURL != "" {
		podLabels = withBrokerClientLabel(selector)
		sa = runtimeapi.SAProd
		cloneMounts = append(cloneMounts, brokerTokenMount())
		volumes = append(volumes, brokerTokenVolume())
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.ProdDeployment}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		one := int32(1)
		dep.Spec.Replicas = &one
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		dep.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
			Spec: corev1.PodSpec{
				ServiceAccountName: sa,
				SecurityContext:    podSecurity(),
				InitContainers: []corev1.Container{{
					Name:            "clone",
					Image:           cell.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{runtimeapi.RuntimeBin, "prod-clone"},
					SecurityContext: containerSecurity(),
					// A clone, not a dev server: sized like settle.
					Resources:    settleResources(),
					Env:          cloneEnv,
					VolumeMounts: cloneMounts,
				}},
				Containers: []corev1.Container{{
					Name:            "prod",
					Image:           cell.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{runtimeapi.RuntimeBin, "prod-serve"},
					SecurityContext: containerSecurity(),
					Resources:       prodResources(cell),
					Env:             serveEnv,
					EnvFrom:         databaseEnvFrom(cell.Spec.Database.ProdSecretName, prodDBSecret),
					Ports:           []corev1.ContainerPort{{Name: "prod", ContainerPort: prodPort(cell)}},
					ReadinessProbe:  tcpReadiness(prodPort(cell)),
					// emptyDir only + no git env: cannot share dev state or creds.
					VolumeMounts: []corev1.VolumeMount{{Name: "prodspace", MountPath: "/prodspace"}},
				}},
				Volumes: volumes,
			},
		}
		return nil
	})
	if err != nil {
		return err
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.ProdService}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.Selector = selector
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

// recordCellsTotal sets agentcell_cells_total from a fresh List rather than
// incrementing/decrementing on create/delete, so the gauge self-heals on the
// next reconcile of any Cell instead of drifting if an update is ever missed.
func (r *CellReconciler) recordCellsTotal(ctx context.Context, ns string) error {
	var list acv1.CellList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return err
	}
	cellsTotal.Set(float64(len(list.Items)))
	return nil
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
		// The anchor lives in another namespace, so it cannot be Owned — a
		// cross-namespace owner reference is not a thing Kubernetes has.
		// Without this watch a Cell that stops being schedulable keeps
		// reporting Ready and the machine it used to be on, indefinitely,
		// until some unrelated Session event happens to wake the reconciler.
		// That is precisely the opaque failure the placement fields exist to
		// remove, so observing it cannot itself depend on luck.
		Watches(&appsv1.StatefulSet{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []ctrl.Request {
				if obj.GetName() != ids.AnchorStatefulSet {
					return nil
				}
				cell := ids.CellFromNamespace(obj.GetNamespace())
				if cell == "" {
					return nil
				}
				return []ctrl.Request{{NamespacedName: types.NamespacedName{
					Namespace: r.ControlNamespace, Name: cell,
				}}}
			})).
		Named("cell").
		Complete(r)
}

// ensureQuota caps the whole Cell namespace.
func (r *CellReconciler) ensureQuota(ctx context.Context, cell *acv1.Cell, ns string) error {
	hard := cellQuota(cell)
	q := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "cell"}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, q, func() error {
		q.Spec.Hard = hard
		return nil
	})
	return err
}

// removeInCellProduction tears down the in-Cell production zone.
func (r *CellReconciler) removeInCellProduction(ctx context.Context, ns string) error {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.ProdDeployment}}
	if err := r.Delete(ctx, dep); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: ids.ProdService}}
	if err := r.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// handOff notifies an external deployer, once per release.
func (r *CellReconciler) handOff(ctx context.Context, cell *acv1.Cell) error {
	if runsProductionHere(cell) || cell.Spec.Production.ReleaseID == "" {
		return nil
	}
	if cell.Status.HandedOffRelease == cell.Spec.Production.ReleaseID {
		return nil
	}
	if err := r.notifyExternal(ctx, cell); err != nil {
		return err
	}
	// Recorded only on success, so a failed handoff is retried on the next
	// reconcile rather than silently dropped.
	cell.Status.HandedOffRelease = cell.Spec.Production.ReleaseID
	cell.Status.HandoffMessage = ""
	return nil
}

// Database secrets, copied per zone.
//
// Two names, never one shared: a preview runs code an agent has just written
// against data it may have just decided to migrate, and pointing it at the
// database production uses is the ordinary way a company loses a table.
// Production unset therefore means production has NO database — falling back
// to the dev one would be the exact mistake this shape exists to prevent.
const (
	devDBSecret  = "database-dev"
	prodDBSecret = "database-prod"
)

func (r *CellReconciler) copyDatabaseSecrets(ctx context.Context, cell *acv1.Cell, ns string) error {
	for _, m := range []struct{ from, to string }{
		{cell.Spec.Database.DevSecretName, devDBSecret},
		{cell.Spec.Database.ProdSecretName, prodDBSecret},
	} {
		if m.from == "" {
			continue
		}
		if err := r.copySecret(ctx, cell.Namespace, m.from, ns, m.to); err != nil {
			return err
		}
	}
	return nil
}

// databaseEnvFrom turns a secret into environment for a zone. Whole-secret
// rather than one fixed variable, because what a connection looks like is the
// framework's business — DATABASE_URL, PGHOST+PGUSER, a JDBC string — and all
// of those are just keys.
func databaseEnvFrom(declared, name string) []corev1.EnvFromSource {
	if declared == "" {
		return nil
	}
	return []corev1.EnvFromSource{{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
		},
	}}
}
