// celld is the AgentCell control plane: a controller-manager reconciling
// the Cell and Session CRDs, plus the HTTP surface (calibration UI, control
// API, and the resident product-preview reverse proxy).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/access"
	"github.com/zippo1908/agentcell/internal/controller"
	"github.com/zippo1908/agentcell/internal/version"
	"github.com/zippo1908/agentcell/internal/webui"
)

func main() {
	var (
		httpAddr    = flag.String("http-addr", ":8080", "UI/API/preview listen address")
		metricsAddr = flag.String("metrics-addr", ":8081", "Prometheus metrics address")
		controlNS   = flag.String("control-namespace", envOr("AGENTCELL_NAMESPACE", "agentcell-system"),
			"namespace holding Cell/Session CRs")
		providersDir = flag.String("providers-dir", "/etc/agentcell/providers.d",
			"directory of provider preset overlays (*.yaml)")
		tokenFile = flag.String("token-file", "/etc/agentcell/auth/tokens",
			"file of API access tokens (whitespace-separated); enables auth when present")
		allowNoAuth = flag.Bool("allow-no-auth", false,
			"start with the HTTP surface unauthenticated (dev only)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("celld", version.String())
		return
	}
	ctrl.SetLogger(logzap.New())
	log := ctrl.Log.WithName("celld")

	registry, err := loadRegistry(*providersDir)
	if err != nil {
		log.Error(err, "load provider registry")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "add client-go scheme")
		os.Exit(1)
	}
	if err := acv1.AddToScheme(scheme); err != nil {
		log.Error(err, "add agentcell scheme")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress: "0", // /healthz served on the main mux below
	})
	if err != nil {
		log.Error(err, "new manager")
		os.Exit(1)
	}
	if err := (&controller.CellReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		log.Error(err, "setup cell controller")
		os.Exit(1)
	}
	if err := (&controller.SessionReconciler{Client: mgr.GetClient(), Registry: registry}).SetupWithManager(mgr); err != nil {
		log.Error(err, "setup session controller")
		os.Exit(1)
	}

	auth := webui.NewAuthenticator(readTokenFile(*tokenFile))
	if !auth.Enabled() {
		if !*allowNoAuth {
			log.Error(nil, "no API tokens found and --allow-no-auth not set; refusing to expose an unauthenticated control plane",
				"tokenFile", *tokenFile)
			os.Exit(1)
		}
		log.Info("WARNING: HTTP surface is UNAUTHENTICATED (--allow-no-auth)")
	}

	ui := &webui.Handler{Client: mgr.GetClient(), Namespace: *controlNS, Registry: registry}
	mux := http.NewServeMux()
	auth.LoginRoutes(mux)
	mux.Handle("/", auth.Middleware(ui.Routes()))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = healthz.Ping(nil)
		w.WriteHeader(http.StatusOK)
	})
	// The HTTP surface starts with the manager so it uses the warmed cache.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		srv := &http.Server{Addr: *httpAddr, Handler: mux}
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		log.Info("http listening", "addr", *httpAddr, "controlNamespace", *controlNS)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	})); err != nil {
		log.Error(err, "add http runnable")
		os.Exit(1)
	}

	log.Info("starting", "version", version.String())
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited")
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// readTokenFile returns the token material, or "" if the file is absent —
// absence is a valid (dev) state that the caller gates on --allow-no-auth.
func readTokenFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func loadRegistry(dir string) (*access.Registry, error) {
	var overlays [][]byte
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			overlays = append(overlays, raw)
		}
	}
	return access.Load(overlays...)
}
