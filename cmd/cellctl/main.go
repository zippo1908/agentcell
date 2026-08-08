// cellctl is the operator CLI: every platform operation is available here
// before any web UI exists. It talks straight to the Kubernetes API using
// the AgentCell scheme; kubeconfig resolution follows kubectl conventions.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/agentcell/agentcell/api/v1alpha1"
	"github.com/agentcell/agentcell/internal/access"
	"github.com/agentcell/agentcell/internal/version"
	"github.com/agentcell/agentcell/pkg/ids"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cellctl: "+err.Error())
		os.Exit(1)
	}
}

func usage() error {
	return fmt.Errorf(`usage:
  cellctl cells
  cellctl cell create <name> --repo <url> --image <image> [--branch main]
          [--secret <git-secret>] [--preview "npm run dev" --preview-port 3000]
          [--description <text>]
  cellctl dispatch <cell> --task <text> --runner <claude|codex|pi>
          --provider <name> --cred <secret> [--model <m>] [--follow]
  cellctl sessions <cell>
  cellctl settle <session>
  cellctl version

  common flags: --namespace (default $AGENTCELL_NAMESPACE or agentcell-system)`)
}

func newClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := acv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

func nsDefault() string {
	if v := os.Getenv("AGENTCELL_NAMESPACE"); v != "" {
		return v
	}
	return "agentcell-system"
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	ctx := context.Background()
	switch args[0] {
	case "version":
		fmt.Println("cellctl", version.String())
		return nil
	case "cells":
		return listCells(ctx, args[1:])
	case "cell":
		if len(args) >= 3 && args[1] == "create" {
			return createCell(ctx, args[2], args[3:])
		}
		return usage()
	case "dispatch":
		if len(args) >= 2 {
			return dispatch(ctx, args[1], args[2:])
		}
		return usage()
	case "sessions":
		if len(args) >= 2 {
			return listSessions(ctx, args[1], args[2:])
		}
		return usage()
	case "settle":
		if len(args) >= 2 {
			return settle(ctx, args[1], args[2:])
		}
		return usage()
	}
	return usage()
}

func listCells(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cells", flag.ExitOnError)
	ns := fs.String("namespace", nsDefault(), "")
	_ = fs.Parse(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	var list acv1.CellList
	if err := c.List(ctx, &list, client.InNamespace(*ns)); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPHASE\tSLOTS\tPREVIEW\tREPO")
	for i := range list.Items {
		cl := &list.Items[i]
		maxs := cl.Spec.MaxSessions
		if maxs == 0 {
			maxs = 2
		}
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s\t%s\n", cl.Name, cl.Status.Phase,
			cl.Status.ActiveSessions, maxs, cl.Status.PreviewPath, cl.Spec.Repo.URL)
	}
	return tw.Flush()
}

func createCell(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet("cell create", flag.ExitOnError)
	ns := fs.String("namespace", nsDefault(), "")
	repo := fs.String("repo", "", "git https clone url (required)")
	image := fs.String("image", "", "devbox image (required)")
	branch := fs.String("branch", "main", "")
	secret := fs.String("secret", "", "basic-auth git secret in control namespace")
	preview := fs.String("preview", "", `preview command, e.g. "npm run dev"`)
	previewPort := fs.Int("preview-port", 3000, "")
	desc := fs.String("description", "", "initial product description")
	maxSessions := fs.Int("max-sessions", 2, "slot count")
	_ = fs.Parse(args)
	if *repo == "" || *image == "" {
		return fmt.Errorf("--repo and --image are required")
	}
	if err := ids.ValidateCellName(name); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	cell := &acv1.Cell{}
	cell.Name, cell.Namespace = name, *ns
	cell.Spec = acv1.CellSpec{
		Repo:        acv1.RepoSpec{URL: *repo, Branch: *branch, SecretName: *secret},
		Image:       *image,
		Description: *desc,
		MaxSessions: int32(*maxSessions),
	}
	if *preview != "" {
		cell.Spec.Preview = acv1.PreviewSpec{
			Command: strings.Fields(*preview),
			Port:    int32(*previewPort),
		}
	}
	if err := c.Create(ctx, cell); err != nil {
		return err
	}
	fmt.Printf("cell/%s created; preview will live at /preview/%s/\n", name, name)
	return nil
}

func dispatch(ctx context.Context, cellName string, args []string) error {
	fs := flag.NewFlagSet("dispatch", flag.ExitOnError)
	ns := fs.String("namespace", nsDefault(), "")
	task := fs.String("task", "", "work order (required)")
	runner := fs.String("runner", "claude", strings.Join(access.Runners(), "|"))
	provider := fs.String("provider", "", "provider name (required)")
	model := fs.String("model", "", "")
	cred := fs.String("cred", "", "credential secret in control namespace (required)")
	follow := fs.Bool("follow", false, "point the resident preview at this session")
	_ = fs.Parse(args)
	if *task == "" || *provider == "" || *cred == "" {
		return fmt.Errorf("--task, --provider and --cred are required")
	}
	reg, err := access.Load()
	if err != nil {
		return err
	}
	if _, err := reg.Resolve(*runner, *provider, *model); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	id := ids.NewSessionID()
	sess := &acv1.Session{}
	sess.Name, sess.Namespace = ids.SessionName(id), *ns
	sess.Spec = acv1.SessionSpec{
		Cell: cellName, Task: *task, Runner: *runner, Provider: *provider,
		Model: *model, CredentialSecret: *cred, FollowPreview: *follow,
	}
	if err := c.Create(ctx, sess); err != nil {
		return err
	}
	fmt.Printf("session/%s dispatched to cell/%s (branch will be %s)\n",
		sess.Name, cellName, ids.SessionBranch(id))
	return nil
}

func listSessions(ctx context.Context, cellName string, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	ns := fs.String("namespace", nsDefault(), "")
	_ = fs.Parse(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	var list acv1.SessionList
	if err := c.List(ctx, &list, client.InNamespace(*ns)); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPHASE\tRUNNER\tPROVIDER\tBRANCH\tMESSAGE")
	for i := range list.Items {
		s := &list.Items[i]
		if s.Spec.Cell != cellName {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Status.Phase,
			s.Spec.Runner, s.Spec.Provider, s.Status.Branch, s.Status.Message)
	}
	return tw.Flush()
}

func settle(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet("settle", flag.ExitOnError)
	ns := fs.String("namespace", nsDefault(), "")
	_ = fs.Parse(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	sess := &acv1.Session{}
	sess.Name, sess.Namespace = name, *ns
	if err := c.Delete(ctx, sess); err != nil {
		return err
	}
	fmt.Printf("session/%s deleting — settle runs before the record disappears\n", name)
	return nil
}
