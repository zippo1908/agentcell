package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

const controlNS = "agentcell-system"

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := acv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func testCell() *acv1.Cell {
	c := &acv1.Cell{}
	c.Name, c.Namespace = "shop", controlNS
	c.Spec = acv1.CellSpec{
		Repo:  acv1.RepoSpec{URL: "https://example.com/shop.git"},
		Image: "ghcr.io/agentcell/devbox:latest",
		Preview: acv1.PreviewSpec{
			Command: []string{"npm", "run", "dev"},
			Port:    5173,
		},
	}
	return c
}

func newFake(t *testing.T, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&acv1.Cell{}, &acv1.Session{}).
		Build()
}

func reconcileCell(t *testing.T, c client.Client) {
	t.Helper()
	r := &CellReconciler{Client: c}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}}
	// Twice: first pass adds the finalizer and requeues implicitly.
	for range 2 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("cell reconcile: %v", err)
		}
	}
}

func TestCellReconcileCreatesWorkloadResources(t *testing.T) {
	c := newFake(t, testCell())
	reconcileCell(t, c)
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")

	if err := c.Get(ctx, types.NamespacedName{Name: ns}, &corev1.Namespace{}); err != nil {
		t.Fatalf("workload namespace: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.WorkspacePVC}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("pvc: %v", err)
	}
	var sts appsv1.StatefulSet
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.AnchorStatefulSet}, &sts); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if got := sts.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort; got != 5173 {
		t.Errorf("preview port = %d, want 5173", got)
	}
	if got := sts.Spec.Template.Spec.Containers[0].ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Errorf("anchor imagePullPolicy = %s, want %s", got, corev1.PullIfNotPresent)
	}
	// Readiness must gate on the preview port so "Ready" means "serving",
	// not "container started" — otherwise the proxy 502s on early hits.
	if p := sts.Spec.Template.Spec.Containers[0].ReadinessProbe; p == nil || p.TCPSocket == nil {
		t.Error("anchor with a preview command must have a TCP readiness probe")
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.PreviewService}, &corev1.Service{}); err != nil {
		t.Fatalf("preview service: %v", err)
	}
	// Namespace locked to default-deny + explicit allows.
	var deny netv1.NetworkPolicy
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "default-deny"}, &deny); err != nil {
		t.Fatalf("default-deny netpol: %v", err)
	}
	if len(deny.Spec.PolicyTypes) != 2 {
		t.Errorf("default-deny must cover both ingress and egress, got %v", deny.Spec.PolicyTypes)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "allow-egress"}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("allow-egress netpol: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "allow-control-plane-ingress"}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("ingress netpol: %v", err)
	}

	var cell acv1.Cell
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if cell.Status.PreviewPath != "/preview/shop/" {
		t.Errorf("previewPath = %q", cell.Status.PreviewPath)
	}
}

func TestCellFinalizeWaitsForWorkloadNamespaceDeletion(t *testing.T) {
	ctx := context.Background()
	cell := testCell()
	cell.Finalizers = []string{cellFinalizer}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ids.WorkloadNamespace(cell.Name)}}
	c := newFake(t, cell, ns)
	r := &CellReconciler{Client: c}

	result, err := r.finalize(ctx, cell)
	if err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	if result.RequeueAfter != 2*time.Second {
		t.Fatalf("first finalize requeue = %s, want 2s", result.RequeueAfter)
	}
	if len(cell.Finalizers) != 1 || cell.Finalizers[0] != cellFinalizer {
		t.Fatalf("finalizer removed before namespace deletion completed: %v", cell.Finalizers)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: ns.Name}, &corev1.Namespace{}); err == nil {
		t.Fatal("workload namespace still exists after delete request")
	}

	result, err = r.finalize(ctx, cell)
	if err != nil {
		t.Fatalf("second finalize: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("second finalize requeue = %s, want no requeue", result.RequeueAfter)
	}
	if len(cell.Finalizers) != 0 {
		t.Fatalf("finalizer retained after namespace deletion: %v", cell.Finalizers)
	}
}
func envMap(vars []corev1.EnvVar) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, e := range vars {
		m[e.Name] = e
	}
	return m
}

// In broker mode, no workload container that runs (or hosts) repo-controlled
// code may carry a forge credential; it gets the broker URL + cell name and
// authenticates with its ServiceAccount token instead (ADR-0005).
func TestBrokerModeStripsForgeCredentialsFromWorkloads(t *testing.T) {
	cellCR := testCell()
	cellCR.Spec.Repo.SecretName = "git-cred"
	cellCR.Spec.Production = acv1.ProductionSpec{ReleaseID: ids.NewSessionID()}
	c := newFake(t, cellCR)
	r := &CellReconciler{Client: c, GitBrokerURL: "http://git-broker.agentcell-system.svc:8080"}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: "shop"}}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")

	// Dedicated per-role ServiceAccounts exist.
	for _, sa := range []string{runtimeapi.SAAnchor, runtimeapi.SASettle, runtimeapi.SAProd} {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: sa}, &corev1.ServiceAccount{}); err != nil {
			t.Errorf("service account %q: %v", sa, err)
		}
	}

	var sts appsv1.StatefulSet
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.AnchorStatefulSet}, &sts); err != nil {
		t.Fatal(err)
	}
	anchorPod := sts.Spec.Template.Spec
	anchor := envMap(anchorPod.Containers[0].Env)
	if _, has := anchor["GIT_TOKEN"]; has {
		t.Error("anchor holds a forge token in broker mode")
	}
	if anchor[runtimeapi.EnvGitBroker].Value == "" {
		t.Error("anchor missing broker URL")
	}
	if anchor[runtimeapi.EnvCellName].Value != "shop" {
		t.Errorf("anchor cell name = %q", anchor[runtimeapi.EnvCellName].Value)
	}
	if anchorPod.ServiceAccountName != runtimeapi.SAAnchor {
		t.Errorf("anchor SA = %q, want %q", anchorPod.ServiceAccountName, runtimeapi.SAAnchor)
	}
	if sts.Spec.Template.Labels[runtimeapi.BrokerClientLabelKey] != runtimeapi.BrokerClientLabelVal {
		t.Error("anchor pod not labeled broker-client")
	}
	if !hasBrokerTokenVolume(anchorPod) {
		t.Error("anchor missing audience-bound broker token volume")
	}

	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.ProdDeployment}, &dep); err != nil {
		t.Fatal(err)
	}
	clone := envMap(dep.Spec.Template.Spec.InitContainers[0].Env)
	if _, has := clone["GIT_TOKEN"]; has {
		t.Error("prod-clone holds a forge token in broker mode")
	}
	if clone[runtimeapi.EnvGitBroker].Value == "" {
		t.Error("prod-clone missing broker URL")
	}

	// A dedicated broker-egress policy, scoped to broker-client pods only.
	var bpol netv1.NetworkPolicy
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "allow-broker-egress"}, &bpol); err != nil {
		t.Fatalf("allow-broker-egress: %v", err)
	}
	if bpol.Spec.PodSelector.MatchLabels[runtimeapi.BrokerClientLabelKey] != runtimeapi.BrokerClientLabelVal {
		t.Error("broker-egress policy is not scoped to broker-client pods")
	}
}

func hasBrokerTokenVolume(spec corev1.PodSpec) bool {
	for _, v := range spec.Volumes {
		if v.Projected != nil {
			for _, s := range v.Projected.Sources {
				if s.ServiceAccountToken != nil && s.ServiceAccountToken.Audience == runtimeapi.BrokerAudience {
					return true
				}
			}
		}
	}
	return false
}

// The session pod runs untrusted code and must carry no SA token.
func TestSessionPodHasNoServiceAccountToken(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), newSession(name, "t"))
	r := sessionReconciler(t, c)
	r.GitBrokerURL = "http://git-broker:8080"
	reconcileSession(t, r, name, 3)
	var pod corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ids.WorkloadNamespace("shop"), Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("session pod must set automountServiceAccountToken=false")
	}
}

func credSecret(name string) *corev1.Secret {
	s := &corev1.Secret{}
	s.Name, s.Namespace = name, controlNS
	s.Data = map[string][]byte{"key": []byte("sk-test")}
	return s
}

func newSession(name, task string) *acv1.Session {
	sess := &acv1.Session{}
	sess.Name, sess.Namespace = name, controlNS
	sess.Spec = acv1.SessionSpec{
		Cell: "shop", Task: task, Runner: "claude", Provider: "aliyun-bailian",
		Model: "qwen3-coder-plus", CredentialSecret: "bailian-key",
	}
	return sess
}

func sessionReconciler(t *testing.T, c client.Client) *SessionReconciler {
	t.Helper()
	reg, err := access.Load()
	if err != nil {
		t.Fatal(err)
	}
	return &SessionReconciler{Client: c, Registry: reg}
}

func reconcileSession(t *testing.T, r *SessionReconciler, name string, times int) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: controlNS, Name: name}}
	for range times {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("session reconcile %s: %v", name, err)
		}
	}
}

func TestSessionDispatchCreatesPodWithPerSessionCredential(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), newSession(name, "build the cart page"))
	r := sessionReconciler(t, c)
	reconcileSession(t, r, name, 3) // id → finalizer → dispatch

	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")

	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pod); err != nil {
		t.Fatalf("session pod: %v", err)
	}
	// Credential must arrive via the per-session secret indirection, never
	// as a literal in the pod spec.
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Value == "sk-test" {
			t.Errorf("literal credential leaked into pod spec via %s", env.Name)
		}
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.SessionSecretName(id)}, &corev1.Secret{}); err != nil {
		t.Fatalf("per-session secret: %v", err)
	}

	var sess acv1.Session
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &sess); err != nil {
		t.Fatal(err)
	}
	if sess.Status.Phase != acv1.SessionRunning {
		t.Errorf("phase = %s, want Running", sess.Status.Phase)
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Error("session pod must run as non-root")
	}
	if got := pod.Spec.Containers[0].ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Errorf("session imagePullPolicy = %s, want %s", got, corev1.PullIfNotPresent)
	}
	var cell acv1.Cell
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if len(cell.Status.SlotLeases) != 1 || cell.Status.SlotLeases[0] != id {
		t.Errorf("slot lease not recorded: %v", cell.Status.SlotLeases)
	}
}

func TestSlotLeaseClaimIdempotentAndRelease(t *testing.T) {
	id1, id2, id3 := ids.NewSessionID(), ids.NewSessionID(), ids.NewSessionID()
	s1 := newSession(ids.SessionName(id1), "a")
	c := newFake(t, testCell(), credSecret("bailian-key"), s1)
	r := sessionReconciler(t, c)
	ctx := context.Background()

	claim := func(sessID string) bool {
		ok, err := r.claimSlot(ctx, s1, sessID)
		if err != nil {
			t.Fatalf("claimSlot(%s): %v", sessID, err)
		}
		return ok
	}
	if !claim(id1) || !claim(id1) {
		t.Fatal("claim must succeed and be idempotent for the same id")
	}
	if !claim(id2) {
		t.Fatal("second slot should be free (maxSessions=2)")
	}
	if claim(id3) {
		t.Fatal("third claim must be rejected — gate oversold")
	}
	r.releaseSlot(ctx, controlNS, "shop", id1)
	if !claim(id3) {
		t.Fatal("released slot must be claimable again")
	}
	// Double release is harmless.
	r.releaseSlot(ctx, controlNS, "shop", id1)
}

func TestSlotGateQueuesThirdSession(t *testing.T) {
	idA, idB, idC := ids.NewSessionID(), ids.NewSessionID(), ids.NewSessionID()
	c := newFake(t, testCell(), credSecret("bailian-key"),
		newSession(ids.SessionName(idA), "a"),
		newSession(ids.SessionName(idB), "b"),
		newSession(ids.SessionName(idC), "c"))
	r := sessionReconciler(t, c)
	reconcileSession(t, r, ids.SessionName(idA), 3)
	reconcileSession(t, r, ids.SessionName(idB), 3)
	reconcileSession(t, r, ids.SessionName(idC), 3)

	var third acv1.Session
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: ids.SessionName(idC)}, &third); err != nil {
		t.Fatal(err)
	}
	if third.Status.Phase != acv1.SessionQueued {
		t.Errorf("third session phase = %s, want Queued (maxSessions=2)", third.Status.Phase)
	}
}

func TestFollowPreviewFlipsCellTarget(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	sess := newSession(name, "restyle the landing page")
	sess.Spec.FollowPreview = true
	c := newFake(t, testCell(), credSecret("bailian-key"), sess)
	r := sessionReconciler(t, c)
	reconcileSession(t, r, name, 3)

	var cell acv1.Cell
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if cell.Spec.Preview.FollowSession != id {
		t.Errorf("followSession = %q, want %q", cell.Spec.Preview.FollowSession, id)
	}
	// The anchor must now serve the session worktree.
	if got := previewTargetDir(&cell); got != ids.WorktreePath(id) {
		t.Errorf("previewTargetDir = %q, want worktree", got)
	}
}

func TestNoProductionZoneBeforeFirstRelease(t *testing.T) {
	c := newFake(t, testCell())
	reconcileCell(t, c)
	err := c.Get(context.Background(),
		types.NamespacedName{Namespace: ids.WorkloadNamespace("shop"), Name: ids.ProdDeployment},
		&appsv1.Deployment{})
	if err == nil {
		t.Fatal("production deployment must not exist before the first release")
	}
	var cell acv1.Cell
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell)
	if cell.Status.ProductionPath != "" {
		t.Errorf("productionPath = %q, want empty before release", cell.Status.ProductionPath)
	}
}

func TestReleaseCreatesIsolatedProduction(t *testing.T) {
	cellCR := testCell()
	cellCR.Spec.Production = acv1.ProductionSpec{ReleaseID: ids.NewSessionID()} // command falls back to preview's
	c := newFake(t, cellCR)
	reconcileCell(t, c)
	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")

	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.ProdDeployment}, &dep); err != nil {
		t.Fatalf("prod deployment: %v", err)
	}
	// The isolation guarantee: the prod pod must never mount the dev-zone
	// PVC — debugging in the dev zone cannot reach production.
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			t.Fatalf("prod pod mounts PVC %q — dev/prod isolation broken", v.PersistentVolumeClaim.ClaimName)
		}
	}
	// Credential hygiene: git env only in the clone init container; the
	// serving container (which runs repo-controlled code) gets none.
	if len(dep.Spec.Template.Spec.InitContainers) != 1 ||
		dep.Spec.Template.Spec.InitContainers[0].Command[1] != "prod-clone" {
		t.Fatal("prod pod must clone via a dedicated init container")
	}
	if got := dep.Spec.Template.Spec.InitContainers[0].ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Errorf("prod clone imagePullPolicy = %s, want %s", got, corev1.PullIfNotPresent)
	}
	serve := dep.Spec.Template.Spec.Containers[0]
	if serve.Command[1] != "prod-serve" {
		t.Errorf("serving container command = %v", serve.Command)
	}
	if serve.ReadinessProbe == nil || serve.ReadinessProbe.TCPSocket == nil {
		t.Error("prod serving container must have a TCP readiness probe")
	}
	if got := serve.ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Errorf("prod serve imagePullPolicy = %s, want %s", got, corev1.PullIfNotPresent)
	}
	if len(serve.EnvFrom) != 0 {
		t.Error("serving container must not inherit the git credential secret")
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.ProdService}, &corev1.Service{}); err != nil {
		t.Fatalf("prod service: %v", err)
	}
	var cell acv1.Cell
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if cell.Status.ProductionPath != "/app/shop/" {
		t.Errorf("productionPath = %q, want /app/shop/", cell.Status.ProductionPath)
	}
}

// Crash between terminal-status write and lease release: the next
// reconcile of the terminal session must return the slot.
func TestTerminalSessionReleasesLeakedLease(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	sess := newSession(name, "t")
	cellCR := testCell()
	cellCR.Status.SlotLeases = []string{id} // leaked lease
	c := newFake(t, cellCR, credSecret("bailian-key"), sess)
	r := sessionReconciler(t, c)
	ctx := context.Background()

	// Simulate the crash aftermath: session already terminal with its id.
	var s acv1.Session
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	s.Status.SessionID = id
	s.Status.Phase = acv1.SessionSettled
	if err := c.Status().Update(ctx, &s); err != nil {
		t.Fatal(err)
	}

	reconcileSession(t, r, name, 1)
	var cell acv1.Cell
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if len(cell.Status.SlotLeases) != 0 {
		t.Fatalf("leaked lease not reclaimed on terminal reconcile: %v", cell.Status.SlotLeases)
	}
}

// The cell controller's stale-lease sweep: a lease with no live session
// behind it (session deleted outright) is dropped.
func TestCellReconcileSweepsOrphanLeases(t *testing.T) {
	cellCR := testCell()
	cellCR.Status.SlotLeases = []string{"orphan-id-no-session"}
	c := newFake(t, cellCR)
	reconcileCell(t, c)
	var cell acv1.Cell
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if len(cell.Status.SlotLeases) != 0 {
		t.Fatalf("orphan lease survived the sweep: %v", cell.Status.SlotLeases)
	}
}

func TestSessionPodGoneLeadsToSettling(t *testing.T) {
	id := ids.NewSessionID()
	name := ids.SessionName(id)
	c := newFake(t, testCell(), credSecret("bailian-key"), newSession(name, "t"))
	r := sessionReconciler(t, c)
	reconcileSession(t, r, name, 3)

	ctx := context.Background()
	ns := ids.WorkloadNamespace("shop")
	// Simulate eviction: force-delete the pod, then reconcile again.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	if err := c.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, r, name, 1)

	var sess acv1.Session
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: name}, &sess); err != nil {
		t.Fatal(err)
	}
	if sess.Status.Phase != acv1.SessionSettling {
		t.Fatalf("phase = %s, want Settling", sess.Status.Phase)
	}
}
