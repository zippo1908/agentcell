package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func joined(argv []string) string { return strings.Join(argv, " ") }

// Vite ignores PORT, so it has to be told on the command line — otherwise
// the server comes up on 5173 while the platform proxies 3000, and the
// preview is a blank page with a healthy-looking process behind it.
func TestVitePortGoesOnTheCommandLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"dev":"vite"},"devDependencies":{"vite":"^5"}}`)

	plan := detectPreview(dir, 3000)

	if !strings.Contains(joined(plan.Argv), "--port 3000") {
		t.Fatalf("vite must be given the port on the command line, got %v", plan.Argv)
	}
	if !strings.Contains(joined(plan.Argv), "--host 0.0.0.0") {
		t.Fatalf("a preview bound to loopback is invisible from outside the pod, got %v", plan.Argv)
	}
}

// Everything else in the JS world reads PORT, and passing vite's flags to it
// would make the dev server exit on an unknown argument.
func TestNonViteGetsThePortInTheEnvironmentNotAsFlags(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"dev":"next dev"},"dependencies":{"next":"^14"}}`)

	plan := detectPreview(dir, 3000)

	if joined(plan.Argv) != "npm run dev" {
		t.Fatalf("want a plain dev script, got %v", plan.Argv)
	}
	if !hasEnv(plan.Env, "PORT=3000") {
		t.Fatalf("want PORT in the environment, got %v", plan.Env)
	}
	if !hasEnv(plan.Env, "HOST=0.0.0.0") {
		t.Fatalf("want HOST in the environment, got %v", plan.Env)
	}
}

func TestFallsBackToStartWhenThereIsNoDevScript(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"start":"node server.js"}}`)

	if got := joined(detectPreview(dir, 3000).Argv); got != "npm start" {
		t.Fatalf("want npm start, got %q", got)
	}
}

func TestDjangoIsServedOnAllInterfaces(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "manage.py", "# django")

	got := joined(detectPreview(dir, 8000).Argv)
	if !strings.Contains(got, "runserver 0.0.0.0:8000") {
		t.Fatalf("want a Django dev server on 0.0.0.0, got %q", got)
	}
}

// A package.json and an index.html together is a JS app whose index.html is
// a template. Serving the file would show the un-built template.
func TestAnAppBeatsItsOwnIndexHTML(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"dev":"vite"},"devDependencies":{"vite":"^5"}}`)
	write(t, dir, "index.html", "<html>%VITE_TEMPLATE%</html>")

	if got := joined(detectPreview(dir, 3000).Argv); !strings.HasPrefix(got, "npm run dev") {
		t.Fatalf("the app must win over the static file, got %q", got)
	}
}

func TestAPlainPageIsServedAsFiles(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("httpd"); err != nil {
			t.Skip("no static server on PATH to serve with")
		}
	}
	dir := t.TempDir()
	write(t, dir, "index.html", "<html>hi</html>")

	plan := detectPreview(dir, 3000)
	if len(plan.Argv) == 0 {
		t.Fatalf("a directory with an index.html can always be served: %s", plan.Why)
	}
}

// The point of the whole file: when there is nothing to serve, say so. The
// version of this that returned an empty command and no reason is what left
// projects with a blank preview and nowhere to look.
func TestNothingToServeComesWithAReason(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "README.md", "# just some notes")

	plan := detectPreview(dir, 3000)
	if len(plan.Argv) != 0 {
		t.Fatalf("nothing here should be servable, got %v", plan.Argv)
	}
	if strings.TrimSpace(plan.Why) == "" {
		t.Fatal("no preview without a reason is the bug this replaced")
	}
}

// A half-written package.json must not take the preview down with it.
func TestBrokenPackageJSONFallsThroughInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts": {"dev":`)
	write(t, dir, "manage.py", "# django")

	if got := joined(detectPreview(dir, 8000).Argv); !strings.Contains(got, "runserver") {
		t.Fatalf("want the next rule to apply, got %q", got)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// A one-element command is a shell line — the Cell API has always said so.
//
// The anchor exec'd it as a filename instead, which is how a preview command
// of `busybox httpd -f -p 3000 -h .` became:
//
//	exec: "busybox httpd -f -p 3000 -h .": executable file not found in $PATH
//
// repeating forever behind a readiness probe that held the anchor NotReady
// for sixteen hours.
func TestAOneElementCommandIsAShellLine(t *testing.T) {
	got := execArgv([]string{"busybox httpd -f -p 3000 -h ."})

	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("want it run through a shell, got %v", got)
	}
	if got[2] != "busybox httpd -f -p 3000 -h ." {
		t.Fatalf("the line must reach the shell intact, got %q", got[2])
	}
}

// A real argv must be exec'd as-is: wrapping it would re-introduce a shell
// between the platform and a command it already knows how to run.
func TestARealArgvIsLeftAlone(t *testing.T) {
	in := []string{"npm", "run", "dev", "--", "--host", "0.0.0.0"}

	got := execArgv(in)

	if joined(got) != joined(in) {
		t.Fatalf("want %v unchanged, got %v", in, got)
	}
}
