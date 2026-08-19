package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Working out how to serve a checkout, so nobody has to type it.
//
// The create-a-project form used to ask for a preview command. It was the
// last free-text field in a form that had learned to offer everything else,
// and it asked for something the asker usually does not know on day one —
// the repository is often empty at that point, and the answer is written in
// its package.json anyway. Left blank, which is what happened, there was no
// preview at all and nothing said why.
//
// So the platform reads the checkout and decides. Getting it wrong is cheap
// (a preview that does not come up, with the reason in the log); getting it
// right is most of the value of the preview.

// previewPlan is what detection concluded: either a command to run, or a
// reason nobody should expect a preview.
type previewPlan struct {
	Argv []string
	// Env is added to the inherited environment. Most JS toolchains take a
	// port from PORT rather than a flag, and the ones that take a flag
	// disagree about its spelling.
	Env []string
	// Why is set when Argv is empty: a legible sentence, because "no
	// preview" with no reason is the failure this whole file exists to stop.
	Why string
}

// detectPreview inspects a checkout and returns how to serve it.
//
// Order matters: a repository that is both a Node app and has an index.html
// is a Node app whose index.html is a template. First match wins.
func detectPreview(dir string, port int) previewPlan {
	portEnv := []string{
		fmt.Sprintf("PORT=%d", port),
		// Binding to loopback is the usual default and is invisible from
		// outside the pod, which reads exactly like a broken preview.
		"HOST=0.0.0.0",
	}

	if pkg, ok := readPackageJSON(filepath.Join(dir, "package.json")); ok {
		switch {
		case pkg.Scripts["dev"] != "":
			// Vite is worth special-casing because it ignores PORT, and it
			// is the toolchain most of these repositories use.
			if pkg.dependsOn("vite") {
				return previewPlan{
					Argv: []string{"npm", "run", "dev", "--", "--host", "0.0.0.0", "--port", fmt.Sprint(port)},
					Env:  portEnv,
				}
			}
			return previewPlan{Argv: []string{"npm", "run", "dev"}, Env: portEnv}
		case pkg.Scripts["start"] != "":
			return previewPlan{Argv: []string{"npm", "start"}, Env: portEnv}
		}
	}

	if exists(filepath.Join(dir, "manage.py")) {
		return previewPlan{
			Argv: []string{"python3", "manage.py", "runserver", fmt.Sprintf("0.0.0.0:%d", port)},
			Env:  portEnv,
		}
	}

	// A checkout with a page in it can be served as files. This is the case
	// that used to need somebody to know a one-liner.
	if exists(filepath.Join(dir, "index.html")) {
		if httpd, err := exec.LookPath("httpd"); err == nil {
			return previewPlan{Argv: []string{httpd, "-f", "-p", fmt.Sprint(port), "-h", "."}, Env: portEnv}
		}
		if py, err := exec.LookPath("python3"); err == nil {
			return previewPlan{Argv: []string{py, "-m", "http.server", fmt.Sprint(port), "--bind", "0.0.0.0"}, Env: portEnv}
		}
	}

	return previewPlan{Why: "这个仓库里没有可识别的启动方式:没有 package.json 的 dev/start 脚本,没有 manage.py,根目录也没有 index.html。"}
}

type packageJSON struct {
	Scripts map[string]string `json:"scripts"`
	Deps    map[string]string `json:"dependencies"`
	DevDeps map[string]string `json:"devDependencies"`
}

func (p packageJSON) dependsOn(name string) bool {
	if _, ok := p.Deps[name]; ok {
		return true
	}
	_, ok := p.DevDeps[name]
	return ok
}

// readPackageJSON tolerates a broken file rather than failing the preview:
// an unparsable package.json is a reason to fall through to the next rule,
// not to refuse to serve anything.
func readPackageJSON(path string) (packageJSON, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return packageJSON{}, false
	}
	var p packageJSON
	if err := json.Unmarshal(raw, &p); err != nil {
		return packageJSON{}, false
	}
	return p, true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
