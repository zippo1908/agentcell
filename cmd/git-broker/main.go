// git-broker is the only AgentCell component that holds forge credentials.
// It is an authenticating git smart-HTTP proxy (ADR-0005): workload pods
// (anchor, settle, prod-clone) reach it instead of the real remote and
// authenticate with their projected ServiceAccount token. The broker maps
// that token to the pod's namespace (= Cell identity) via TokenReview,
// injects the real forge credential, and proxies to the actual remote. No
// workload ever holds the forge token.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/version"
)

type server struct {
	k8s        client.Client        // reads Cell CRs and git Secrets
	auth       kubernetes.Interface // TokenReview
	creds      *credProvider
	controlNS  string
	enforceRef bool
}

func main() {
	var (
		addr      = flag.String("addr", ":8080", "listen address")
		controlNS = flag.String("control-namespace", envOr("AGENTCELL_NAMESPACE", "agentcell-system"),
			"namespace holding Cell CRs and git Secrets")
		enforceRef  = flag.Bool("enforce-ref-policy", true, "restrict pushes to refs/heads/session/* (ADR-0005 v2)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("git-broker", version.String())
		return
	}

	cfg := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		die("scheme", err)
	}
	if err := acv1.AddToScheme(scheme); err != nil {
		die("agentcell scheme", err)
	}
	kc, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		die("k8s client", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		die("clientset", err)
	}

	s := &server{k8s: kc, auth: cs, creds: newCredProvider(), controlNS: *controlNS, enforceRef: *enforceRef}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/forge/", s.handleForge) // ADR-0006, control plane only
	mux.HandleFunc("/", s.handleGit)

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	fmt.Printf("git-broker %s listening on %s (controlNamespace=%s, refPolicy=%v)\n",
		version.String(), *addr, *controlNS, *enforceRef)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		die("serve", err)
	}
}

func (s *server) ctx() context.Context { return context.Background() }

func die(what string, err error) {
	fmt.Fprintf(os.Stderr, "git-broker: %s: %v\n", what, err)
	os.Exit(1)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
