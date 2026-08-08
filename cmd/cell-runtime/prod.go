package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// runProd is PID 1 of the production (正式区) pod. Every release rolls this
// pod; on boot it shallow-clones the release ref into its own emptyDir —
// it never touches the dev-zone PVC, so development debugging cannot
// affect what production serves.
func runProd() error {
	if err := ensureAskpass(); err != nil {
		return err
	}
	url := os.Getenv(runtimeapi.EnvRepoURL)
	ref := os.Getenv(runtimeapi.EnvProdRef)
	if url == "" || ref == "" {
		return fmt.Errorf("%s / %s not set", runtimeapi.EnvRepoURL, runtimeapi.EnvProdRef)
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(runtimeapi.EnvProdCmd)), &argv); err != nil || len(argv) == 0 {
		return fmt.Errorf("%s invalid: %v", runtimeapi.EnvProdCmd, err)
	}

	// Fresh, immutable release checkout.
	_ = os.RemoveAll(runtimeapi.ProdRepoPath)
	if err := os.MkdirAll(filepath.Dir(runtimeapi.ProdRepoPath), 0o755); err != nil {
		return err
	}
	if err := git("/", "clone", "--depth", "1", "--branch", ref, url, runtimeapi.ProdRepoPath); err != nil {
		return fmt.Errorf("clone release %q: %w", ref, err)
	}
	if sha, err := gitOut(runtimeapi.ProdRepoPath, "rev-parse", "HEAD"); err == nil {
		fmt.Printf("prod: serving release %s @ %s (release id %s)\n",
			ref, sha, os.Getenv(runtimeapi.EnvProdReleaseID))
	}

	go reapZombies()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	supervisePreview(argv, runtimeapi.ProdRepoPath, stop)
	return nil
}
