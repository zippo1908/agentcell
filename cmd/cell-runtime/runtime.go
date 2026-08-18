package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// runUserRuntime is PID 1 of a user's runtime pod: one tmux server, holding
// every session that user has open in this Cell.
//
// One server per user rather than one per session. The agent CLIs already
// manage conversations — Claude Code by an id the caller can choose, Codex by
// its own bookkeeping — so a second layer of per-session processes buys
// nothing and costs a pod each. What the platform owes them is a private
// $HOME to keep that state in and a terminal that outlives any single run.
//
// The runtime holds no credential: model keys arrive per window, over the
// exec channel, at the moment a session starts.
func runUserRuntime() error {
	uid := int64(os.Getuid())
	if err := ensurePrivateHome(uid); err != nil {
		return err
	}
	trustAllRepos()
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("the user runtime needs tmux, and it is not in this Cell's image: " +
			"install it in the devbox image")
	}
	sock := ids.TmuxSocket(uid)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	// A server with no sessions exits, so it is held open by one that is
	// never handed out. Windows for real sessions are added beside it.
	if out, err := tmux(sock, "new-session", "-d", "-s", ids.TmuxHolder, "-c", holderDir()); err != nil {
		return fmt.Errorf("tmux start: %v: %s", err, out)
	}
	fmt.Printf("user runtime: tmux server on %s\n", sock)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	for {
		select {
		case <-stop:
			fmt.Println("user runtime: stopping; worktrees stay for settle")
			return nil
		case <-time.After(30 * time.Second):
			// Reap: PID 1 in a pod inherits orphans from every window.
			for {
				var ws syscall.WaitStatus
				if pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil); pid <= 0 || err != nil {
					break
				}
			}
		}
	}
}

// runWindowOpen starts one session inside the user's runtime: its worktree,
// its window, its agent.
//
// Invoked by the control plane over exec. Everything that is not secret
// arrives in argv; the model credential arrives on stdin as KEY=VALUE lines
// and is set on the window alone. A key in argv would be readable from
// /proc by every other window this user has open — same user, but a session
// is still the boundary a credential is scoped to.
func runWindowOpen(args []string) error {
	// -restore rebuilds the terminal WITHOUT running the agent again.
	//
	// A runtime that disappears (evicted, node rebooted) takes its windows
	// with it, but not the work: the worktree is on the volume and the CLI's
	// own conversation is in the private $HOME. Re-running the agent would
	// duplicate whatever it had already done; restoring the window hands the
	// session back to its owner, who continues the conversation when they
	// choose — which is what the CLIs are good at.
	restore := len(args) > 0 && args[0] == "-restore"
	if restore {
		args = args[1:]
	}
	if len(args) < 1 || (!restore && len(args) < 2) {
		return fmt.Errorf("window-open: need [-restore] <session-id> <agent argv...>")
	}
	id, argv := args[0], args[1:]
	uid := int64(os.Getuid())
	if err := ensurePrivateHome(uid); err != nil {
		return err
	}
	// Read stdin BEFORE anything that needs a session value. An exec inherits
	// the runtime pod's environment, which is deliberately empty of
	// per-session detail — the task text, the base branch and the model key
	// all arrive here, and the briefing would otherwise be written blank.
	env, err := readEnvFromStdin()
	if err != nil {
		return err
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	// This runs as a fresh exec, so it does not inherit whatever PID 1 set
	// up; the trust entries are idempotent and cheap.
	trustAllRepos()
	wt := ids.WorktreePath(uid, id)
	// Every repository, not just the first: a project group exists so the
	// agent can see both halves of a change, and a terminal opened on half
	// the project would be exactly the failure it was meant to prevent.
	base := os.Getenv(runtimeapi.EnvBaseBranch)
	for _, rp := range reposFromEnv() {
		b := rp.Branch
		if b == "" {
			b = base
		}
		if err := prepareWorktreeFor(ids.WorktreeDirFor(uid, id, rp.Path), id, b, rp.Path); err != nil {
			return fmt.Errorf("repo %q: %w", rp.Name, err)
		}
	}
	// 0700 like everything else under the private tree: this holds the
	// conversation itself.
	if err := os.MkdirAll(ids.SessionStateDir(uid, id), 0o700); err != nil {
		return fmt.Errorf("session state dir: %w", err)
	}
	// The endpoint some CLIs use lives in a file next to that conversation,
	// and a connected account login lives in the same directory.
	if err := writeAgentConfig(); err != nil {
		return err
	}
	if err := writeAccountCredential(); err != nil {
		return err
	}
	if err := writeLibrary(wt); err != nil {
		return err
	}
	sock := ids.TmuxSocket(uid)
	window := ids.TmuxWindow(id)
	// Idempotent: a reconciler retries, and a second window for one session
	// would run the agent twice against the same worktree.
	if out, err := tmux(sock, "list-windows", "-a", "-F", "#{window_name}"); err == nil {
		for _, name := range strings.Split(out, "\n") {
			if strings.TrimSpace(name) == window {
				fmt.Printf("window %s already open\n", window)
				return nil
			}
		}
	}
	// The environment goes through a 0600 file the window sources and then
	// unlinks — NOT through `tmux new-window -e`, which would put the model
	// key in the tmux client's argv and therefore in /proc for every other
	// window this user has open. That is the exposure this whole path exists
	// to avoid; putting it back one layer down would be pointless.
	envFile := filepath.Join(ids.UserHome(uid), "env-"+id)
	if err := writeEnvFile(envFile, env); err != nil {
		return err
	}
	// The agent CLIs read modified Enter (Shift-Enter for a newline in a
	// prompt) and say so out loud when it is off. Set on the server, once,
	// so every window has it.
	_, _ = tmux(sock, "set", "-g", "extended-keys", "on")
	if out, err := tmux(sock, "new-window", "-d", "-t", ids.TmuxHolder, "-n", window, "-c", wt); err != nil {
		_ = os.Remove(envFile)
		return fmt.Errorf("tmux new-window: %v: %s", err, out)
	}
	if restore {
		// Source the environment so a follow-up runs with the model key, but
		// start nothing.
		if err := sendCommand(sock, window, []string{"true"}, false, id, envFile); err != nil {
			return err
		}
		fmt.Printf("window %s restored in %s\n", window, wt)
		return nil
	}
	if err := sendCommand(sock, window, argv, true, id, envFile); err != nil {
		return err
	}
	answerFirstRunPrompts(sock, window)
	fmt.Printf("window %s opened in %s\n", window, wt)
	return nil
}

// answerFirstRunPrompts clears the gates a CLI puts in front of a person on
// its first run in a directory.
//
// Kimi asks "Trust this folder?" before it will start, and every session
// gets a fresh state directory, so it asks EVERY time. Left alone, each new
// session sits on a menu waiting for a keypress — and the first thing the
// person says gets eaten by that menu instead of reaching the agent.
//
// Answered by looking, not blindly: sending keys into a terminal that is
// not showing the dialog would type into the agent's input box. So this
// reads the screen, acts only on what it finds, and gives up quietly.
func answerFirstRunPrompts(sock, window string) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		out, err := tmux(sock, "capture-pane", "-p", "-t", window)
		if err != nil {
			continue
		}
		if !strings.Contains(out, "Trust this folder") {
			continue
		}
		// The cursor starts on "Don't trust"; trusting is one line up.
		// Trusting is right here: this directory is a worktree the platform
		// just created from the project's own repository, in a container
		// that exists to run that project's code.
		if _, err := tmux(sock, "send-keys", "-t", window, "Up"); err != nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = tmux(sock, "send-keys", "-t", window, "Enter")
		return
	}
}

// writeEnvFile lays down the window's environment, readable only by its
// owner. Created with O_EXCL so a stale file from a previous attempt cannot
// be silently reused, and 0600 before anything is written to it.
func writeEnvFile(path string, env []string) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("window environment: %w", err)
	}
	defer f.Close()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, err := fmt.Fprintf(f, "export %s=%s\n", k, shellQuote(v)); err != nil {
			return err
		}
	}
	return nil
}

// runWindowStatus answers whether one session's window still exists, and
// whether its agent has finished.
//
// The window is the session, not the pod. A pod that answers exec only means
// the runtime is up: the owner may have closed this window, or the runtime
// container may have restarted and taken every window with it while the pod
// itself stayed. Reporting on the pod would call all of those "running".
func runWindowStatus(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("window-status: need <session-id>")
	}
	id := args[0]
	sock := ids.TmuxSocket(int64(os.Getuid()))
	out, err := tmux(sock, "list-windows", "-a", "-F", "#{window_name}")
	alive := false
	if err == nil {
		for _, name := range strings.Split(out, "\n") {
			if strings.TrimSpace(name) == ids.TmuxWindow(id) {
				alive = true
				break
			}
		}
	}
	exit := ""
	if b, err := os.ReadFile(runtimeapi.DoneMarkerFor(id)); err == nil {
		exit = strings.TrimSpace(string(b))
	}
	// Whether anybody is watching. This is the signal that separates "a
	// person is using this" from "an agent finished and nobody came back",
	// and only tmux can answer it — the control plane sees a pod either way.
	// Reclamation policy hangs off it, so it is reported alongside the rest
	// rather than inferred from a timer.
	// Count only clients watching THIS session.
	//
	// Counting every client on the server meant one person reading session A
	// kept session B awake too — the tmux server is per USER, so "attached"
	// was answering a question about the user, not about the session, and
	// the idle clock it feeds is per session.
	attached := false
	if out, err := tmux(sock, "list-clients", "-F", "#{client_session}"); err == nil {
		want := "v-" + id
		for _, name := range strings.Split(out, "\n") {
			name = strings.TrimSpace(name)
			if name == want || strings.HasPrefix(name, want+"-") {
				attached = true
				break
			}
		}
	}
	// How long since this window last produced anything.
	//
	// The exit marker answers "did the one-shot command finish", which an
	// INTERACTIVE agent never does — it sits at its own prompt forever. For
	// those, the honest question is whether anything is happening, and tmux
	// already timestamps the last output per window. Without this an
	// interactive session either looks permanently busy (and never sleeps)
	// or permanently idle (and gets slept mid-answer).
	quiet := "-"
	if out, err := tmux(sock, "list-windows", "-a", "-F",
		"#{window_name} #{window_activity}"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			name, ts, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok || name != ids.TmuxWindow(id) {
				continue
			}
			if unix, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64); err == nil && unix > 0 {
				quiet = strconv.FormatInt(int64(time.Since(time.Unix(unix, 0)).Seconds()), 10)
			}
		}
	}
	// One line, parsed by the control plane: alive=<bool> exit=<code|->
	code := exit
	if code == "" {
		code = "-"
	}
	fmt.Printf("alive=%t exit=%s attached=%t quiet=%s\n", alive, code, attached, quiet)
	if !alive {
		// Non-zero so a caller can branch on the exit status alone.
		os.Exit(3)
	}
	return nil
}

// runWindowClose ends a session's window. The worktree is left alone: settle
// owns it, and reclaiming work is never this applet's job.
func runWindowClose(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("window-close: need <session-id>")
	}
	sock := ids.TmuxSocket(int64(os.Getuid()))
	// Missing is success: this runs on the way to settle, and a window that
	// is already gone is the state we wanted.
	_, _ = tmux(sock, "kill-window", "-t", ids.TmuxWindow(args[0]))
	return nil
}

// sendCommand types a command into a window. resetDone clears the completion
// marker first, so "is it still working?" answers about this run.
func sendCommand(sock, window string, argv []string, resetDone bool, sessionID, envFile string) error {
	line := shellJoin(argv)
	if resetDone {
		marker := shellQuote(runtimeapi.DoneMarkerFor(sessionID))
		line = "rm -f " + marker + "; " + line + "; printf '%s' \"$?\" > " + marker
	}
	if envFile != "" {
		// Sourced and unlinked in one go: the key lives in the window's
		// environment, and nowhere on disk a moment longer than it must.
		q := shellQuote(envFile)
		line = ". " + q + "; rm -f " + q + "; " + line
	}
	// C-u first: never concatenate onto a half-typed line.
	if out, err := tmux(sock, "send-keys", "-t", window, "C-u"); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	if out, err := tmux(sock, "send-keys", "-t", window, "-l", line); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	if out, err := tmux(sock, "send-keys", "-t", window, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %v: %s", err, out)
	}
	return nil
}

// readEnvFromStdin decodes the window's environment as one JSON object.
//
// It used to be KEY=VALUE lines, which meant a task containing a newline
// either corrupted the next variable or had to be refused outright — and
// refusing was wrong, because the console offers a multi-line box and a
// briefing is prose. JSON carries prose, and secrets still travel here
// rather than in argv, which /proc would expose to every other window.
func readEnvFromStdin() ([]string, error) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("window-open: stdin must be a JSON object of environment variables: %w", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic, so a failure is reproducible
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out, nil
}

func tmux(sock string, args ...string) (string, error) {
	full := append([]string{"-S", sock}, args...)
	b, err := exec.Command("tmux", full...).CombinedOutput()
	return string(b), err
}

// ensureUserRepo gives this user their own repository over the shared,
// read-only base (ADR-0012).
//
// --shared points the new repository's alternates at the project mirror, so
// reads resolve through to published history while WRITES land here. That is
// the whole isolation: an agent's unpublished commits sit in a 0700
// directory owned by one uid, and a peer's agent is refused by the kernel —
// no mediating daemon, and the agent keeps native git.
func ensureUserRepo(uid int64) (string, error) { return ensureUserRepoFor(uid, "") }

// ensureUserRepoFor is the user's own clone of ONE repository of the
// project, reading through alternates into the shared mirror the anchor
// keeps. One per repository, because a project group has several and a
// worktree can only come from the repository it belongs to.
func ensureUserRepoFor(uid int64, path string) (string, error) {
	repo := ids.UserRepoDirFor(uid, path)
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		// Keep it current with the base the anchor tracks.
		_ = git(repo, "fetch", "origin")
		return repo, nil
	}
	if err := os.MkdirAll(filepath.Dir(repo), 0o700); err != nil {
		return "", err
	}
	if err := ensureRepoTrusted(ids.RepoDir(path)); err != nil {
		return "", err
	}
	if err := gitNet("/", "clone", "--shared", "--no-checkout", ids.RepoDir(path), repo); err != nil {
		return "", fmt.Errorf("user repository: %w", err)
	}
	if err := ensureRepoTrusted(repo); err != nil {
		return "", err
	}
	if err := os.Chmod(repo, 0o700); err != nil {
		return "", err
	}
	// The shared mirror is the origin, so a fetch refreshes the base without
	// touching the network or the forge credential.
	return repo, nil
}

// prepareWorktree carves the session's worktree and lays down its briefing.
// Shared by the one-shot session applet and the runtime, so both produce the
// same thing.
func prepareWorktree(wt, id, base string) error {
	return prepareWorktreeFor(wt, id, base, "")
}

// prepareWorktreeFor carves one repository's worktree for a session. With a
// project group this runs once per repository, and they land side by side
// under the session directory — the agent has to see both halves of a
// change at once, which is the entire reason a project may hold several.
func prepareWorktreeFor(wt, id, base, path string) error {
	branch := ids.SessionBranch(id)
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
			return err
		}
		repo, err := ensureUserRepoFor(int64(os.Getuid()), path)
		if err != nil {
			return err
		}
		if err := git(repo, "worktree", "add", "-b", branch, wt, "origin/"+base); err != nil {
			return fmt.Errorf("worktree add: %w", err)
		}
	}
	_ = os.MkdirAll(filepath.Join(wt, ".agentcell"), 0o755)
	task := "# 本单任务\n\n" + os.Getenv(runtimeapi.EnvTask) + "\n"
	if desc := os.Getenv(runtimeapi.EnvDescription); desc != "" {
		_ = os.WriteFile(filepath.Join(wt, ".agentcell", "PRODUCT.md"),
			[]byte("# 产品描述(用户随预览持续校准)\n\n"+desc+"\n"), 0o644)
		task += "\n产品整体描述见 `.agentcell/PRODUCT.md`,以它为准对齐方向。\n"
	}
	if _, err := os.Stat(runtimeapi.KnowledgePath); err == nil {
		task += "\n项目持久知识库在 `" + runtimeapi.KnowledgePath + "/`(跨会话共享):" +
			"开工前浏览;本单学到的可复用经验(约定、坑、决策)以 md 文件沉淀回去。\n"
	}
	return os.WriteFile(filepath.Join(wt, ".agentcell", "TASK.md"), []byte(task), 0o644)
}

var _ = json.Marshal

// trustAllRepos marks every repository of the project as safe for git.
//
// One fixed path was enough when a project was one repository; a project
// group has several and none of them is at the old location, so trusting the
// old path alone left every git command refusing to run.
func trustAllRepos() {
	for _, r := range reposFromEnv() {
		_ = ensureRepoTrusted(ids.RepoDir(r.Path))
	}
}

// holderDir is where the tmux holder session sits: the single repository if
// there is one, otherwise the workspace itself, which is the only directory
// a project group has in common.
func holderDir() string {
	repos := reposFromEnv()
	if len(repos) == 1 {
		return ids.RepoDir(repos[0].Path)
	}
	return "/workspace"
}
