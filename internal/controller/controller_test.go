package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ids.PreviewService}, &corev1.Service{}); err != nil {
		t.Fatalf("preview service: %v", err)
	}

	var cell acv1.Cell
	if err := c.Get(ctx, types.NamespacedName{Namespace: controlNS, Name: "shop"}, &cell); err != nil {
		t.Fatal(err)
	}
	if cell.Status.PreviewPath != "/preview/shop/" {
		t.Errorf("previewPath = %q", cell.Status.PreviewPath)
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
