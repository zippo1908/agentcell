package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// The production (正式区) pod is split for credential hygiene:
//
//	prod-clone  init container — the only place with git credentials;
//	            runs nothing but this applet, materializes the release
//	            checkout and records the resolved SHA
//	prod-serve  serving container — executes the (repo-controlled) prod
//	            command with NO git environment at all
//
// Both only ever touch the pod-local emptyDir, never the dev-zone PVC.

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

const releaseShaFile = "/prodspace/RELEASE_SHA"

func runProdClone() error {
	if err := ensureAskpass(); err != nil {
		return err
	}
	url := os.Getenv(runtimeapi.EnvRepoURL)
	ref := os.Getenv(runtimeapi.EnvProdRef)
	if url == "" || ref == "" {
		return fmt.Errorf("%s / %s not set", runtimeapi.EnvRepoURL, runtimeapi.EnvProdRef)
	}

	// Fresh, immutable release checkout.
	_ = os.RemoveAll(runtimeapi.ProdRepoPath)
	if err := os.MkdirAll(runtimeapi.ProdRepoPath, 0o755); err != nil {
		return err
	}
	if shaRe.MatchString(ref) {
		// Bare commit SHA: clone --branch cannot take it; fetch it directly.
		if err := git(runtimeapi.ProdRepoPath, "init", "-q"); err != nil {
			return err
		}
		if err := git(runtimeapi.ProdRepoPath, "remote", "add", "origin", url); err != nil {
			return err
		}
		if err := git(runtimeapi.ProdRepoPath, "fetch", "--depth", "1", "origin", ref); err != nil {
			return fmt.Errorf("fetch release sha %q: %w", ref, err)
		}
		if err := git(runtimeapi.ProdRepoPath, "checkout", "--detach", "FETCH_HEAD"); err != nil {
			return err
		}
	} else {
		if err := git("/", "clone", "--depth", "1", "--branch", ref, url, runtimeapi.ProdRepoPath); err != nil {
			return fmt.Errorf("clone release %q: %w", ref, err)
		}
	}

	sha, err := gitOut(runtimeapi.ProdRepoPath, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve release sha: %w", err)
	}
	if err := os.WriteFile(releaseShaFile, []byte(sha+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("prod-clone: release %s resolved to %s (release id %s)\n",
		ref, sha, os.Getenv(runtimeapi.EnvProdReleaseID))
	return nil
}

func runProdServe() error {
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(runtimeapi.EnvProdCmd)), &argv); err != nil || len(argv) == 0 {
		return fmt.Errorf("%s invalid: %v", runtimeapi.EnvProdCmd, err)
	}
	if _, err := os.Stat(filepath.Join(runtimeapi.ProdRepoPath, ".git")); err != nil {
		return fmt.Errorf("release checkout missing (init container failed?): %w", err)
	}
	if sha, err := os.ReadFile(releaseShaFile); err == nil {
		fmt.Printf("prod-serve: serving %s(release id %s)\n", string(sha), os.Getenv(runtimeapi.EnvProdReleaseID))
	}
	go reapZombies()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	supervisePreview(argv, runtimeapi.ProdRepoPath, stop)
	return nil
}
